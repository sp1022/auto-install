// Package topology 提供配置模板和管理功能
package topology

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/logger"
)

// ConfigManager 配置管理器
type ConfigManager struct {
	config    *config.Config
	logger    *logger.Logger
	templates map[string]*template.Template
}

// NewConfigManager 创建配置管理器
func NewConfigManager(cfg *config.Config, log *logger.Logger) *ConfigManager {
	return &ConfigManager{
		config:    cfg,
		logger:    log,
		templates: make(map[string]*template.Template),
	}
}

// LoadTemplates 加载配置模板
func (m *ConfigManager) LoadTemplates() error {
	// 定义模板
	templates := map[string]string{
		"postgresql.conf": postgresqlConfTemplate,
		"pg_hba.conf":     pgHbaConfTemplate,
		"patroni.yml":     patroniYamlTemplate,
		"recovery.conf":   recoveryConfTemplate,
	}

	for name, content := range templates {
		tmpl, err := template.New(name).Parse(content)
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", name, err)
		}
		m.templates[name] = tmpl
	}

	m.logger.Info("Configuration templates loaded",
		logger.Fields{
			"templates": len(m.templates),
		})

	return nil
}

// GeneratePostgreSQLConfig 生成PostgreSQL配置
func (m *ConfigManager) GeneratePostgreSQLConfig(node *config.NodeConfig) (string, error) {
	tmpl, exists := m.templates["postgresql.conf"]
	if !exists {
		return "", fmt.Errorf("template not found")
	}

	// 准备模板数据
	data := struct {
		Node       *config.NodeConfig
		Config     *config.Config
		WALDir     string
		Extensions []string
		IsCitus    bool
		IsPatroni  bool
	}{
		Node:       node,
		Config:     m.config,
		WALDir:     node.WALDir,
		Extensions: m.config.Extensions,
		IsCitus:    m.config.DeployMode == config.ModeCitus,
		IsPatroni:  m.config.DeployMode == config.ModePatroni,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// GeneratePgHbaConfig 生成pg_hba.conf配置
func (m *ConfigManager) GeneratePgHbaConfig(node *config.NodeConfig) (string, error) {
	tmpl, exists := m.templates["pg_hba.conf"]
	if !exists {
		return "", fmt.Errorf("template not found")
	}

	// 准备网络段
	network := getNetworkFromHost(node.Host)

	data := struct {
		Node      *config.NodeConfig
		Config    *config.Config
		Network   string
		IsReplica bool
	}{
		Node:      node,
		Config:    m.config,
		Network:   network,
		IsReplica: !node.IsMaster,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// GeneratePatroniConfig 生成Patroni配置
func (m *ConfigManager) GeneratePatroniConfig(node *config.NodeConfig, etcdEndpoints []string) (string, error) {
	tmpl, exists := m.templates["patroni.yml"]
	if !exists {
		return "", fmt.Errorf("template not found")
	}

	replicationPassword, err := generateRandomPassword(16)
	if err != nil {
		return "", fmt.Errorf("failed to generate replication password: %w", err)
	}

	data := struct {
		Node                *config.NodeConfig
		Config              *config.Config
		EtcdEndpoints       []string
		ReplicationPassword string
	}{
		Node:                node,
		Config:              m.config,
		EtcdEndpoints:       etcdEndpoints,
		ReplicationPassword: replicationPassword,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// WriteConfigFile 写入配置文件
func (m *ConfigManager) WriteConfigFile(node *config.NodeConfig, configType, content string) error {
	var configPath string
	switch configType {
	case "postgresql.conf":
		configPath = filepath.Join(node.DataDir, "postgresql.conf")
	case "pg_hba.conf":
		configPath = filepath.Join(node.DataDir, "pg_hba.conf")
	case "patroni.yml":
		configPath = fmt.Sprintf("/etc/patroni/%s.yml", node.Name)
	default:
		return fmt.Errorf("unknown config type: %s", configType)
	}

	// 写入文件
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	m.logger.Info("Configuration file written",
		logger.Fields{
			"node":       node.Host,
			"configType": configType,
			"path":       configPath,
		})

	return nil
}

// ValidateConfig 验证配置
func (m *ConfigManager) ValidateConfig() error {
	// 验证部署模式
	switch m.config.DeployMode {
	case config.ModeStandalone, config.ModeMasterSlave, config.ModePatroni, config.ModeCitus:
		// 有效模式
	default:
		return fmt.Errorf("invalid deploy_mode: %s", m.config.DeployMode)
	}

	// 验证构建模式
	switch m.config.BuildMode {
	case config.BuildCompile, config.BuildDistribute:
		// 有效模式
	default:
		return fmt.Errorf("invalid build_mode: %s", m.config.BuildMode)
	}

	// 验证组配置
	if len(m.config.Groups) == 0 {
		return fmt.Errorf("no groups defined")
	}

	// 验证每个组
	for i, group := range m.config.Groups {
		if len(group.Nodes) == 0 {
			return fmt.Errorf("group %d has no nodes", i)
		}

		// 验证至少有一个主节点
		hasMaster := false
		for _, node := range group.Nodes {
			if node.IsMaster {
				hasMaster = true
				break
			}
		}
		if !hasMaster {
			return fmt.Errorf("group %d (%s) has no master node", i, group.Name)
		}
	}

	// 验证Citus配置
	if m.config.DeployMode == config.ModeCitus {
		if err := m.validateCitusConfig(); err != nil {
			return fmt.Errorf("citus config validation failed: %w", err)
		}
	}

	return nil
}

// validateCitusConfig 验证Citus配置
func (m *ConfigManager) validateCitusConfig() error {
	hasCoordinator := false
	hasWorker := false

	for _, group := range m.config.Groups {
		if group.Role == "coordinator" {
			hasCoordinator = true
		}
		if group.Role == "worker" {
			hasWorker = true
		}
	}

	if !hasCoordinator {
		return fmt.Errorf("citus mode requires at least one coordinator group")
	}

	if !hasWorker {
		return fmt.Errorf("citus mode requires at least one worker group")
	}

	return nil
}

// GetConfigDiff 获取配置差异
func (m *ConfigManager) GetConfigDiff(oldConfig, newConfig string) []string {
	oldLines := strings.Split(oldConfig, "\n")
	newLines := strings.Split(newConfig, "\n")

	diffs := make([]string, 0)

	// 简化的差异检测
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	for i := 0; i < maxLen; i++ {
		oldLine := ""
		newLine := ""

		if i < len(oldLines) {
			oldLine = strings.TrimSpace(oldLines[i])
		}
		if i < len(newLines) {
			newLine = strings.TrimSpace(newLines[i])
		}

		if oldLine != newLine && newLine != "" && !strings.HasPrefix(newLine, "#") {
			diffs = append(diffs, fmt.Sprintf("+ %s", newLine))
		}
	}

	return diffs
}

// BackupConfig 备份配置文件
func (m *ConfigManager) BackupConfig(node *config.NodeConfig, configType string) error {
	var configPath string
	switch configType {
	case "postgresql.conf":
		configPath = filepath.Join(node.DataDir, "postgresql.conf")
	case "pg_hba.conf":
		configPath = filepath.Join(node.DataDir, "pg_hba.conf")
	default:
		return fmt.Errorf("unknown config type: %s", configType)
	}

	// 读取现有配置
	content, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// 创建备份文件
	backupPath := configPath + ".bak"
	if err := os.WriteFile(backupPath, content, 0644); err != nil {
		return fmt.Errorf("failed to write backup: %w", err)
	}

	m.logger.Info("Configuration backed up",
		logger.Fields{
			"node":   node.Host,
			"config": configType,
			"backup": backupPath,
		})

	return nil
}

// getNetworkFromHost 从主机地址获取网络段
func getNetworkFromHost(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".") + ".0/24"
	}
	return host + "/32"
}

// 配置模板

var postgresqlConfTemplate = `# PostgreSQL configuration file
# Generated by pg-deploy

# Connection Settings
listen_addresses = '{{ .Node.Host }}'
port = {{ .Node.Port }}
max_connections = 200
superuser_reserved_connections = 3

# Memory Settings
shared_buffers = 256MB
effective_cache_size = 1GB
maintenance_work_mem = 64MB
work_mem = 16MB

# WAL Settings
wal_level = replica
max_wal_size = 1GB
min_wal_size = 80MB
{{ if .WALDir }}
# WAL directory is symlinked to {{ .WALDir }}
{{ end }}

# Replication Settings
{{ if eq .Config.DeployMode "master-slave" }}
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
{{ end }}

{{ if .IsCitus }}
# Citus Settings
shared_preload_libraries = 'citus'
citus.node_conninfo = 'host={{ .Node.Host }} port={{ .Node.Port }}'
{{ end }}

{{ if .IsPatroni }}
# Patroni Settings
wal_level = logical
max_wal_senders = 10
max_replication_slots = 10
hot_standby = on
{{ end }}

# Logging
logging_collector = on
log_directory = '/var/log/postgresql'
log_filename = 'postgresql-%Y-%m-%d_%H%M%S.log'
log_rotation_age = 1d
log_rotation_size = 100MB

# Extensions
{{ if .Extensions }}
shared_preload_libraries = '{{ join .Extensions "," }}'
{{ end }}
`

var pgHbaConfTemplate = `# PostgreSQL Client Authentication Configuration File
# Generated by pg-deploy

# TYPE  DATABASE        USER            ADDRESS                 METHOD

# Local connections
local   all             postgres                                peer
local   all             all                                     md5

# IPv4 local connections
host    all             all             127.0.0.1/32            md5
host    all             all             {{ .Network }}          md5

# Replication connections
{{ if .Node.IsMaster }}
host    replication     replicator      {{ .Network }}          md5
{{ end }}

{{ if eq .Config.DeployMode "patroni" }}
host    all             all             {{ .Network }}          md5
{{ end }}
`

var patroniYamlTemplate = `# Patroni Configuration for {{ .Node.Name }}
# Generated by pg-deploy

name: {{ .Node.Name }}
scope: pg-cluster

restapi:
  listen: 0.0.0.0:8008
  connect_address: {{ .Node.Host }}:8008

etcd3:
  hosts:
{{ range .EtcdEndpoints }}
    - {{ . }}
{{ end }}

postgresql:
  listen: 0.0.0.0:{{ .Node.Port }}
  connect_address: {{ .Node.Host }}:{{ .Node.Port }}
  data_dir: {{ .Node.DataDir }}
  bin_dir: /usr/local/pgsql/bin

  authentication:
    replication:
      username: replicator
      password: "{{ .ReplicationPassword }}"

  parameters:
    max_connections: 200
    shared_buffers: 256MB
    wal_level: logical
    hot_standby: on
    max_wal_senders: 10
    max_replication_slots: 10

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false
`

var recoveryConfTemplate = `# Standby recovery configuration
# Generated by pg-deploy

standby_mode = on
primary_conninfo = 'host={{ .MasterHost }} port={{ .MasterPort }} user=replicator'
primary_slot_name = {{ .SlotName }}
`

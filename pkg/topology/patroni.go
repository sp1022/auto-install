// Package topology 提供Patroni高可用集群管理功能
package topology

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// PatroniManager Patroni高可用集群管理器
type PatroniManager struct {
	config        *config.Config
	executor      *executor.Executor
	logger        *logger.Logger
	nodes         []*config.NodeConfig
	etcdEndpoints []string
	restPorts     map[string]int

	replicationPassword string
	superuserPassword   string
}

// NewPatroniManager 创建Patroni管理器
func NewPatroniManager(cfg *config.Config, exec *executor.Executor, log *logger.Logger) *PatroniManager {
	nodes := cfg.GetAllNodes()

	return &PatroniManager{
		config:        cfg,
		executor:      exec,
		logger:        log,
		nodes:         nodes,
		etcdEndpoints: []string{"localhost:2379"}, // 默认etcd端点
		restPorts:     make(map[string]int),
	}
}

// DeployEtcdCluster 部署etcd集群
func (m *PatroniManager) DeployEtcdCluster() error {
	m.logger.Info("Deploying etcd cluster",
		logger.Fields{"nodes": len(m.nodes)})

	etcdNodes := m.selectEtcdNodes()
	if len(etcdNodes) == 0 {
		return fmt.Errorf("no nodes available for etcd deployment")
	}

	if err := m.installEtcdCluster(etcdNodes); err != nil {
		return fmt.Errorf("failed to install etcd cluster dependencies: %w", err)
	}

	if len(etcdNodes) == 1 {
		if err := m.resetSingleNodeEtcd(etcdNodes[0]); err != nil {
			return fmt.Errorf("failed to reset single-node etcd environment: %w", err)
		}
	}

	// 生成etcd集群配置
	if err := m.configureEtcdCluster(etcdNodes); err != nil {
		return fmt.Errorf("failed to configure etcd cluster: %w", err)
	}

	// 启动etcd服务
	if err := m.startEtcdCluster(etcdNodes); err != nil {
		return fmt.Errorf("failed to start etcd cluster: %w", err)
	}

	// 验证etcd集群���康
	if err := m.validateEtcdCluster(etcdNodes); err != nil {
		return fmt.Errorf("etcd cluster validation failed: %w", err)
	}

	// 更新etcd端点列表
	m.updateEtcdEndpoints(etcdNodes)

	m.logger.Info("etcd cluster deployed successfully", logger.Fields{})
	return nil
}

func (m *PatroniManager) selectEtcdNodes() []*config.NodeConfig {
	selected := make([]*config.NodeConfig, 0, 3)
	seenHosts := make(map[string]bool)

	for _, node := range m.nodes {
		if seenHosts[node.Host] {
			continue
		}
		seenHosts[node.Host] = true
		selected = append(selected, node)
		if len(selected) == 3 {
			break
		}
	}

	return selected
}

func (m *PatroniManager) installEtcdCluster(nodes []*config.NodeConfig) error {
	installCmd := `
set -e
if command -v etcd >/dev/null 2>&1 && command -v etcdctl >/dev/null 2>&1; then
  exit 0
fi
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y etcd-server etcd-client
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y etcd
elif command -v yum >/dev/null 2>&1; then
  yum install -y etcd
elif command -v zypper >/dev/null 2>&1; then
  zypper --non-interactive install etcd
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache etcd
fi
command -v etcd >/dev/null 2>&1
command -v etcdctl >/dev/null 2>&1
`

	for _, node := range nodes {
		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, installCmd, true, false)
		if result.Error != nil {
			return fmt.Errorf("failed to install etcd on %s: %w", node.Host, result.Error)
		}
	}

	return nil
}

// configureEtcdCluster 配置etcd集群
func (m *PatroniManager) configureEtcdCluster(nodes []*config.NodeConfig) error {
	for i, node := range nodes {
		// 生成etcd配置
		etcdConfig := m.generateEtcdConfig(node, i, len(nodes))

		// 写入配置文件
		configFile := "/etc/etcd/etcd.yml"
		cmd := fmt.Sprintf("getent group etcd >/dev/null 2>&1 || groupadd --system etcd; "+
			"id etcd >/dev/null 2>&1 || useradd --system -g etcd -d /var/lib/etcd -s /sbin/nologin etcd; "+
			"mkdir -p /etc/etcd /var/lib/etcd && chown -R etcd:etcd /etc/etcd /var/lib/etcd && "+
			"cat > %s << 'EOF'\n%s\nEOF\nchown etcd:etcd %s",
			configFile, etcdConfig, configFile)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			return fmt.Errorf("failed to write etcd config on %s: %w", node.Host, result.Error)
		}

		m.logger.Info("etcd configured",
			logger.Fields{
				"node":  node.Host,
				"index": i,
			})
	}

	return nil
}

// generateEtcdConfig 生成etcd配置
func (m *PatroniManager) generateEtcdConfig(node *config.NodeConfig, index, total int) string {
	// 生成etcd集群成员列表
	var initialCluster []string
	for i, n := range m.nodes {
		if i >= total {
			break
		}
		memberName := fmt.Sprintf("etcd%d", i)
		peerURL := fmt.Sprintf("http://%s:2380", n.Host)
		initialCluster = append(initialCluster, fmt.Sprintf("%s=%s", memberName, peerURL))
	}

	thisMemberName := fmt.Sprintf("etcd%d", index)
	thisPeerURL := fmt.Sprintf("http://%s:2380", node.Host)
	thisClientURL := fmt.Sprintf("http://%s:2379", node.Host)

	config := fmt.Sprintf(`name: "%s"
data-dir: "/var/lib/etcd"
listen-peer-urls: "http://0.0.0.0:2380"
listen-client-urls: "http://0.0.0.0:2379"
initial-advertise-peer-urls: "%s"
advertise-client-urls: "%s"
initial-cluster: "%s"
initial-cluster-state: "new"
initial-cluster-token: "patroni-etcd-cluster"
`, thisMemberName, thisPeerURL, thisClientURL, strings.Join(initialCluster, ","))

	return config
}

// startEtcdCluster 启动etcd集群
func (m *PatroniManager) startEtcdCluster(nodes []*config.NodeConfig) error {
	for _, node := range nodes {
		// 创建systemd服务文件
		serviceContent := m.generateEtcdServiceFile()

		serviceFile := "/etc/systemd/system/etcd.service"
		cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", serviceFile, serviceContent)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			return fmt.Errorf("failed to create etcd service on %s: %w", node.Host, result.Error)
		}

		// 启动etcd服务
		cmd = fmt.Sprintf("systemctl daemon-reload && systemctl enable etcd && systemctl start etcd")

		result = m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			diag := m.collectEtcdDiagnostics(node)
			return fmt.Errorf("failed to start etcd on %s: %w\n%s", node.Host, result.Error, diag)
		}

		m.logger.Info("etcd started",
			logger.Fields{"node": node.Host})
	}

	return nil
}

// generateEtcdServiceFile 生成etcd systemd服务文件
func (m *PatroniManager) generateEtcdServiceFile() string {
	return `[Unit]
Description=Etcd Server
After=network.target

[Service]
Type=notify
User=etcd
Group=etcd
ExecStart=/bin/bash -lc 'exec $(command -v etcd) --config-file=/etc/etcd/etcd.yml'
Restart=on-failure
RestartSec=10s
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`
}

// validateEtcdCluster 验证etcd集群健康
func (m *PatroniManager) validateEtcdCluster(nodes []*config.NodeConfig) error {
	endpoints := make([]string, 0, len(nodes))
	for _, node := range nodes {
		endpoints = append(endpoints, fmt.Sprintf("http://%s:2379", node.Host))
	}
	endpointList := strings.Join(endpoints, ",")

	for _, node := range nodes {
		if err := m.waitForEtcdHealthy(node, endpointList, 30*time.Second); err != nil {
			return err
		}
	}

	m.logger.Info("etcd cluster is healthy", logger.Fields{})
	return nil
}

// updateEtcdEndpoints 更新etcd端点列表
func (m *PatroniManager) updateEtcdEndpoints(nodes []*config.NodeConfig) {
	m.etcdEndpoints = make([]string, 0, len(nodes))
	for _, node := range nodes {
		endpoint := fmt.Sprintf("%s:2379", node.Host)
		m.etcdEndpoints = append(m.etcdEndpoints, endpoint)
	}
}

// GeneratePatroniConfig 生成Patroni配置
func (m *PatroniManager) GeneratePatroniConfig(node *config.NodeConfig) (string, error) {
	if err := m.ensureClusterPasswords(); err != nil {
		return "", err
	}

	restPort := m.getRestPort(node)

	config := fmt.Sprintf(`# Patroni Configuration for %s
name: %s
scope: pg-cluster
namespace: /service/
restapi:
  listen: 0.0.0.0:%d
  connect_address: %s:%d

etcd3:
  hosts: "%s"
  protocol: "http"

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    postgresql:
      use_pg_rewind: true
      parameters:
        wal_level: replica
        hot_standby: "on"
        max_wal_senders: 10
        max_replication_slots: 10
        unix_socket_directories: "%s"
  initdb:
    - encoding: UTF8
    - data-checksums
  pg_hba:
    - host replication replicator 0.0.0.0/0 md5
    - host all all 0.0.0.0/0 md5
  users:
    replicator:
      password: "%s"
      options:
        - replication
    postgres:
      password: "%s"
      options:
        - createrole
        - createdb

postgresql:
  listen: 0.0.0.0:%d
  connect_address: %s:%d
  data_dir: %s
  bin_dir: %s/bin
  authentication:
    superuser:
      username: postgres
      password: "%s"
    replication:
      username: replicator
      password: "%s"
  parameters:
    # Conservative defaults so low-memory hosts can start reliably.
    max_connections: 50
    shared_buffers: 64MB
    wal_level: replica
    hot_standby: on
    max_wal_senders: 10
    max_replication_slots: 10
    wal_log_hints: on
    unix_socket_directories: "%s"

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false
`,
		node.Name,
		node.Name,
		restPort,
		node.Host,
		restPort,
		strings.Join(m.etcdEndpoints, ","),
		node.DataDir,
		m.replicationPassword,
		m.superuserPassword,
		node.Port,
		node.Host,
		node.Port,
		node.DataDir,
		m.config.PGSoftDir,
		m.superuserPassword,
		m.replicationPassword,
		node.DataDir,
	)

	return config, nil
}

// ConfigurePatroniNode 配置Patroni节点
func (m *PatroniManager) ConfigurePatroniNode(node *config.NodeConfig) error {
	m.logger.Info("Configuring Patroni node",
		logger.Fields{
			"node": node.Host,
			"name": node.Name,
		})

	// 生成配置
	config, err := m.GeneratePatroniConfig(node)
	if err != nil {
		return fmt.Errorf("failed to generate config: %w", err)
	}

	// 写入配置文件
	configFile := fmt.Sprintf("/etc/patroni/%s.yml", node.Name)
	cmd := fmt.Sprintf("mkdir -p /etc/patroni && cat > %s << 'EOF'\n%s\nEOF",
		configFile, config)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)

	if result.Error != nil {
		return fmt.Errorf("failed to write config: %w", result.Error)
	}

	m.logger.Info("Patroni configured",
		logger.Fields{"node": node.Host})

	return nil
}

// StartPatroniCluster 启动Patroni集群
func (m *PatroniManager) StartPatroniCluster() error {
	m.logger.Info("Starting Patroni cluster",
		logger.Fields{"nodes": len(m.nodes)})

	for _, node := range m.nodes {
		if err := m.preparePatroniNodeStart(node); err != nil {
			return fmt.Errorf("failed to prepare Patroni node %s: %w", node.Host, err)
		}
	}

	for _, node := range m.nodes {
		serviceContent := m.generatePatroniServiceFile(node)

		serviceFile := fmt.Sprintf("/etc/systemd/system/patroni-%s.service", node.Name)
		cmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", serviceFile, serviceContent)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			return fmt.Errorf("failed to create service on %s: %w", node.Host, result.Error)
		}
	}

	var startFailures []string
	for _, node := range m.nodes {
		cmd := fmt.Sprintf("systemctl daemon-reload && systemctl enable patroni-%s && systemctl start patroni-%s",
			node.Name, node.Name)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			diag := m.collectPatroniDiagnostics(node)
			startFailures = append(startFailures, fmt.Sprintf("node=%s error=%v\n%s", node.Host, result.Error, diag))
			continue
		}

		m.logger.Info("Patroni started",
			logger.Fields{"node": node.Host})
	}

	if len(startFailures) > 0 {
		return fmt.Errorf("failed to start Patroni on %d node(s):\n%s", len(startFailures), strings.Join(startFailures, "\n\n"))
	}

	// 验证集群状态
	if err := m.waitForPatroniCluster(45 * time.Second); err != nil {
		return fmt.Errorf("cluster validation failed: %w", err)
	}

	return nil
}

func (m *PatroniManager) preparePatroniNodeStart(node *config.NodeConfig) error {
	commands := []string{
		fmt.Sprintf("mkdir -p %s", shellValue(node.DataDir)),
		fmt.Sprintf("chown postgres:postgres %s", shellValue(node.DataDir)),
		fmt.Sprintf("chmod 0700 %s", shellValue(node.DataDir)),
	}

	if node.WALDir != "" {
		commands = append(commands,
			fmt.Sprintf("mkdir -p %s", shellValue(node.WALDir)),
			fmt.Sprintf("chown postgres:postgres %s", shellValue(node.WALDir)),
			fmt.Sprintf("chmod 0700 %s", shellValue(node.WALDir)),
		)
	}

	if node.PGLogDir != "" {
		commands = append(commands,
			fmt.Sprintf("mkdir -p %s", shellValue(node.PGLogDir)),
			fmt.Sprintf("chown postgres:postgres %s", shellValue(node.PGLogDir)),
			fmt.Sprintf("chmod 0755 %s", shellValue(node.PGLogDir)),
		)
	}

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, strings.Join(commands, " && "), true, false)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func (m *PatroniManager) collectPatroniDiagnostics(node *config.NodeConfig) string {
	serviceName := fmt.Sprintf("patroni-%s", node.Name)
	configFile := fmt.Sprintf("/etc/patroni/%s.yml", node.Name)
	serviceFile := fmt.Sprintf("/etc/systemd/system/%s.service", serviceName)
	cmd := fmt.Sprintf(`
echo '=== systemctl status %s ==='
systemctl status %s --no-pager -l 2>&1 || true
echo ''
echo '=== journalctl -u %s ==='
journalctl -u %s -n 80 --no-pager 2>&1 || true
echo ''
echo '=== %s ==='
cat %s 2>&1 || true
echo ''
echo '=== data directory permissions ==='
namei -l %s 2>&1 || true
echo ''
echo '=== %s ==='
cat %s 2>&1 || true
echo ''
echo '=== command -v patroni ==='
command -v patroni 2>&1 || true
echo ''
echo '=== patroni version ==='
patroni --version 2>&1 || true
`, serviceName, serviceName, serviceName, serviceName, configFile, configFile, node.DataDir, serviceFile, serviceFile)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, true)
	return strings.TrimSpace(result.Output)
}

func shellValue(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'"
}

// generatePatroniServiceFile 生成Patroni systemd服务文件
func (m *PatroniManager) generatePatroniServiceFile(node *config.NodeConfig) string {
	return fmt.Sprintf(`[Unit]
Description=Patroni instance for %s
After=network.target etcd.service

[Service]
Type=simple
User=postgres
Group=postgres
Environment=PATH=/opt/pg-deploy/patroni-runtime/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/bin/bash -lc 'if [ -x /opt/pg-deploy/patroni-runtime/bin/patroni ]; then exec /opt/pg-deploy/patroni-runtime/bin/patroni /etc/patroni/%s.yml; elif [ -x /usr/local/bin/patroni ]; then exec /usr/local/bin/patroni /etc/patroni/%s.yml; else exec patroni /etc/patroni/%s.yml; fi'
KillMode=process
Restart=on-failure
RestartSec=10s
TimeoutSec=0

[Install]
WantedBy=multi-user.target
`, node.Name, node.Name, node.Name, node.Name)
}

// validatePatroniCluster 验证Patroni集群状态
func (m *PatroniManager) validatePatroniCluster() error {
	// 查询集群成员
	memberList, err := m.GetClusterMembers()
	if err != nil {
		return err
	}

	m.logger.Info("Patroni cluster members", logger.Fields{
		"total_members": len(memberList.Members),
	})

	hasLeader := false
	// 验证每个成员
	for _, member := range memberList.Members {
		m.logger.Debug("Patroni member status", logger.Fields{
			"name":  member.Name,
			"host":  member.Host,
			"role":  member.Role,
			"state": member.State,
		})
		if isLeaderRole(member.Role) {
			hasLeader = true
			m.logger.Info("Cluster leader",
				logger.Fields{
					"name": member.Name,
					"host": member.Host,
				})
		}
	}

	if !hasLeader {
		return fmt.Errorf("no leader elected in Patroni cluster")
	}

	return nil
}

func (m *PatroniManager) waitForEtcdHealthy(node *config.NodeConfig, endpointList string, timeout time.Duration) error {
	m.logger.Info("Waiting for etcd health",
		logger.Fields{
			"node":      node.Host,
			"endpoints": endpointList,
			"timeout":   timeout.String(),
		})

	cmd := fmt.Sprintf("ETCDCTL_API=3 $(command -v etcdctl) endpoint health --cluster --endpoints=%s", endpointList)
	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastOutput string

	for time.Now().Before(deadline) {
		attempt++
		result := m.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: m.config.SSHUser,
		}, cmd, false, true)

		if result.Error == nil && strings.Contains(result.Output, "is healthy") {
			m.logger.Info("etcd is healthy",
				logger.Fields{
					"node":     node.Host,
					"attempts": attempt,
				})
			return nil
		}

		lastOutput = strings.TrimSpace(result.Output)
		if attempt == 1 || attempt%5 == 0 {
			m.logger.Debug("etcd still starting",
				logger.Fields{
					"node":     node.Host,
					"attempts": attempt,
					"output":   lastOutput,
				})
		}
		time.Sleep(1 * time.Second)
	}

	diag := m.collectEtcdDiagnostics(node)
	return fmt.Errorf("etcd not healthy on %s after %s: %s\n%s", node.Host, timeout, lastOutput, diag)
}

func (m *PatroniManager) resetSingleNodeEtcd(node *config.NodeConfig) error {
	m.logger.Info("Resetting single-node etcd environment",
		logger.Fields{"node": node.Host})

	cmd := `
set -e
systemctl stop etcd 2>/dev/null || true
systemctl disable etcd 2>/dev/null || true
pkill -9 etcd 2>/dev/null || true
rm -rf /var/lib/etcd/*
rm -f /etc/etcd/etcd.yml
rm -f /etc/systemd/system/etcd.service
systemctl daemon-reload || true
`
	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func (m *PatroniManager) collectEtcdDiagnostics(node *config.NodeConfig) string {
	cmd := `
echo '=== systemctl status etcd ==='
systemctl status etcd --no-pager -l 2>&1 || true
echo ''
echo '=== journalctl -u etcd ==='
journalctl -u etcd -n 80 --no-pager 2>&1 || true
echo ''
echo '=== /etc/etcd/etcd.yml ==='
cat /etc/etcd/etcd.yml 2>&1 || true
echo ''
echo '=== ss -lntp (2379/2380) ==='
ss -lntp 2>/dev/null | grep -E '2379|2380' || true
`
	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, true)
	return strings.TrimSpace(result.Output)
}

func (m *PatroniManager) waitForPatroniCluster(timeout time.Duration) error {
	m.logger.Info("Waiting for Patroni cluster election",
		logger.Fields{
			"nodes":   len(m.nodes),
			"timeout": timeout.String(),
		})

	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastErr error

	for time.Now().Before(deadline) {
		attempt++
		err := m.validatePatroniCluster()
		if err == nil {
			m.logger.Info("Patroni cluster is ready",
				logger.Fields{"attempts": attempt})
			return nil
		}

		lastErr = err
		if attempt == 1 || attempt%5 == 0 {
			m.logger.Debug("Patroni cluster still converging",
				logger.Fields{
					"attempts": attempt,
					"error":    err,
				})
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("Patroni cluster not ready after %s: %w", timeout, lastErr)
}

// GetClusterMembers 获取集群成员列表
func (m *PatroniManager) GetClusterMembers() (*PatroniClusterMembers, error) {
	if len(m.nodes) == 0 {
		return nil, fmt.Errorf("no nodes available")
	}

	members := &PatroniClusterMembers{
		Members: make([]ClusterMember, 0),
	}

	var lastErr error
	for _, node := range m.nodes {
		member, err := m.getPatroniMemberFromAPI(node)
		if err != nil {
			lastErr = err
			continue
		}
		members.Members = append(members.Members, *member)
	}

	if len(members.Members) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("failed to get cluster members from Patroni API: %w", lastErr)
		}
		return nil, fmt.Errorf("failed to get cluster members from Patroni API")
	}

	return members, nil
}

func (m *PatroniManager) getPatroniMemberFromAPI(node *config.NodeConfig) (*ClusterMember, error) {
	restPort := m.getRestPort(node)
	cmd := fmt.Sprintf("curl -sf http://127.0.0.1:%d/patroni", restPort)
	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, false, true)
	if result.Error != nil {
		return nil, result.Error
	}

	var payload struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Output)), &payload); err != nil {
		return nil, fmt.Errorf("invalid patroni api response on %s: %w", node.Host, err)
	}

	name := payload.Name
	if name == "" {
		name = node.Name
	}

	return &ClusterMember{
		Name:  name,
		Host:  node.Host,
		Role:  strings.ToLower(strings.TrimSpace(payload.Role)),
		State: strings.ToLower(strings.TrimSpace(payload.State)),
	}, nil
}

// PatroniClusterMembers Patroni集群成员
type PatroniClusterMembers struct {
	Members []ClusterMember
}

// ClusterMember 集群成员
type ClusterMember struct {
	Name  string
	Host  string
	Role  string
	State string
}

// PerformFailover 执行手动故障转移
func (m *PatroniManager) PerformFailover(fromNode, toNode string) error {
	m.logger.Info("Performing manual failover",
		logger.Fields{
			"from": fromNode,
			"to":   toNode,
		})

	// 查找对应节点
	var fromConfig, toConfig *config.NodeConfig
	for _, node := range m.nodes {
		if node.Name == fromNode {
			fromConfig = node
		}
		if node.Name == toNode {
			toConfig = node
		}
	}

	if fromConfig == nil || toConfig == nil {
		return fmt.Errorf("node not found")
	}

	// 执行故障转移
	cmd := fmt.Sprintf("patronictl -c /etc/patroni/%s.yml failover --master %s --candidate %s --force",
		fromConfig.Name, fromConfig.Name, toConfig.Name)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   fromConfig.Host,
		Host: fromConfig.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)

	if result.Error != nil {
		return fmt.Errorf("failover failed: %w", result.Error)
	}

	m.logger.Info("Failover completed successfully", logger.Fields{})
	return nil
}

// GetClusterHealth 获取集群健康状态
func (m *PatroniManager) GetClusterHealth() (*PatroniHealth, error) {
	members, err := m.GetClusterMembers()
	if err != nil {
		return nil, err
	}

	health := &PatroniHealth{
		TotalMembers:   len(members.Members),
		HealthyMembers: 0,
		Leader:         "",
	}

	for _, member := range members.Members {
		if member.State == "running" || member.State == "streaming" {
			health.HealthyMembers++
		}
		if isLeaderRole(member.Role) {
			health.Leader = member.Name
		}
	}

	health.Healthy = health.HealthyMembers == health.TotalMembers

	return health, nil
}

func isLeaderRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "leader", "primary", "master", "standby_leader":
		return true
	default:
		return false
	}
}

// PatroniHealth Patroni集群健康状态
type PatroniHealth struct {
	TotalMembers   int
	HealthyMembers int
	Healthy        bool
	Leader         string
}

// WatchClusterState 监控集群状态变化
func (m *PatroniManager) WatchClusterState(callback func(*PatroniClusterMembers)) error {
	// 每隔5秒查询一次集群状态
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		members, err := m.GetClusterMembers()
		if err != nil {
			m.logger.Warn("Failed to get cluster state",
				logger.Fields{"error": err})
			continue
		}

		callback(members)
	}

	return nil
}

// RestartNode 重启Patroni节点
func (m *PatroniManager) RestartNode(node *config.NodeConfig, schedule string) error {
	m.logger.Info("Restarting Patroni node",
		logger.Fields{
			"node":     node.Host,
			"schedule": schedule,
		})

	cmd := fmt.Sprintf("patronictl -c /etc/patroni/%s.yml restart %s", node.Name, node.Name)
	if schedule != "" {
		cmd += fmt.Sprintf(" --schedule %s", schedule)
	}

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)

	if result.Error != nil {
		return fmt.Errorf("restart failed: %w", result.Error)
	}

	m.logger.Info("Node restart scheduled", logger.Fields{"node": node.Host})

	return nil
}

func (m *PatroniManager) ensureClusterPasswords() error {
	if m.replicationPassword == "" {
		password, err := generateRandomPassword(16)
		if err != nil {
			return fmt.Errorf("failed to generate replication password: %w", err)
		}
		m.replicationPassword = password
	}

	if m.superuserPassword == "" {
		password, err := generateRandomPassword(16)
		if err != nil {
			return fmt.Errorf("failed to generate superuser password: %w", err)
		}
		m.superuserPassword = password
	}

	return nil
}

func (m *PatroniManager) getRestPort(node *config.NodeConfig) int {
	if port, ok := m.restPorts[node.Name]; ok {
		return port
	}

	port := node.Port + 1000
	m.restPorts[node.Name] = port
	return port
}

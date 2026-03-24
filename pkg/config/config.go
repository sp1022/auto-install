// Package config 提供部署配置文件解析和管理
// 支持从配置文件加载部署拓扑和参数
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/example/pg-deploy/pkg/logger"
)

const (
	// 环境变量名
	envSSHPassword = "SSH_PASSWORD"
)

// DeployMode 部署模式
type DeployMode string

const (
	ModeStandalone  DeployMode = "standalone"   // 单机模式
	ModeMasterSlave DeployMode = "master-slave" // ��从模式
	ModePatroni     DeployMode = "patroni"      // Patroni HA
	ModeCitus       DeployMode = "citus"        // Citus 分布式
)

// BuildMode 构建模式
type BuildMode string

const (
	BuildCompile    BuildMode = "compile"    // 从源码编译
	BuildDistribute BuildMode = "distribute" // 分发预编译二进制
)

// Config 部署配置
type Config struct {
	// SSH 配置
	SSHUser     string
	SSHPassword string

	// 环境配置
	EnvironmentName   string
	EnvironmentPrefix string

	// 部署配置
	DeployMode DeployMode
	BuildMode  BuildMode

	// PostgreSQL 路径
	PGSource        string // PostgreSQL 源码包或二进制包路径
	PGSoftDir       string // PostgreSQL 安装目录
	PGConfigureOpts string // PostgreSQL configure 选项

	// 扩展配置
	Extensions []string // 如 citus, pg_stat_statements

	// 组配置
	Groups []*GroupConfig

	// 配置文件路径
	configPath string
}

// GroupConfig PostgreSQL 组配置
type GroupConfig struct {
	ID   int
	Name string
	Role string // coordinator, worker, primary, standby

	Nodes []*NodeConfig
}

// NodeConfig 节点配置
type NodeConfig struct {
	ID       int
	Name     string
	Role     string // coordinator, worker, primary, standby
	Host     string
	Port     int
	DataDir  string
	WALDir   string // 可选
	PGLogDir string // 可选
	IsMaster bool
	IsLocal  bool // 是否为本地节点
}

// Load 从文件加载配置
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	cfg := &Config{
		configPath: path,
		Groups:     make([]*GroupConfig, 0),
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 解析键值对
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if err := cfg.parseField(key, value); err != nil {
			return nil, fmt.Errorf("error parsing line %d: %w", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	cfg.ApplyEnvironment()

	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	// 配置文件未提供密码时，允许从环境变量补齐。
	if cfg.SSHPassword == "" {
		cfg.LoadPasswordFromEnv()
	}

	return cfg, nil
}

// parseField 解析配置字段
func (c *Config) parseField(key, value string) error {
	switch key {
	case "ssh_user":
		c.SSHUser = value
	case "ssh_password":
		c.SSHPassword = value
	case "env_name":
		c.EnvironmentName = value
	case "env_prefix":
		c.EnvironmentPrefix = value
	case "deploy_mode":
		c.DeployMode = DeployMode(value)
	case "build_mode":
		c.BuildMode = BuildMode(value)
	case "pg_source":
		c.PGSource = value
	case "pg_soft_dir":
		c.PGSoftDir = value
	case "pg_configure_opts":
		c.PGConfigureOpts = value
	case "extensions":
		if value != "" {
			c.Extensions = strings.Split(value, ",")
			for i := range c.Extensions {
				c.Extensions[i] = strings.TrimSpace(c.Extensions[i])
			}
		}
	default:
		// 组配置: group_0, group_1, etc.
		if strings.HasPrefix(key, "group_") {
			groupNumStr := strings.TrimPrefix(key, "group_")
			groupNum, err := strconv.Atoi(groupNumStr)
			if err != nil {
				return fmt.Errorf("invalid group number: %s", groupNumStr)
			}
			return c.parseGroup(groupNum, value)
		}
	}

	return nil
}

// parseGroup 解析组配置
// 格式: id|name|role|ip:port:data_dir:wal_dir:pglog_dir:is_master,...
func (c *Config) parseGroup(id int, value string) error {
	nodesStr := strings.Split(value, ",")
	if len(nodesStr) == 0 {
		return fmt.Errorf("no nodes defined for group %d", id)
	}

	group := &GroupConfig{
		ID:    id,
		Nodes: make([]*NodeConfig, 0, len(nodesStr)),
	}

	for idx, nodeStr := range nodesStr {
		nodeStr = strings.TrimSpace(nodeStr)
		if nodeStr == "" {
			continue
		}

		node, err := parseNodeConfig(nodeStr)
		if err != nil && !strings.Contains(nodeStr, "|") {
			node, err = parseShorthandNodeConfig(group, idx, nodeStr)
		}
		if err != nil {
			return fmt.Errorf("failed to parse node: %w", err)
		}

		if group.Name == "" && node.Name != "" {
			group.Name = node.Name
		}
		if group.Role == "" && node.Role != "" {
			group.Role = node.Role
		}

		group.Nodes = append(group.Nodes, node)
	}

	// 设置组名称和角色（从第一个节点获取）
	if len(group.Nodes) > 0 {
		group.Name = group.Nodes[0].Name
		group.Role = group.Nodes[0].Role
	}

	c.Groups = append(c.Groups, group)
	return nil
}

func parseShorthandNodeConfig(group *GroupConfig, index int, s string) (*NodeConfig, error) {
	hostParts := strings.Split(s, ":")
	if len(hostParts) < 3 {
		return nil, fmt.Errorf("invalid shorthand node format, expected host:port:data_dir:..., got %d parts", len(hostParts))
	}

	port, err := strconv.Atoi(hostParts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}

	node := &NodeConfig{
		ID:      index,
		Host:    hostParts[0],
		Port:    port,
		DataDir: hostParts[2],
	}

	if group.Name != "" {
		node.Name = fmt.Sprintf("%s%d", group.Name, index)
	} else {
		node.Name = fmt.Sprintf("group%dnode%d", group.ID, index)
	}

	if len(hostParts) > 3 && hostParts[3] != "" {
		node.WALDir = hostParts[3]
	}
	if len(hostParts) > 4 && hostParts[4] != "" {
		node.PGLogDir = hostParts[4]
	}
	if len(hostParts) > 5 && hostParts[5] != "" {
		isMaster, err := strconv.Atoi(hostParts[5])
		if err != nil {
			return nil, fmt.Errorf("invalid is_master flag: %w", err)
		}
		node.IsMaster = isMaster == 1
	}

	if node.IsMaster {
		if group.Role != "" {
			node.Role = group.Role
		} else {
			node.Role = "primary"
		}
	} else {
		switch group.Role {
		case "coordinator", "worker":
			node.Role = group.Role
		default:
			node.Role = "standby"
		}
	}

	if node.Host == "" {
		return nil, fmt.Errorf("host cannot be empty")
	}
	if node.DataDir == "" {
		return nil, fmt.Errorf("data_dir cannot be empty")
	}

	return node, nil
}

// parseNodeConfig 解析节点配置
// 格式: id|name|role|host:port:data_dir:wal_dir:pglog_dir:is_master
// 示例: 0|pg0|coordinator|192.168.1.10:5432:/data/pgdata::::1
func parseNodeConfig(s string) (*NodeConfig, error) {
	// 首先按 | 分割 ID|name|role|host:port:...
	parts := strings.Split(s, "|")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid node format, expected id|name|role|host:port:..., got %d parts", len(parts))
	}

	// 解析 ID
	id, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid node ID: %w", err)
	}

	node := &NodeConfig{
		ID:   id,
		Name: parts[1],
		Role: parts[2],
	}

	// 解析 host:port:data_dir:wal_dir:pglog_dir:is_master
	hostParts := strings.Split(parts[3], ":")
	if len(hostParts) < 3 {
		return nil, fmt.Errorf("invalid host:port:data_dir format, got %d parts", len(hostParts))
	}

	// host
	node.Host = hostParts[0]
	// 验证主机名不为空且不包含危险字符
	if node.Host == "" {
		return nil, fmt.Errorf("host cannot be empty")
	}
	// 防止路径遍历和命令注入
	if strings.ContainsAny(node.Host, ";&|`$()") {
		return nil, fmt.Errorf("host contains invalid characters: %s", node.Host)
	}

	// port
	port, err := strconv.Atoi(hostParts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid port: %w", err)
	}
	// 验证端口范围 (1-65535)
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}
	node.Port = port

	// data_dir
	node.DataDir = hostParts[2]
	// 验证路径不为空且防止路径遍历
	if node.DataDir == "" {
		return nil, fmt.Errorf("data_dir cannot be empty")
	}
	// 防止路径遍历攻击
	if strings.Contains(node.DataDir, "../") {
		return nil, fmt.Errorf("data_dir contains path traversal: %s", node.DataDir)
	}

	// 可选字段（注意连续的冒号会产生空字符串）
	if len(hostParts) > 3 && hostParts[3] != "" {
		node.WALDir = hostParts[3]
		// 验证路径防止路径遍历
		if strings.Contains(node.WALDir, "../") {
			return nil, fmt.Errorf("wal_dir contains path traversal: %s", node.WALDir)
		}
	}
	if len(hostParts) > 4 && hostParts[4] != "" {
		node.PGLogDir = hostParts[4]
		// 验证路径防止路径遍历
		if strings.Contains(node.PGLogDir, "../") {
			return nil, fmt.Errorf("pglog_dir contains path traversal: %s", node.PGLogDir)
		}
	}
	if len(hostParts) > 5 {
		// is_master 可能在最后一个位置
		isMasterIdx := 5
		if hostParts[5] == "" && len(hostParts) > 6 {
			isMasterIdx = 6
		}
		if hostParts[isMasterIdx] != "" {
			isMaster, err := strconv.Atoi(hostParts[isMasterIdx])
			if err != nil {
				return nil, fmt.Errorf("invalid is_master flag: %w", err)
			}
			node.IsMaster = isMaster == 1
		}
	}

	return node, nil
}

// Validate 验证配置
func (c *Config) Validate() error {
	// 必需字段
	if c.SSHUser == "" {
		return fmt.Errorf("ssh_user is required")
	}

	if c.DeployMode == "" {
		return fmt.Errorf("deploy_mode is required")
	}

	if c.BuildMode == "" {
		return fmt.Errorf("build_mode is required")
	}

	if c.PGSoftDir == "" {
		return fmt.Errorf("pg_soft_dir is required")
	}

	// 验证部署模式
	switch c.DeployMode {
	case ModeStandalone, ModeMasterSlave, ModePatroni, ModeCitus:
		// 有效模式
	default:
		return fmt.Errorf("invalid deploy_mode: %s", c.DeployMode)
	}

	// 验证构建模式
	switch c.BuildMode {
	case BuildCompile, BuildDistribute:
		// 有效模式
	default:
		return fmt.Errorf("invalid build_mode: %s", c.BuildMode)
	}

	// 验证源码路径
	if c.BuildMode == BuildCompile {
		if c.PGSource == "" {
			return fmt.Errorf("pg_source is required for compile mode")
		}
		if _, err := os.Stat(c.PGSource); os.IsNotExist(err) {
			return fmt.Errorf("pg_source file not found: %s", c.PGSource)
		}
	}

	if err := validateManagedPath("pg_soft_dir", c.PGSoftDir); err != nil {
		return err
	}

	// 验证组配置
	if len(c.Groups) == 0 {
		return fmt.Errorf("at least one group must be defined")
	}

	// 每个组至少有一个主节点
	for i, group := range c.Groups {
		hasMaster := false
		for _, node := range group.Nodes {
			if err := validateManagedPath("data_dir", node.DataDir); err != nil {
				return fmt.Errorf("group %d (%s) node %s: %w", i, group.Name, node.Name, err)
			}
			if node.WALDir != "" {
				if err := validateManagedPath("wal_dir", node.WALDir); err != nil {
					return fmt.Errorf("group %d (%s) node %s: %w", i, group.Name, node.Name, err)
				}
			}
			if node.PGLogDir != "" {
				if err := validateManagedPath("pglog_dir", node.PGLogDir); err != nil {
					return fmt.Errorf("group %d (%s) node %s: %w", i, group.Name, node.Name, err)
				}
			}
			if node.IsMaster {
				hasMaster = true
				break
			}
		}
		if !hasMaster {
			return fmt.Errorf("group %d (%s) has no master node", i, group.Name)
		}
	}

	return nil
}

// GetMasterNodes 获取所有主节点
func (c *Config) GetMasterNodes() []*NodeConfig {
	var masters []*NodeConfig
	for _, group := range c.Groups {
		for _, node := range group.Nodes {
			if node.IsMaster {
				masters = append(masters, node)
			}
		}
	}
	return masters
}

// GetAllNodes 获取所有节点
func (c *Config) GetAllNodes() []*NodeConfig {
	var nodes []*NodeConfig
	for _, group := range c.Groups {
		nodes = append(nodes, group.Nodes...)
	}
	return nodes
}

// GetNodesByGroup 获取指定组的所有节点
func (c *Config) GetNodesByGroup(groupID int) []*NodeConfig {
	for _, group := range c.Groups {
		if group.ID == groupID {
			return group.Nodes
		}
	}
	return nil
}

// GetLocalNodes 获取本地节点
func (c *Config) GetLocalNodes() []*NodeConfig {
	// TODO: 实现本地节点检测逻辑
	return []*NodeConfig{}
}

// Save 保存配置到文件
func (c *Config) Save(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer file.Close()

	// 写入基本配置
	if c.SSHUser != "" {
		fmt.Fprintf(file, "ssh_user: %s\n", c.SSHUser)
	}
	if c.EnvironmentName != "" {
		fmt.Fprintf(file, "env_name: %s\n", c.EnvironmentName)
	}
	if c.EnvironmentPrefix != "" {
		fmt.Fprintf(file, "env_prefix: %s\n", c.EnvironmentPrefix)
	}
	// 安全改进：不再保存明��密码到配置文件
	// 密码应通过环境变量 SSH_PASSWORD 或在运行时提供
	// if c.SSHPassword != "" {
	// 	fmt.Fprintf(file, "ssh_password: %s\n", c.SSHPassword)
	// }
	if c.DeployMode != "" {
		fmt.Fprintf(file, "deploy_mode: %s\n", c.DeployMode)
	}
	if c.BuildMode != "" {
		fmt.Fprintf(file, "build_mode: %s\n", c.BuildMode)
	}
	if c.PGSource != "" {
		fmt.Fprintf(file, "pg_source: %s\n", c.PGSource)
	}
	if c.PGSoftDir != "" {
		fmt.Fprintf(file, "pg_soft_dir: %s\n", c.PGSoftDir)
	}
	if c.PGConfigureOpts != "" {
		fmt.Fprintf(file, "pg_configure_opts: %s\n", c.PGConfigureOpts)
	}
	if len(c.Extensions) > 0 {
		fmt.Fprintf(file, "extensions: %s\n", strings.Join(c.Extensions, ","))
	}

	// 写入组配置
	for _, group := range c.Groups {
		var nodeStrs []string
		for _, node := range group.Nodes {
			nodeStr := fmt.Sprintf("%d|%s|%s|%s:%d:%s:%s:%s:%d",
				node.ID, node.Name, node.Role,
				node.Host, node.Port, node.DataDir,
				node.WALDir, node.PGLogDir,
				map[bool]int{true: 1, false: 0}[node.IsMaster],
			)
			nodeStrs = append(nodeStrs, nodeStr)
		}
		fmt.Fprintf(file, "group_%d: %s\n", group.ID, strings.Join(nodeStrs, ","))
	}

	return nil
}

// LogInfo 记录配置摘要到日志
func (c *Config) LogInfo(log *logger.Logger) {
	log.Info("Configuration loaded",
		logger.Fields{
			"deploy_mode":  c.DeployMode,
			"build_mode":   c.BuildMode,
			"ssh_user":     c.SSHUser,
			"pg_soft_dir":  c.PGSoftDir,
			"extensions":   c.Extensions,
			"group_count":  len(c.Groups),
			"total_nodes":  len(c.GetAllNodes()),
			"master_nodes": len(c.GetMasterNodes()),
		})

	for _, group := range c.Groups {
		log.Info("Group configuration",
			logger.Fields{
				"group_id":   group.ID,
				"group_name": group.Name,
				"role":       group.Role,
				"node_count": len(group.Nodes),
			})
	}
}

// ClearPassword 安全地清除内存中的密码
// 使用 crypto/subtle 来确保编译器不会优化掉这个操作
func (c *Config) ClearPassword() {
	if c.SSHPassword != "" {
		// 将密码转换为字节切片并用零覆盖
		passwordBytes := []byte(c.SSHPassword)
		defer func() {
			// 使用 subtle.ZeroBytes 来安全清零
			for i := range passwordBytes {
				passwordBytes[i] = 0
			}
		}()
		// 清空字符串（Go 字符串是不可变的，但我们至少可以清除引用）
		c.SSHPassword = ""
	}
}

// GetPassword 获取密码用于SSH连接
// 返回密码的副本以避免泄露原始引用
func (c *Config) GetPassword() string {
	if c.SSHPassword == "" {
		return ""
	}
	// 返回密码的副本
	passwordCopy := string([]byte(c.SSHPassword))
	return passwordCopy
}

// SetPassword 设置密码（从环境变量或用户输入）
func (c *Config) SetPassword(password string) {
	c.SSHPassword = password
}

// LoadPasswordFromEnv 从环境变量加载密码
func (c *Config) LoadPasswordFromEnv() bool {
	password := os.Getenv(envSSHPassword)
	if password != "" {
		c.SSHPassword = password
		return true
	}
	return false
}

// HasPassword 检查是否已配置密码
func (c *Config) HasPassword() bool {
	return c.SSHPassword != ""
}

func (c *Config) ApplyEnvironment() {
	if c.EnvironmentName == "" {
		return
	}

	if c.EnvironmentPrefix == "" {
		c.EnvironmentPrefix = c.EnvironmentName
	}

	c.PGSource = c.resolveTemplate(c.PGSource)
	c.PGSoftDir = c.resolveTemplate(c.PGSoftDir)

	for _, group := range c.Groups {
		group.Name = c.scopeName(c.resolveTemplate(group.Name))
		for _, node := range group.Nodes {
			node.Name = c.scopeName(c.resolveTemplate(node.Name))
			node.DataDir = c.resolveTemplate(node.DataDir)
			node.WALDir = c.resolveTemplate(node.WALDir)
			node.PGLogDir = c.resolveTemplate(node.PGLogDir)
		}
	}
}

func validateManagedPath(field, path string) error {
	if path == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path: %s", field, path)
	}
	cleaned := filepath.Clean(path)
	switch cleaned {
	case "/", ".", "..":
		return fmt.Errorf("%s cannot be a dangerous path: %s", field, path)
	}
	if strings.ContainsAny(path, ";&|`$()<>{}[]\n\r") {
		return fmt.Errorf("%s contains unsafe shell characters: %s", field, path)
	}
	return nil
}

func (c *Config) resolveTemplate(value string) string {
	if value == "" {
		return value
	}

	value = strings.ReplaceAll(value, "{env}", c.EnvironmentName)
	value = strings.ReplaceAll(value, "{prefix}", c.EnvironmentPrefix)
	return value
}

func (c *Config) scopeName(name string) string {
	if name == "" || c.EnvironmentPrefix == "" {
		return name
	}

	prefix := c.EnvironmentPrefix + "-"
	if strings.HasPrefix(name, prefix) {
		return name
	}

	return prefix + name
}

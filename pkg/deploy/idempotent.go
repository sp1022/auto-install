// Package deploy 提供幂等性检查器
// 确保部署操作可以安全地重复执行而不产生副作用
package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// IdempotentChecker 幂等性检查器
type IdempotentChecker struct {
	executor *executor.Executor
	config   *config.Config
	logger   *logger.Logger
}

// NewIdempotentChecker 创建幂等性检查器
func NewIdempotentChecker(exec *executor.Executor, cfg *config.Config, log *logger.Logger) *IdempotentChecker {
	return &IdempotentChecker{
		executor: exec,
		config:   cfg,
		logger:   log,
	}
}

// CheckDirectories 检查目录是否已创建
func (c *IdempotentChecker) CheckDirectories() (bool, error) {
	nodes := c.config.GetAllNodes()

	for _, node := range nodes {
		// 检查数据目录
		if node.DataDir != "" {
			if !c.directoryExists(node, node.DataDir) {
				return false, nil
			}
		}

		// 检查 WAL 目录
		if node.WALDir != "" {
			if !c.directoryExists(node, node.WALDir) {
				return false, nil
			}
		}

		// 检查日志目录
		if node.PGLogDir != "" {
			if !c.directoryExists(node, node.PGLogDir) {
				return false, nil
			}
		}
	}

	return true, nil
}

// directoryExists 检查目录是否存在
func (c *IdempotentChecker) directoryExists(node *config.NodeConfig, dir string) bool {
	cmd := fmt.Sprintf("test -d %s", dir)
	result := c.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: c.config.SSHUser,
	}, cmd, false, false)

	return result.Error == nil
}

// CheckUser 检查 postgres 用户是否存在
func (c *IdempotentChecker) CheckUser() (bool, error) {
	cmd := "id postgres >/dev/null 2>&1 && echo 'exists' || echo 'not_exists'"

	nodes := c.config.GetAllNodes()
	for _, node := range nodes {
		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil || !strings.Contains(result.Output, "exists") {
			return false, nil
		}
	}

	return true, nil
}

// CheckSoftwareInstallation 检查软件是否已安装
func (c *IdempotentChecker) CheckSoftwareInstallation() (bool, error) {
	nodes := c.config.GetAllNodes()

	for _, node := range nodes {
		// 检查 pg_config
		pgConfigPath := filepath.Join(c.config.PGSoftDir, "bin/pg_config")
		cmd := fmt.Sprintf("test -x %s", pgConfigPath)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckDatabaseInit 检查数据库是否已初始化
func (c *IdempotentChecker) CheckDatabaseInit() (bool, error) {
	masterNodes := c.config.GetMasterNodes()

	for _, node := range masterNodes {
		// 检查 PG_VERSION 文件
		cmd := fmt.Sprintf("test -f %s/PG_VERSION", node.DataDir)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckPostgreSQLRunning 检查 PostgreSQL 是否运行
func (c *IdempotentChecker) CheckPostgreSQLRunning() (bool, error) {
	nodes := c.config.GetAllNodes()

	for _, node := range nodes {
		pgIsready := filepath.Join(c.config.PGSoftDir, "bin/pg_isready")
		cmd := fmt.Sprintf("%s -h localhost -p %d", pgIsready, node.Port)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckReplication 检查复制是否已配置
func (c *IdempotentChecker) CheckReplication() (bool, error) {
	if c.config.DeployMode != config.ModeMasterSlave {
		return true, nil // 非主从模式，跳过检查
	}

	slaveNodes := c.getSlaveNodes()
	if len(slaveNodes) == 0 {
		return true, nil
	}

	for _, node := range slaveNodes {
		// 检查 standby.signal 文件
		cmd := fmt.Sprintf("test -f %s/standby.signal", node.DataDir)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckPatroniInstalled 检查 Patroni 是否已安装
func (c *IdempotentChecker) CheckPatroniInstalled() (bool, error) {
	if c.config.DeployMode != config.ModePatroni {
		return true, nil
	}

	cmd := "command -v patroni >/dev/null 2>&1 && command -v patronictl >/dev/null 2>&1"

	nodes := c.config.GetAllNodes()
	for _, node := range nodes {
		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckPatroniConfigured 检查 Patroni 是否已配置
func (c *IdempotentChecker) CheckPatroniConfigured() (bool, error) {
	if c.config.DeployMode != config.ModePatroni {
		return true, nil
	}

	// 检查 patroni 配置文件
	nodes := c.config.GetAllNodes()
	for _, node := range nodes {
		configFile := fmt.Sprintf("/etc/patroni/%s.yml", node.Name)
		cmd := fmt.Sprintf("test -f %s", configFile)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckSystemdService 检查 systemd 服务是否已创建
func (c *IdempotentChecker) CheckSystemdService() (bool, error) {
	nodes := c.config.GetAllNodes()

	for _, node := range nodes {
		var serviceName string
		switch c.config.DeployMode {
		case config.ModePatroni:
			serviceName = fmt.Sprintf("patroni-%s", node.Name)
		default:
			serviceName = fmt.Sprintf("postgresql-%s", node.Name)
		}

		cmd := fmt.Sprintf("systemctl list-unit-files | grep -q '%s.service'", serviceName)

		result := c.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: c.config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// CheckLocalFile 检查本地文件是否存在
func (c *IdempotentChecker) CheckLocalFile(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}
	return true
}

// GetDeploymentState 获取当前部署状态
func (c *IdempotentChecker) GetDeploymentState() (*DeploymentState, error) {
	state := &DeploymentState{
		StepsCompleted: make(map[string]bool),
	}

	// 检查各个步骤
	if completed, _ := c.CheckDirectories(); completed {
		state.StepsCompleted["prepare_directories"] = true
	}

	if completed, _ := c.CheckUser(); completed {
		state.StepsCompleted["create_user"] = true
	}

	if completed, _ := c.CheckSoftwareInstallation(); completed {
		state.StepsCompleted["deploy_software"] = true
	}

	if completed, _ := c.CheckDatabaseInit(); completed {
		state.StepsCompleted["init_database"] = true
	}

	if completed, _ := c.CheckPostgreSQLRunning(); completed {
		state.StepsCompleted["start_postgresql"] = true
	}

	if completed, _ := c.CheckReplication(); completed {
		state.StepsCompleted["setup_replication"] = true
	}

	if completed, _ := c.CheckPatroniInstalled(); completed {
		state.StepsCompleted["install_patroni"] = true
	}

	if completed, _ := c.CheckPatroniConfigured(); completed {
		state.StepsCompleted["configure_patroni"] = true
	}

	if completed, _ := c.CheckSystemdService(); completed {
		state.StepsCompleted["create_systemd_service"] = true
	}

	return state, nil
}

// getSlaveNodes 获取所有从节点
func (c *IdempotentChecker) getSlaveNodes() []*config.NodeConfig {
	var slaves []*config.NodeConfig
	for _, group := range c.config.Groups {
		for _, node := range group.Nodes {
			if !node.IsMaster {
				slaves = append(slaves, node)
			}
		}
	}
	return slaves
}

// DeploymentState 部署状态
type DeploymentState struct {
	StepsCompleted map[string]bool
}

// IsStepCompleted 检查特定步骤是否完成
func (s *DeploymentState) IsStepCompleted(stepName string) bool {
	completed, exists := s.StepsCompleted[stepName]
	return exists && completed
}

// GetCompletedSteps 获取已完成的步骤列表
func (s *DeploymentState) GetCompletedSteps() []string {
	var steps []string
	for step := range s.StepsCompleted {
		steps = append(steps, step)
	}
	return steps
}

// GetCompletionPercentage 获取完成百分比
func (s *DeploymentState) GetCompletionPercentage() float64 {
	totalSteps := 9.0 // 总步骤数
	completedSteps := float64(len(s.StepsCompleted))
	return (completedSteps / totalSteps) * 100
}

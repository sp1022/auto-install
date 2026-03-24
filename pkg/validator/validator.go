// Package validator 提供多节点连接验证功能
// 基于 .pgpass 管理 PostgreSQL 凭证，支持并发验证
package validator

import (
	"fmt"
	"sync"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/credentials"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// ValidationResult 验证结果
type ValidationResult struct {
	NodeID     string
	Host       string
	Port       int
	SSHSuccess bool
	PGSuccess  bool
	PGChecked  bool
	SSHErr     error
	PGErr      error
	IsMaster   bool
	IsLocal    bool
}

// Validator 多节点连接验证器
type Validator struct {
	config   *config.Config
	pgpass   *credentials.PGPass
	executor *executor.Executor
	nodes    []*executor.Node // 缓存节点列表
	logger   *logger.Logger
	username string // PostgreSQL 用户名
	database string // 数据库名（默认 postgres）
}

// New 创建新的验证器
func New(cfg *config.Config, username string, log *logger.Logger) (*Validator, error) {
	// 创建 .pgpass 管理器
	pgpass, err := credentials.NewPGPass(log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize .pgpass: %w", err)
	}

	// 创建执行器
	nodes := make([]*executor.Node, 0, len(cfg.GetAllNodes()))
	for _, nodeCfg := range cfg.GetAllNodes() {
		node := &executor.Node{
			ID:       fmt.Sprintf("%s:%d", nodeCfg.Host, nodeCfg.Port),
			Host:     nodeCfg.Host,
			Port:     22, // SSH 端口
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
			// KeyPath 可以根据需要添加
		}
		nodes = append(nodes, node)
	}

	exec, err := executor.New(executor.Config{
		Nodes:         nodes,
		MaxConcurrent: 10, // 适合 10-50 节点
		Logger:        log,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	return &Validator{
		config:   cfg,
		pgpass:   pgpass,
		executor: exec,
		nodes:    nodes,
		logger:   log,
		username: username,
		database: "postgres", // 默认数据库
	}, nil
}

// ValidateSSH 验证 SSH 连接
func (v *Validator) ValidateSSH() []*ValidationResult {
	v.logger.Info("Validating SSH connections",
		logger.Fields{"node_count": len(v.config.GetAllNodes())})

	// 测试连接
	connectedNodes := v.executor.TestConnection(v.nodes)

	// 构建结果
	results := make([]*ValidationResult, 0, len(v.config.GetAllNodes()))

	// 生成结果
	for _, nodeCfg := range v.config.GetAllNodes() {
		key := fmt.Sprintf("%s:%d", nodeCfg.Host, nodeCfg.Port)
		result := &ValidationResult{
			NodeID:   key,
			Host:     nodeCfg.Host,
			Port:     nodeCfg.Port,
			IsMaster: nodeCfg.IsMaster,
		}

		// 检查是否在已连接列表中
		found := false
		for _, connected := range connectedNodes {
			if connected.ID == key {
				found = true
				break
			}
		}
		result.SSHSuccess = found

		if !found {
			result.SSHErr = fmt.Errorf("SSH connection failed")
		}

		results = append(results, result)
	}

	// 记录统计
	successCount := 0
	for _, r := range results {
		if r.SSHSuccess {
			successCount++
		}
	}

	v.logger.Info("SSH validation completed",
		logger.Fields{
			"total":      len(results),
			"successful": successCount,
			"failed":     len(results) - successCount,
		})

	return results
}

// ValidatePostgreSQL 验证 PostgreSQL 连接
// 在 SSH 成功的节点上直接通过 SSH 执行 pg_isready 验证 PostgreSQL 连接
// 不需要本地 .pgpass，因为 root SSH 可以直接在目标节点执行命令
func (v *Validator) ValidatePostgreSQL(sshResults []*ValidationResult) []*ValidationResult {
	v.logger.Info("Validating PostgreSQL connections",
		logger.Fields{})

	// 收集需要验证的节点
	var nodesToValidate []*config.NodeConfig
	for _, sshResult := range sshResults {
		if !sshResult.SSHSuccess {
			continue
		}

		// 查找对应的节点配置
		for _, nodeCfg := range v.config.GetAllNodes() {
			key := fmt.Sprintf("%s:%d", nodeCfg.Host, nodeCfg.Port)
			if key == sshResult.NodeID {
				nodesToValidate = append(nodesToValidate, nodeCfg)
				break
			}
		}
	}

	v.logger.Debug("Testing PostgreSQL connectivity via SSH",
		logger.Fields{"nodes": len(nodesToValidate)})

	// 并发验证 PostgreSQL 连接
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]*ValidationResult, len(sshResults))
	copy(results, sshResults)

	for _, nodeCfg := range nodesToValidate {
		wg.Add(1)

		go func(nc *config.NodeConfig) {
			defer wg.Done()

			key := fmt.Sprintf("%s:%d", nc.Host, nc.Port)

			// 通过 SSH 执行 pg_isready 检查 PostgreSQL 是否运行
			// 不需要密码，因为 pg_isready 可以本地连接（通过 unix socket）
			pgBinDir := v.config.PGSoftDir + "/bin"
			cmd := fmt.Sprintf("%s/pg_isready -h localhost -p %d -U %s -d %s",
				pgBinDir, nc.Port, v.username, v.database)

			result := v.executor.RunOnNode(&executor.Node{
				ID:   key,
				Host: nc.Host,
				User: v.config.SSHUser,
			}, cmd, false, false)

			var err error
			if result.Error != nil {
				err = result.Error
				// 获取更详细的诊断信息
				diagCmd := fmt.Sprintf("echo '=== PostgreSQL Process ===' && ps aux | grep postgres | grep -v grep || echo 'No postgres processes found'; echo ''; echo '=== PostgreSQL Log (last 20 lines) ===' && tail -20 /var/log/postgresql/postgresql.log 2>/dev/null || echo 'No log file found'; echo ''; echo '=== Data Directory Status ===' && ls -ld %s 2>/dev/null || echo 'Data directory not found'", nc.DataDir)
				diagResult := v.executor.RunOnNode(&executor.Node{
					ID:   key,
					Host: nc.Host,
					User: v.config.SSHUser,
				}, diagCmd, false, true)
				if diagResult.Error == nil && diagResult.Output != "" {
					v.logger.Debug("PostgreSQL diagnostic info", logger.Fields{
						"host":   nc.Host,
						"port":   nc.Port,
						"output": diagResult.Output,
					})
				}
			}

			mu.Lock()
			defer mu.Unlock()

			// 更新对应的结果
			for i, r := range results {
				if r.NodeID == key {
					results[i].PGChecked = true
					results[i].PGSuccess = (err == nil)
					results[i].PGErr = err
					break
				}
			}

			if err != nil {
				v.logger.Warn("PostgreSQL connection failed",
					logger.Fields{
						"host":  nc.Host,
						"port":  nc.Port,
						"error": err,
					})
			} else {
				v.logger.Debug("PostgreSQL connection successful",
					logger.Fields{
						"host": nc.Host,
						"port": nc.Port,
					})
			}
		}(nodeCfg)
	}

	wg.Wait()

	// 记录统计
	successCount := 0
	for _, r := range results {
		if r.PGSuccess {
			successCount++
		}
	}

	v.logger.Info("PostgreSQL validation completed",
		logger.Fields{
			"total":      len(results),
			"successful": successCount,
			"failed":     len(results) - successCount,
		})

	return results
}

// ValidateAll 验证所有连接（SSH + PostgreSQL）
func (v *Validator) ValidateAll() []*ValidationResult {
	v.logger.Info("Starting full validation",
		logger.Fields{})

	// 1. 验证 SSH
	sshResults := v.ValidateSSH()

	// 2. 验证 PostgreSQL（仅在 SSH 成功的节点上）
	pgResults := v.ValidatePostgreSQL(sshResults)

	return pgResults
}

// AddCredentials 批量添加凭证到 .pgpass
func (v *Validator) AddCredentials(hostname, port, database, username, password string) error {
	return v.pgpass.Add(hostname, port, database, username, password)
}

// AddCredentialsForNodes 为所有节点添加凭证
func (v *Validator) AddCredentialsForNodes(password string) error {
	v.logger.Info("Adding credentials for all nodes",
		logger.Fields{})

	for _, nodeCfg := range v.config.GetAllNodes() {
		port := fmt.Sprintf("%d", nodeCfg.Port)
		if err := v.pgpass.Add(nodeCfg.Host, port, v.database, v.username, password); err != nil {
			v.logger.Warn("Failed to add credentials",
				logger.Fields{
					"host":  nodeCfg.Host,
					"port":  nodeCfg.Port,
					"error": err,
				})
			continue
		}

		v.logger.Debug("Credentials added",
			logger.Fields{
				"host":     nodeCfg.Host,
				"port":     nodeCfg.Port,
				"username": v.username,
			})
	}

	return nil
}

// GenerateReport 生成验证报告
func (v *Validator) GenerateReport(results []*ValidationResult) string {
	var report string

	report += "=== Connection Validation Report ===\n\n"

	// SSH 连接统计
	sshSuccess := 0
	sshFailed := 0
	for _, r := range results {
		if r.SSHSuccess {
			sshSuccess++
		} else {
			sshFailed++
		}
	}

	report += fmt.Sprintf("SSH Connections:\n")
	report += fmt.Sprintf("  Total:      %d\n", len(results))
	report += fmt.Sprintf("  Successful: %d\n", sshSuccess)
	report += fmt.Sprintf("  Failed:     %d\n\n", sshFailed)

	// PostgreSQL 连接统计
	pgSuccess := 0
	pgFailed := 0
	for _, r := range results {
		if r.PGChecked && r.PGSuccess {
			pgSuccess++
		} else if r.PGChecked {
			pgFailed++
		}
	}

	report += fmt.Sprintf("PostgreSQL Connections:\n")
	report += fmt.Sprintf("  Checked:    %d\n", pgSuccess+pgFailed)
	report += fmt.Sprintf("  Successful: %d\n", pgSuccess)
	report += fmt.Sprintf("  Failed:     %d\n\n", pgFailed)

	// 详细失败信息
	report += "=== Failed Nodes ===\n\n"
	hasFailure := false

	for _, r := range results {
		if !r.SSHSuccess || (r.PGChecked && !r.PGSuccess) {
			hasFailure = true
			report += fmt.Sprintf("Node: %s\n", r.NodeID)
			report += fmt.Sprintf("  Host: %s, Port: %d\n", r.Host, r.Port)
			report += fmt.Sprintf("  SSH: ")
			if r.SSHSuccess {
				report += "OK\n"
			} else {
				report += fmt.Sprintf("FAILED - %v\n", r.SSHErr)
			}
			report += fmt.Sprintf("  PostgreSQL: ")
			if !r.PGChecked {
				report += "SKIPPED\n"
			} else if r.PGSuccess {
				report += "OK\n"
			} else {
				report += fmt.Sprintf("FAILED - %v\n", r.PGErr)
			}
			report += "\n"
		}
	}

	if !hasFailure {
		report += "All nodes passed validation!\n\n"
	}

	return report
}

// ValidateDeployment 验证部署环境
// 检查节点可达性、磁盘空间、依赖等
func (v *Validator) ValidateDeployment() error {
	v.logger.Info("Validating deployment environment",
		logger.Fields{})

	// 1. SSH 连接验证
	sshResults := v.ValidateSSH()
	for _, r := range sshResults {
		if !r.SSHSuccess {
			return fmt.Errorf("SSH connection failed to %s: %w", r.NodeID, r.SSHErr)
		}
	}

	// 2. 检查磁盘空间（示例）
	// TODO: 实现磁盘空间检查

	// 3. 检查依赖（示例）
	// TODO: 实现依赖检查

	v.logger.Info("Deployment environment validation passed",
		logger.Fields{})
	return nil
}

// Package topology 提供PostgreSQL集群拓扑管理功能
// 支持Citus分布式、Patroni高可用和主从复制拓扑
package topology

import (
	"fmt"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// CitusManager Citus分布式集群管理器
type CitusManager struct {
	config      *config.Config
	executor    *executor.Executor
	logger      *logger.Logger
	coordinator *config.NodeConfig
	workers     []*config.NodeConfig
}

// NewCitusManager 创建Citus管理器
func NewCitusManager(cfg *config.Config, exec *executor.Executor, log *logger.Logger) *CitusManager {
	// 查找coordinator和worker
	var coordinator *config.NodeConfig
	var workers []*config.NodeConfig

	for _, group := range cfg.Groups {
		if group.Role == "coordinator" {
			// 第一个节点是coordinator
			if len(group.Nodes) > 0 {
				coordinator = group.Nodes[0]
			}
			// 其余节点是worker
			if len(group.Nodes) > 1 {
				workers = append(workers, group.Nodes[1:]...)
			}
		} else if group.Role == "worker" {
			workers = append(workers, group.Nodes...)
		}
	}

	return &CitusManager{
		config:      cfg,
		executor:    exec,
		logger:      log,
		coordinator: coordinator,
		workers:     workers,
	}
}

// ConfigureCoordinator 配置Citus Coordinator
func (m *CitusManager) ConfigureCoordinator() error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	m.logger.Info("Configuring Citus coordinator",
		logger.Fields{
			"host": m.coordinator.Host,
			"port": m.coordinator.Port,
		})

	// 1. 创建extension
	if err := m.createExtension(m.coordinator, "citus"); err != nil {
		return fmt.Errorf("failed to create citus extension: %w", err)
	}

	// 2. 配置citus.local_hostname
	if err := m.setCoordinatorHostname(); err != nil {
		return fmt.Errorf("failed to set coordinator hostname: %w", err)
	}

	// 3. 设置工作内存和连接数
	if err := m.configureCoordinatorSettings(); err != nil {
		return fmt.Errorf("failed to configure coordinator settings: %w", err)
	}

	m.logger.Info("Citus coordinator configured successfully",
		logger.Fields{"host": m.coordinator.Host})

	return nil
}

// createExtension 在指定节点创建PostgreSQL扩展
func (m *CitusManager) createExtension(node *config.NodeConfig, extensionName string) error {
	pgBinDir := m.config.PGSoftDir + "/bin"
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"CREATE EXTENSION IF NOT EXISTS %s;\\\"\"",
		pgBinDir, node.Port, extensionName)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	m.logger.Info("Extension created",
		logger.Fields{
			"node":      node.Host,
			"extension": extensionName,
		})

	return nil
}

// setCoordinatorHostname 设置coordinator主机名
func (m *CitusManager) setCoordinatorHostname() error {
	pgBinDir := m.config.PGSoftDir + "/bin"
	pgData := m.coordinator.DataDir

	// 在postgresql.conf中添加citus.local_hostname
	configCmd := fmt.Sprintf("echo \"citus.local_hostname = '%s' >> %s/postgresql.conf",
		m.coordinator.Host, pgData)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, configCmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 重载配置
	reloadCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s reload\"",
		pgBinDir, pgData)

	result = m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, reloadCmd, true, false)
	return result.Error
}

// configureCoordinatorSettings 配置coordinator参数
func (m *CitusManager) configureCoordinatorSettings() error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	settings := []struct {
		name  string
		value string
	}{
		{"max_connections", "200"},
		{"shared_buffers", "256MB"},
		{"work_mem", "32MB"},
		{"maintenance_work_mem", "128MB"},
		{"citus.total_worker_count", fmt.Sprintf("%d", len(m.workers))},
	}

	for _, setting := range settings {
		cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"ALTER SYSTEM SET %s = '%s';\\\"\"",
			pgBinDir, m.coordinator.Port, setting.name, setting.value)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   m.coordinator.Host,
			Host: m.coordinator.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			m.logger.Warn("Failed to set parameter",
				logger.Fields{
					"parameter": setting.name,
					"error":     result.Error,
				})
		}
	}

	// 重载配置
	pgData := m.coordinator.DataDir
	reloadCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s reload\"",
		pgBinDir, pgData)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, reloadCmd, true, false)
	return result.Error
}

// RegisterWorkers 注册Worker节点到Coordinator
func (m *CitusManager) RegisterWorkers() error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	if len(m.workers) == 0 {
		return fmt.Errorf("no worker nodes found")
	}

	m.logger.Info("Registering worker nodes",
		logger.Fields{
			"coordinator":  m.coordinator.Host,
			"worker_count": len(m.workers),
		})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 为每个worker执行添加命令
	for _, worker := range m.workers {
		// 添加worker到coordinator
		cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT master_add_node('%s', %d);\\\"\"",
			pgBinDir, m.coordinator.Port, worker.Host, worker.Port)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   m.coordinator.Host,
			Host: m.coordinator.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			m.logger.Warn("Failed to register worker",
				logger.Fields{
					"worker": worker.Host,
					"error":  result.Error,
				})
			continue
		}

		m.logger.Info("Worker registered",
			logger.Fields{
				"worker": worker.Host,
				"port":   worker.Port,
			})
	}

	// 验证worker节点
	time.Sleep(2 * time.Second) // 等待元数据同步

	if err := m.validateWorkers(); err != nil {
		return fmt.Errorf("worker validation failed: %w", err)
	}

	m.logger.Info("All workers registered successfully", logger.Fields{})
	return nil
}

// validateWorkers 验证worker节点状态
func (m *CitusManager) validateWorkers() error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	// 查询worker节点状态
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT * FROM master_get_active_worker_nodes();\\\"\"",
		pgBinDir, m.coordinator.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return result.Error
	}

	// 解析结果
	lines := strings.Split(result.Output, "\n")
	activeWorkers := 0
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "nodename") {
			activeWorkers++
		}
	}

	m.logger.Info("Worker validation completed",
		logger.Fields{
			"total_workers":  len(m.workers),
			"active_workers": activeWorkers,
		})

	if activeWorkers != len(m.workers) {
		return fmt.Errorf("expected %d active workers, got %d", len(m.workers), activeWorkers)
	}

	return nil
}

// CreateDistributedTable 创建分布式表
func (m *CitusManager) CreateDistributedTable(tableName, distributionColumn string, colocateWith string) error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	m.logger.Info("Creating distributed table",
		logger.Fields{
			"table":              tableName,
			"distributionColumn": distributionColumn,
			"colocateWith":       colocateWith,
		})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 1. 创建主表（如果不存在）
	createTableSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id SERIAL PRIMARY KEY,
			name TEXT,
			created_at TIMESTAMP DEFAULT NOW()
		)`, tableName)

	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"%s\\\"\"",
		pgBinDir, m.coordinator.Port, createTableSQL)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to create table: %w", result.Error)
	}

	// 2. 设置为分布式表
	var distributeSQL string
	if colocateWith != "" {
		distributeSQL = fmt.Sprintf("SELECT create_distributed_table('%s', '%s', colocate_with => '%s')",
			tableName, distributionColumn, colocateWith)
	} else {
		distributeSQL = fmt.Sprintf("SELECT create_distributed_table('%s', '%s')",
			tableName, distributionColumn)
	}

	cmd = fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"%s\\\"\"",
		pgBinDir, m.coordinator.Port, distributeSQL)

	result = m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to distribute table: %w", result.Error)
	}

	m.logger.Info("Distributed table created successfully",
		logger.Fields{"table": tableName})

	return nil
}

// CreateReferenceTable 创建参考表（复制到所有worker）
func (m *CitusManager) CreateReferenceTable(tableName string) error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	m.logger.Info("Creating reference table",
		logger.Fields{"table": tableName})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 创建参考表
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT create_reference_table('%s');\\\"\"",
		pgBinDir, m.coordinator.Port, tableName)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to create reference table: %w", result.Error)
	}

	m.logger.Info("Reference table created successfully",
		logger.Fields{"table": tableName})

	return nil
}

// RebalanceCluster 重新平衡集群数据
func (m *CitusManager) RebalanceCluster() error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	m.logger.Info("Rebalancing cluster data", logger.Fields{})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 执行重新平衡
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT rebalance_table_shards();\\\"\"",
		pgBinDir, m.coordinator.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to rebalance cluster: %w", result.Error)
	}

	m.logger.Info("Cluster rebalanced successfully", logger.Fields{})
	return nil
}

// GetClusterStatus 获取集群状态
func (m *CitusManager) GetClusterStatus() (*CitusClusterStatus, error) {
	if m.coordinator == nil {
		return nil, fmt.Errorf("no coordinator node found")
	}

	pgBinDir := m.config.PGSoftDir + "/bin"

	status := &CitusClusterStatus{
		Coordinator: m.coordinator.Host,
		Workers:     make([]WorkerStatus, 0),
	}

	// 查询worker状态
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT * FROM master_get_active_worker_nodes();\\\"\"",
		pgBinDir, m.coordinator.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return nil, result.Error
	}

	// 解析worker状态
	lines := strings.Split(result.Output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "nodename") {
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				worker := WorkerStatus{
					Host: strings.TrimSpace(parts[0]),
					Port: strings.TrimSpace(parts[1]),
				}
				status.Workers = append(status.Workers, worker)
			}
		}
	}

	status.TotalWorkers = len(status.Workers)
	status.Healthy = status.TotalWorkers == len(m.workers)

	return status, nil
}

// CitusClusterStatus Citus集群状态
type CitusClusterStatus struct {
	Coordinator  string
	Workers      []WorkerStatus
	TotalWorkers int
	Healthy      bool
}

// WorkerStatus Worker节点状态
type WorkerStatus struct {
	Host string
	Port string
}

// ConfigureWorker 配置Worker节点
func (m *CitusManager) ConfigureWorker(worker *config.NodeConfig) error {
	m.logger.Info("Configuring Citus worker",
		logger.Fields{
			"host": worker.Host,
			"port": worker.Port,
		})

	// 1. 创建citus扩展
	if err := m.createExtension(worker, "citus"); err != nil {
		return fmt.Errorf("failed to create citus extension on worker: %w", err)
	}

	// 2. 配置worker设置
	if err := m.configureWorkerSettings(worker); err != nil {
		return fmt.Errorf("failed to configure worker settings: %w", err)
	}

	m.logger.Info("Citus worker configured successfully",
		logger.Fields{"host": worker.Host})

	return nil
}

// configureWorkerSettings 配置worker参数
func (m *CitusManager) configureWorkerSettings(worker *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"
	pgData := worker.DataDir

	settings := []struct {
		name  string
		value string
	}{
		{"max_connections", "200"},
		{"shared_buffers", "256MB"},
		{"work_mem", "32MB"},
	}

	for _, setting := range settings {
		cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"ALTER SYSTEM SET %s = '%s';\\\"\"",
			pgBinDir, worker.Port, setting.name, setting.value)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   worker.Host,
			Host: worker.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			m.logger.Warn("Failed to set worker parameter",
				logger.Fields{
					"worker":    worker.Host,
					"parameter": setting.name,
					"error":     result.Error,
				})
		}
	}

	// 重载配置
	reloadCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s reload\"",
		pgBinDir, pgData)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   worker.Host,
		Host: worker.Host,
		User: m.config.SSHUser,
	}, reloadCmd, true, false)
	return result.Error
}

// ConfigureAllWorkers 配置所有Worker节点
func (m *CitusManager) ConfigureAllWorkers() error {
	m.logger.Info("Configuring all worker nodes",
		logger.Fields{"count": len(m.workers)})

	for _, worker := range m.workers {
		if err := m.ConfigureWorker(worker); err != nil {
			m.logger.Warn("Failed to configure worker",
				logger.Fields{
					"worker": worker.Host,
					"error":  err,
				})
			// 继续配置其他worker
		}
	}

	return nil
}

// RemoveWorker 从集群中移除Worker
func (m *CitusManager) RemoveWorker(worker *config.NodeConfig) error {
	if m.coordinator == nil {
		return fmt.Errorf("no coordinator node found")
	}

	m.logger.Info("Removing worker from cluster",
		logger.Fields{
			"worker": worker.Host,
			"port":   worker.Port,
		})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 移除worker
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT master_remove_node('%s', %d);\\\"\"",
		pgBinDir, m.coordinator.Port, worker.Host, worker.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to remove worker: %w", result.Error)
	}

	m.logger.Info("Worker removed successfully",
		logger.Fields{"worker": worker.Host})

	return nil
}

// GetTableDistribution 获取表的分布信息
func (m *CitusManager) GetTableDistribution(tableName string) ([]ShardDistribution, error) {
	if m.coordinator == nil {
		return nil, fmt.Errorf("no coordinator node found")
	}

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 查询分片分布
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT * FROM pg_dist_shard WHERE logicalrelid = '%s'::regclass;\\\"\"",
		pgBinDir, m.coordinator.Port, tableName)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.coordinator.Host,
		Host: m.coordinator.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return nil, result.Error
	}

	// 解析结果（简化实现）
	distributions := make([]ShardDistribution, 0)
	lines := strings.Split(result.Output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "shardid") {
			parts := strings.Split(line, "|")
			if len(parts) >= 4 {
				dist := ShardDistribution{
					ShardID:    strings.TrimSpace(parts[0]),
					LogicalRel: strings.TrimSpace(parts[1]),
					ShardIndex: strings.TrimSpace(parts[2]),
					ShardState: strings.TrimSpace(parts[3]),
				}
				distributions = append(distributions, dist)
			}
		}
	}

	return distributions, nil
}

// ShardDistribution 分片分布信息
type ShardDistribution struct {
	ShardID    string
	LogicalRel string
	ShardIndex string
	ShardState string
}

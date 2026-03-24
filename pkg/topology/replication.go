// Package topology 提供主从复制增强功能
package topology

import (
	"fmt"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// ReplicationManager 主从复制管理器
type ReplicationManager struct {
	config            *config.Config
	executor          *executor.Executor
	logger            *logger.Logger
	master            *config.NodeConfig
	standbys          []*config.NodeConfig
	cascadingStandbys []*config.NodeConfig
}

// NewReplicationManager 创建复制管理器
func NewReplicationManager(cfg *config.Config, exec *executor.Executor, log *logger.Logger) *ReplicationManager {
	masters := cfg.GetMasterNodes()
	var master *config.NodeConfig
	if len(masters) > 0 {
		master = masters[0]
	}

	var standbys []*config.NodeConfig
	for _, group := range cfg.Groups {
		for _, node := range group.Nodes {
			if !node.IsMaster {
				standbys = append(standbys, node)
			}
		}
	}

	return &ReplicationManager{
		config:   cfg,
		executor: exec,
		logger:   log,
		master:   master,
		standbys: standbys,
	}
}

// ConfigureSynchronousReplication 配置同步复制
func (m *ReplicationManager) ConfigureSynchronousReplication() error {
	if m.master == nil {
		return fmt.Errorf("no master node found")
	}

	m.logger.Info("Configuring synchronous replication", logger.Fields{})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 1. 设置synchronous_standby_names
	if len(m.standbys) > 0 {
		// 构建同步备用节点列表
		var syncStandbys []string
		for i, standby := range m.standbys {
			if i < 2 { // 最多2个同步节点
				syncStandbys = append(syncStandbys, fmt.Sprintf("'%s'", standby.Name))
			}
		}

		synchronousStandbys := fmt.Sprintf("{%s}", strings.Join(syncStandbys, ","))
		cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"ALTER SYSTEM SET synchronous_standby_names = '%s';\\\"\"",
			pgBinDir, m.master.Port, synchronousStandbys)

		result := m.executor.RunOnNode(&executor.Node{
			ID:   m.master.Host,
			Host: m.master.Host,
			User: m.config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			return fmt.Errorf("failed to set synchronous_standby_names: %w", result.Error)
		}

		// 重载配置
		if err := m.reloadConfig(m.master); err != nil {
			return err
		}

		m.logger.Info("Synchronous replication configured",
			logger.Fields{
				"sync_standbys": synchronousStandbys,
			})
	}

	return nil
}

// ConfigureCascadingReplication 配置级联复制
func (m *ReplicationManager) ConfigureCascadingReplication() error {
	if len(m.standbys) < 2 {
		m.logger.Info("Not enough standbys for cascading replication", logger.Fields{})
		return nil
	}

	m.logger.Info("Configuring cascading replication", logger.Fields{})

	// 将第一个备用节点作为级联复制的主机
	cascadingMaster := m.standbys[0]
	cascadingSlaves := m.standbys[1:]

	// 配置级联主节点允许复制连接
	if err := m.configureCascadingMaster(cascadingMaster); err != nil {
		return fmt.Errorf("failed to configure cascading master: %w", err)
	}

	// 从级联主节点初始化其他备用节点
	for _, slave := range cascadingSlaves {
		if err := m.initCascadingSlave(cascadingMaster, slave); err != nil {
			m.logger.Warn("Failed to init cascading slave",
				logger.Fields{
					"slave": slave.Host,
					"error": err,
				})
		}
	}

	m.logger.Info("Cascading replication configured", logger.Fields{})
	return nil
}

// configureCascadingMaster 配置级联主节点
func (m *ReplicationManager) configureCascadingMaster(node *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	// 创建级联复制用户
	replicationUser := "cascading_replicator"
	replicationPassword, err := generateRandomPassword(16)
	if err != nil {
		return fmt.Errorf("failed to generate replication password: %w", err)
	}

	// 创建用户
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"CREATE USER %s WITH REPLICATION ENCRYPTED PASSWORD '%s';\\\"\"",
		pgBinDir, node.Port, replicationUser, replicationPassword)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 配置pg_hba.conf允许复制连接
	pgHbaRule := fmt.Sprintf("host    replication    %s    %s/32    md5",
		replicationUser, getCascadingNetwork(node))

	cmd = fmt.Sprintf("echo '%s' >> %s/pg_hba.conf",
		pgHbaRule, node.DataDir)

	result = m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 重载配置
	return m.reloadConfig(node)
}

// initCascadingSlave 初始化级联备用节点
func (m *ReplicationManager) initCascadingSlave(master, slave *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	// 使用pg_basebackup从级联主节点初始化
	cmd := fmt.Sprintf("su - postgres -c \"%s/pg_basebackup -h %s -p %d -D %s -U cascading_replicator -P -R -X stream\"",
		pgBinDir, master.Host, master.Port, slave.DataDir)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 处理WAL目录软链接
	if slave.WALDir != "" {
		rmCmd := fmt.Sprintf("rm -rf %s/pg_wal && ln -s %s %s/pg_wal",
			slave.DataDir, slave.WALDir, slave.DataDir)
		result := m.executor.RunOnNode(&executor.Node{
			ID:   slave.Host,
			Host: slave.Host,
			User: m.config.SSHUser,
		}, rmCmd, true, false)
		if result.Error != nil {
			m.logger.Warn("Failed to link WAL directory",
				logger.Fields{
					"slave": slave.Host,
					"error": result.Error,
				})
		}
	}

	// 启动备用节点
	startCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s start\"",
		pgBinDir, slave.DataDir)

	result = m.executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: m.config.SSHUser,
	}, startCmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	m.logger.Info("Cascading slave initialized",
		logger.Fields{
			"cascading_master": master.Host,
			"slave":            slave.Host,
		})

	return nil
}

// GetReplicationStatus 获取复制状态
func (m *ReplicationManager) GetReplicationStatus() (*ReplicationStatus, error) {
	if m.master == nil {
		return nil, fmt.Errorf("no master node found")
	}

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 查询复制状态
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT * FROM pg_stat_replication;\\\"\"",
		pgBinDir, m.master.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.master.Host,
		Host: m.master.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return nil, result.Error
	}

	status := &ReplicationStatus{
		MasterHost: m.master.Host,
		Standbys:   make([]StandbyStatus, 0),
	}

	// 解析结果
	lines := strings.Split(result.Output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "pid") {
			parts := strings.Split(line, "|")
			if len(parts) >= 10 {
				standby := StandbyStatus{
					ApplicationName: strings.TrimSpace(parts[2]),
					ClientAddr:      strings.TrimSpace(parts[3]),
					State:           strings.TrimSpace(parts[5]),
					SyncState:       strings.TrimSpace(parts[6]),
					SyncPriority:    strings.TrimSpace(parts[7]),
					LagBytes:        strings.TrimSpace(parts[9]),
				}
				status.Standbys = append(status.Standbys, standby)
			}
		}
	}

	// 查询每个备用节点的复制延迟
	for _, standby := range m.standbys {
		if err := m.queryReplicationLag(standby, status); err != nil {
			m.logger.Warn("Failed to query replication lag",
				logger.Fields{
					"standby": standby.Host,
					"error":   err,
				})
		}
	}

	status.Healthy = len(status.Standbys) == len(m.standbys)

	return status, nil
}

// queryReplicationLag 查询复制延迟
func (m *ReplicationManager) queryReplicationLag(standby *config.NodeConfig, status *ReplicationStatus) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	// 查询复制延迟（字节数）
	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS lag_bytes FROM pg_stat_replication;\\\"\"",
		pgBinDir, standby.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   standby.Host,
		Host: standby.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return result.Error
	}

	// 解析延迟值
	lines := strings.Split(result.Output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "lag_bytes") {
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				lagBytes := strings.TrimSpace(parts[1])
				status.ReplicationLags = append(status.ReplicationLags, ReplicationLag{
					StandbyHost: standby.Host,
					LagBytes:    lagBytes,
				})
			}
		}
	}

	return nil
}

// ReplicationStatus 复制状态
type ReplicationStatus struct {
	MasterHost      string
	Standbys        []StandbyStatus
	ReplicationLags []ReplicationLag
	Healthy         bool
}

// StandbyStatus 备用节点状态
type StandbyStatus struct {
	ApplicationName string
	ClientAddr      string
	State           string
	SyncState       string
	SyncPriority    string
	LagBytes        string
}

// ReplicationLag 复制延迟
type ReplicationLag struct {
	StandbyHost string
	LagBytes    string
}

// MonitorReplicationDelay 监控复制延迟
func (m *ReplicationManager) MonitorReplicationDelay(interval time.Duration, callback func(*ReplicationStatus)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		status, err := m.GetReplicationStatus()
		if err != nil {
			m.logger.Warn("Failed to get replication status",
				logger.Fields{"error": err})
			continue
		}

		callback(status)

		// 检查是否有异常延迟
		for _, lag := range status.ReplicationLags {
			// TODO: 解析lag值并检查阈值
			if lag.LagBytes != "" {
				m.logger.Debug("Replication lag",
					logger.Fields{
						"standby": lag.StandbyHost,
						"lag":     lag.LagBytes,
					})
			}
		}
	}

	return nil
}

// PromoteStandby 提升备用节点为主节点
func (m *ReplicationManager) PromoteStandby(standby *config.NodeConfig) error {
	m.logger.Info("Promoting standby to master",
		logger.Fields{
			"standby": standby.Host,
		})

	pgBinDir := m.config.PGSoftDir + "/bin"

	// 停止复制并提升为主节点
	cmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl promote -D %s\"",
		pgBinDir, standby.DataDir)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   standby.Host,
		Host: standby.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to promote standby: %w", result.Error)
	}

	// 更新节点标记（需要在配置中更新）
	// TODO: 更新config中的master节点

	m.logger.Info("Standby promoted successfully",
		logger.Fields{
			"new_master": standby.Host,
		})

	return nil
}

// ReconfigureReplication 重新配置复制（故障转移后）
func (m *ReplicationManager) ReconfigureReplication(newMaster *config.NodeConfig) error {
	m.logger.Info("Reconfiguring replication",
		logger.Fields{
			"new_master": newMaster.Host,
		})

	// 1. 创建新的复制用户
	if err := m.createReplicationUser(newMaster); err != nil {
		return fmt.Errorf("failed to create replication user: %w", err)
	}

	// 2. 配置新主节点允许复制连接
	if err := m.configureMasterForReplication(newMaster); err != nil {
		return fmt.Errorf("failed to configure master: %w", err)
	}

	// 3. 重定向其他备用节点到新主节点
	for _, standby := range m.standbys {
		if standby.Host == newMaster.Host {
			continue
		}

		if err := m.redirectStandby(standby, newMaster); err != nil {
			m.logger.Warn("Failed to redirect standby",
				logger.Fields{
					"standby": standby.Host,
					"error":   err,
				})
		}
	}

	m.logger.Info("Replication reconfigured", logger.Fields{})
	return nil
}

// createReplicationUser 创建复制用户
func (m *ReplicationManager) createReplicationUser(master *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	replicationPassword, err := generateRandomPassword(16)
	if err != nil {
		return fmt.Errorf("failed to generate replication password: %w", err)
	}

	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"CREATE USER replicator WITH REPLICATION ENCRYPTED PASSWORD '%s';\\\"\"",
		pgBinDir, master.Port, replicationPassword)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   master.Host,
		Host: master.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	return result.Error
}

// configureMasterForReplication 配置主节点
func (m *ReplicationManager) configureMasterForReplication(master *config.NodeConfig) error {
	// 修改pg_hba.conf
	pgHbaRule := fmt.Sprintf("host    replication    replicator    %s/32    md5",
		getMasterNetwork(m.config))

	cmd := fmt.Sprintf("echo '%s' >> %s/pg_hba.conf",
		pgHbaRule, master.DataDir)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   master.Host,
		Host: master.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 重载配置
	return m.reloadConfig(master)
}

// redirectStandby 重定向备用节点
func (m *ReplicationManager) redirectStandby(standby, newMaster *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	// 更新standby.signal中的连接信息
	// 需要重新创建primary_conninfo
	// TODO: 使用 primaryConninfo 更新备用节点的连接配置
	// primaryConninfo := fmt.Sprintf("host=%s port=%d user=replicator",
	// 	newMaster.Host, newMaster.Port)

	// 停止备用节点
	stopCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s stop\"",
		pgBinDir, standby.DataDir)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   standby.Host,
		Host: standby.Host,
		User: m.config.SSHUser,
	}, stopCmd, true, false)
	if result.Error != nil {
		return result.Error
	}

	// 更新primary_conninfo
	// TODO: 实现更新postgresql.conf的逻辑

	// 重新启动备用节点
	startCmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s start\"",
		pgBinDir, standby.DataDir)

	result = m.executor.RunOnNode(&executor.Node{
		ID:   standby.Host,
		Host: standby.Host,
		User: m.config.SSHUser,
	}, startCmd, true, false)
	return result.Error
}

// GetReplicationSlots 获取复制槽信息
func (m *ReplicationManager) GetReplicationSlots() ([]ReplicationSlot, error) {
	if m.master == nil {
		return nil, fmt.Errorf("no master node found")
	}

	pgBinDir := m.config.PGSoftDir + "/bin"

	cmd := fmt.Sprintf("su - postgres -c \"%s/psql -p %d -c \\\"SELECT * FROM pg_replication_slots;\\\"\"",
		pgBinDir, m.master.Port)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   m.master.Host,
		Host: m.master.Host,
		User: m.config.SSHUser,
	}, cmd, false, false)
	if result.Error != nil {
		return nil, result.Error
	}

	slots := make([]ReplicationSlot, 0)
	lines := strings.Split(result.Output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "|") && !strings.Contains(line, "slot_name") {
			parts := strings.Split(line, "|")
			if len(parts) >= 5 {
				slot := ReplicationSlot{
					SlotName:   strings.TrimSpace(parts[0]),
					SlotType:   strings.TrimSpace(parts[1]),
					Active:     strings.TrimSpace(parts[2]),
					RestartLSN: strings.TrimSpace(parts[4]),
				}
				slots = append(slots, slot)
			}
		}
	}

	return slots, nil
}

// ReplicationSlot 复制槽
type ReplicationSlot struct {
	SlotName   string
	SlotType   string
	Active     string
	RestartLSN string
}

// reloadConfig 重载配置
func (m *ReplicationManager) reloadConfig(node *config.NodeConfig) error {
	pgBinDir := m.config.PGSoftDir + "/bin"

	cmd := fmt.Sprintf("su - postgres -c \"%s/pg_ctl -D %s reload\"",
		pgBinDir, node.DataDir)

	result := m.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: m.config.SSHUser,
	}, cmd, true, false)
	return result.Error
}

// getMasterNetwork 获取主节点网络段
func getMasterNetwork(cfg *config.Config) string {
	masters := cfg.GetMasterNodes()
	if len(masters) == 0 {
		return "0.0.0.0"
	}

	parts := strings.Split(masters[0].Host, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".") + ".0"
	}

	return masters[0].Host
}

// getCascadingNetwork 获取级联节点网络段
func getCascadingNetwork(node *config.NodeConfig) string {
	parts := strings.Split(node.Host, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".") + ".0"
	}

	return node.Host
}

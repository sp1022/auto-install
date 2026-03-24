// Package deploy 提供主从复制和Patroni相关的部署步骤
package deploy

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/example/pg-deploy/pkg/topology"
)

// SetupReplicationStep 设置主从复制步骤
type SetupReplicationStep struct {
	BaseStep
	replicationPassword string
}

// NewSetupReplicationStep 创建设置复制步骤
func NewSetupReplicationStep() *SetupReplicationStep {
	return &SetupReplicationStep{
		BaseStep: BaseStep{
			name:        "SetupReplication",
			description: "Configure master-slave replication",
		},
	}
}

// Execute 执行复制设置
func (s *SetupReplicationStep) Execute(ctx *Context) error {
	masterNodes := ctx.Config.GetMasterNodes()
	if len(masterNodes) == 0 {
		return fmt.Errorf("no master nodes found")
	}

	master := masterNodes[0] // 使用第一个主节点

	// 1. 在主节点创建复制用户
	if err := s.createReplicationUser(ctx, master); err != nil {
		return fmt.Errorf("failed to create replication user: %w", err)
	}

	// 2. 配置主节点允许复制连接
	if err := s.configureMasterForReplication(ctx, master); err != nil {
		return fmt.Errorf("failed to configure master: %w", err)
	}

	// 3. 重载主节点配置
	if err := s.reloadMaster(ctx, master); err != nil {
		return fmt.Errorf("failed to reload master: %w", err)
	}

	// 4. 在从节点使用 pg_basebackup
	slaveNodes := s.getSlaveNodes(ctx)
	for _, slave := range slaveNodes {
		if err := s.setupSlave(ctx, master, slave); err != nil {
			return fmt.Errorf("failed to setup slave %s: %w", slave.Host, err)
		}
	}

	// 5. 启动从节点
	if err := s.startSlaves(ctx, slaveNodes); err != nil {
		return fmt.Errorf("failed to start slaves: %w", err)
	}

	return nil
}

// createReplicationUser 创建复制用户（幂等）
func (s *SetupReplicationStep) createReplicationUser(ctx *Context, master *config.NodeConfig) error {
	pgBinDir := ctx.Config.PGSoftDir + "/bin"

	// 生成随机密码
	password, err := generateRandomPassword(16)
	if err != nil {
		return fmt.Errorf("failed to generate replication password: %w", err)
	}

	if err := waitForPostgreSQLReady(ctx, master, 30*time.Second, "before creating replication user"); err != nil {
		return err
	}

	// 检查用户是否已存在
	checkCmd := fmt.Sprintf("sudo -u postgres %s/psql -p %d -tAc \"SELECT 1 FROM pg_roles WHERE rolname='replicator'\"",
		pgBinDir, master.Port)
	checkResult := ctx.Executor.RunOnNode(&executor.Node{
		ID:   master.Host,
		Host: master.Host,
		User: ctx.Config.SSHUser,
	}, checkCmd, false, true)

	if checkResult.Error == nil && strings.TrimSpace(checkResult.Output) == "1" {
		// 用户已存在，更新密码
		ctx.Logger.Info("Replication user already exists, updating password",
			logger.Fields{"master": master.Host})
		cmd := fmt.Sprintf("sudo -u postgres %s/psql -p %d -c \"ALTER USER replicator WITH REPLICATION ENCRYPTED PASSWORD '%s';\"",
			pgBinDir, master.Port, password)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   master.Host,
			Host: master.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			return result.Error
		}
	} else {
		// 创建复制用户
		ctx.Logger.Info("Creating replication user",
			logger.Fields{"master": master.Host})
		cmd := fmt.Sprintf("sudo -u postgres %s/psql -p %d -c \"CREATE USER replicator WITH REPLICATION ENCRYPTED PASSWORD '%s';\"",
			pgBinDir, master.Port, password)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   master.Host,
			Host: master.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)
		if result.Error != nil {
			return result.Error
		}
		ctx.Logger.Info("Replication user created",
			logger.Fields{"master": master.Host})
	}

	s.replicationPassword = password
	return nil
}

// configureMasterForReplication 配置主节点
func (s *SetupReplicationStep) configureMasterForReplication(ctx *Context, master *config.NodeConfig) error {
	// 获取所有从节点主机（支持单机多端口和多机场景）
	slaves := s.getSlaveNodes(ctx)
	if len(slaves) == 0 {
		ctx.Logger.Warn("No slave nodes found, skipping replication configuration",
			logger.Fields{"master": master.Host})
		return nil
	}

	// 收集所有唯一的从节点主机IP
	slaveHosts := make(map[string]bool)
	for _, slave := range slaves {
		slaveHosts[slave.Host] = true
	}

	// 为每个唯一的从节点主机添加 pg_hba.conf 规则
	var pgHbaRules []string
	for host := range slaveHosts {
		rule := fmt.Sprintf("host    replication    replicator    %s/32    md5", host)
		pgHbaRules = append(pgHbaRules, rule)
	}
	// 同时添加 localhost 规则（单机场景）
	pgHbaRules = append(pgHbaRules, "host    replication    replicator    127.0.0.1/32    md5")

	// 清理旧的 replicator 规则，然后添加新规则（避免重复累积）
	ctx.Logger.Info("Cleaning old replication rules and adding new ones",
		logger.Fields{"master": master.Host})
	// 使用 sed 删除包含 replicator 的旧行，然后追加新规则
	cleanAndAddCmd := fmt.Sprintf("sed -i '/replicator.*replication/d' %s/pg_hba.conf && echo '%s' >> %s/pg_hba.conf",
		master.DataDir, strings.Join(pgHbaRules, "\n"), master.DataDir)

	result := ctx.Executor.RunOnNode(&executor.Node{
		ID:   master.Host,
		Host: master.Host,
		User: ctx.Config.SSHUser,
	}, cleanAndAddCmd, true, false)

	if result.Error != nil {
		return result.Error
	}

	ctx.Logger.Info("Master configured for replication",
		logger.Fields{
			"master":      master.Host,
			"slave_hosts": len(slaveHosts),
		})

	return nil
}

// reloadMaster 重载主节点配置
func (s *SetupReplicationStep) reloadMaster(ctx *Context, master *config.NodeConfig) error {
	pgBinDir := ctx.Config.PGSoftDir + "/bin"

	cmd := fmt.Sprintf("sudo -u postgres %s/pg_ctl -D %s reload",
		pgBinDir, master.DataDir)

	result := ctx.Executor.RunOnNode(&executor.Node{
		ID:   master.Host,
		Host: master.Host,
		User: ctx.Config.SSHUser,
	}, cmd, true, false)

	return result.Error
}

// setupSlave 设置从节点
func (s *SetupReplicationStep) setupSlave(ctx *Context, master, slave *config.NodeConfig) error {
	pgBinDir := ctx.Config.PGSoftDir + "/bin"

	// 1. 创建 .pgpass 文件用于密码认证
	// 使用 heredoc 避免 shell 对密码特殊字符的解释
	pgpassContent := fmt.Sprintf("%s:%d:replication:replicator:%s",
		master.Host, master.Port, s.replicationPassword)
	pgpassCmd := fmt.Sprintf("mkdir -p /home/postgres && cat > /home/postgres/.pgpass << 'PGPASS_EOF'\n%s\nPGPASS_EOF && chmod 600 /home/postgres/.pgpass && chown postgres:postgres /home/postgres/.pgpass",
		pgpassContent)

	result := ctx.Executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: ctx.Config.SSHUser,
	}, pgpassCmd, true, false)

	if result.Error != nil {
		return fmt.Errorf("failed to create .pgpass file on slave %s: %w", slave.Host, result.Error)
	}

	// 2. 清理旧数据目录并创建新目录（设置正确权限）
	// 注意：PostgreSQL 要求数据目录权限为 0700 或 0750
	ctx.Logger.Info("Preparing slave data directory",
		logger.Fields{"slave": slave.Host, "data_dir": slave.DataDir})
	// 先停止可能运行的进程，删除旧目录，创建新目录并设置权限
	cleanupCmd := fmt.Sprintf("sudo -u postgres %s/pg_ctl -D %s stop -m fast 2>/dev/null || true; "+
		"rm -rf %s && mkdir -p %s && chmod 0700 %s && chown postgres:postgres %s && ls -ld %s",
		pgBinDir, slave.DataDir, slave.DataDir, slave.DataDir, slave.DataDir, slave.DataDir, slave.DataDir)
	result = ctx.Executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: ctx.Config.SSHUser,
	}, cleanupCmd, true, false)
	if result.Error != nil {
		return fmt.Errorf("failed to prepare slave data directory %s: %w", slave.DataDir, result.Error)
	}
	ctx.Logger.Info("Slave data directory prepared with correct permissions",
		logger.Fields{"slave": slave.Host, "data_dir": slave.DataDir, "perm": "0700"})

	// 3. 使用 pg_basebackup 初始化从节点
	ctx.Logger.Info("Running pg_basebackup to initialize slave",
		logger.Fields{"slave": slave.Host, "master": master.Host, "data_dir": slave.DataDir})
	cmd := fmt.Sprintf("sudo -u postgres %s/pg_basebackup -h %s -p %d -D %s -U replicator -P -R -X stream",
		pgBinDir, master.Host, master.Port, slave.DataDir)

	result = ctx.Executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: ctx.Config.SSHUser,
	}, cmd, true, false)

	if result.Error != nil {
		return result.Error
	}

	// 4. pg_basebackup 复制了主库的配置，需要修改从库的端口
	ctx.Logger.Info("Updating slave configuration for correct port",
		logger.Fields{"slave": slave.Host, "port": slave.Port})
	updatePortCmd := fmt.Sprintf("sed -i 's/^port = .*/port = %d/' %s/postgresql.conf && grep '^port' %s/postgresql.conf",
		slave.Port, slave.DataDir, slave.DataDir)
	result = ctx.Executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: ctx.Config.SSHUser,
	}, updatePortCmd, true, false)
	if result.Error != nil {
		ctx.Logger.Warn("Failed to update slave port, attempting to continue",
			logger.Fields{"slave": slave.Host, "error": result.Error})
	}

	// 5. 修复数据目录权限（pg_basebackup 可能创建不正确的权限，PostgreSQL 严格要求 0700）
	ctx.Logger.Info("Fixing data directory permissions after pg_basebackup",
		logger.Fields{"slave": slave.Host, "data_dir": slave.DataDir})
	// 修复权限：根目录 0700，所有文件/子目录归 postgres 用户所有，所有目录 0700
	fixPermCmd := fmt.Sprintf("chmod 0700 %s && chown -R postgres:postgres %s && find %s -type d -exec chmod 0700 {} \\; && ls -la %s | head -5",
		slave.DataDir, slave.DataDir, slave.DataDir, slave.DataDir)
	result = ctx.Executor.RunOnNode(&executor.Node{
		ID:   slave.Host,
		Host: slave.Host,
		User: ctx.Config.SSHUser,
	}, fixPermCmd, true, false)
	if result.Error != nil {
		ctx.Logger.Warn("Permission fix command returned error, attempting recovery",
			logger.Fields{"slave": slave.Host, "error": result.Error})
		// 尝试强制修复
		forceCmd := fmt.Sprintf("chmod -R 0700 %s 2>/dev/null; chown -R postgres:postgres %s 2>/dev/null; ls -ld %s",
			slave.DataDir, slave.DataDir, slave.DataDir)
		ctx.Executor.RunOnNode(&executor.Node{
			ID:   slave.Host,
			Host: slave.Host,
			User: ctx.Config.SSHUser,
		}, forceCmd, true, false)
	}
	ctx.Logger.Info("Data directory permissions fixed",
		logger.Fields{"slave": slave.Host, "data_dir": slave.DataDir})

	// 6. 处理 WAL 目录软链接
	if slave.WALDir != "" {
		rmCmd := fmt.Sprintf("rm -rf %s/pg_wal && ln -s %s %s/pg_wal",
			slave.DataDir, slave.WALDir, slave.DataDir)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   slave.Host,
			Host: slave.Host,
			User: ctx.Config.SSHUser,
		}, rmCmd, true, false)

		if result.Error != nil {
			// WAL 目录链接失败是致命错误，因为从节点无法正常工作
			return fmt.Errorf("failed to link WAL directory on slave %s: %w",
				slave.Host, result.Error)
		}
	}

	ctx.Logger.Info("Slave initialized",
		logger.Fields{
			"slave":  slave.Host,
			"master": master.Host,
		})

	return nil
}

// startSlaves 启动从节点
func (s *SetupReplicationStep) startSlaves(ctx *Context, slaves []*config.NodeConfig) error {
	pgBinDir := ctx.Config.PGSoftDir + "/bin"

	var failedNodes []string
	for _, slave := range slaves {
		// 使用 -l 指定日志文件，-w 等待启动完成但设置超时
		logFile := filepath.Join(slave.DataDir, "pg_start.log")
		if slave.PGLogDir != "" {
			logFile = filepath.Join(slave.PGLogDir, "pg_start.log")
		}
		cmd := fmt.Sprintf("sudo -u postgres %s/pg_ctl -D %s start -l %s",
			pgBinDir, slave.DataDir, logFile)

		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   slave.Host,
			Host: slave.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			failedNodes = append(failedNodes, slave.Host)
			ctx.Logger.Error("Failed to start slave node",
				logger.Fields{
					"slave": slave.Host,
					"error": result.Error,
				})
		} else {
			ctx.Logger.Info("Slave started",
				logger.Fields{"slave": slave.Host})
		}
	}

	// 如果有任何从节点启动失败，返回错误
	if len(failedNodes) > 0 {
		return fmt.Errorf("failed to start %d slave node(s): %v",
			len(failedNodes), failedNodes)
	}

	return nil
}

// IsCompleted 检查复制是否已设置
// 注意：为了支持重新初始化，始终返回 false，确保每次部署都重新设置从节点
func (s *SetupReplicationStep) IsCompleted(ctx *Context) (bool, error) {
	// 重新部署时需要清理从节点数据目录并重新执行 pg_basebackup
	// Execute 方法中已经包含清理逻辑：停止服务、删除数据目录、重新 pg_basebackup
	return false, nil
}

// getSlaveNodes 获取所有从节点
func (s *SetupReplicationStep) getSlaveNodes(ctx *Context) []*config.NodeConfig {
	var slaves []*config.NodeConfig
	for _, group := range ctx.Config.Groups {
		for _, node := range group.Nodes {
			if !node.IsMaster {
				slaves = append(slaves, node)
			}
		}
	}
	return slaves
}

// getMasterNetwork 获取主节点网络段
func getMasterNetwork(ctx *Context) string {
	masters := ctx.Config.GetMasterNodes()
	if len(masters) == 0 {
		return "0.0.0.0"
	}

	// 简单实现：使用主节点IP的前三段
	parts := strings.Split(masters[0].Host, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".") + ".0"
	}

	return masters[0].Host
}

// InstallPatroniStep 安装Patroni步骤
type InstallPatroniStep struct {
	BaseStep
}

// NewInstallPatroniStep 创建安装Patroni步骤
func NewInstallPatroniStep() *InstallPatroniStep {
	return &InstallPatroniStep{
		BaseStep: BaseStep{
			name:        "InstallPatroni",
			description: "Install Patroni, etcd and dependencies",
		},
	}
}

// Execute 执行安装
func (s *InstallPatroniStep) Execute(ctx *Context) error {
	nodes := uniqueNodesByHost(ctx.Config.GetAllNodes(), ctx.Config.SSHUser, ctx.Config.SSHPassword)

	ctx.Logger.Info("Installing Patroni on all nodes",
		logger.Fields{"node_count": len(nodes)})

	cmd := `
set -e
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y python3 python3-pip python3-venv curl
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y python3 python3-pip curl
elif command -v yum >/dev/null 2>&1; then
  yum install -y python3 python3-pip curl
elif command -v zypper >/dev/null 2>&1; then
  zypper --non-interactive install python3 python3-pip curl
elif command -v apk >/dev/null 2>&1; then
  apk add --no-cache python3 py3-pip curl
fi
python3 -m pip install --upgrade pip
python3 -m pip install patroni[etcd] psycopg2-binary
if ! command -v etcd >/dev/null 2>&1 || ! command -v etcdctl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
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
fi
command -v patroni >/dev/null 2>&1
command -v patronictl >/dev/null 2>&1
command -v etcd >/dev/null 2>&1
command -v etcdctl >/dev/null 2>&1
`

	results := ctx.Executor.RunOnNodes(nodes, cmd, true)

	// 检查所有节点的安装结果
	var failedNodes []string
	for _, result := range results {
		if result.Error != nil {
			// 记录失败节点
			failedNodes = append(failedNodes, result.Node.ID)
			ctx.Logger.Error("Failed to install Patroni on node",
				logger.Fields{
					"node":  result.Node.ID,
					"error": result.Error,
				})
		}
	}

	// 如果有任何节点失败，返回错误
	if len(failedNodes) > 0 {
		return fmt.Errorf("failed to install Patroni on %d node(s): %v",
			len(failedNodes), failedNodes)
	}

	return nil
}

// IsCompleted 检查Patroni是否已安装
func (s *InstallPatroniStep) IsCompleted(ctx *Context) (bool, error) {
	cmd := "python3 -c 'import patroni; print(patroni.__version__)' && command -v etcd >/dev/null 2>&1 && command -v etcdctl >/dev/null 2>&1"

	nodes := uniqueConfigNodesByHost(ctx.Config.GetAllNodes())
	for _, node := range nodes {
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// ConfigurePatroniStep 配置Patroni步骤
type ConfigurePatroniStep struct {
	BaseStep
}

// NewConfigurePatroniStep 创建配置Patroni步骤
func NewConfigurePatroniStep() *ConfigurePatroniStep {
	return &ConfigurePatroniStep{
		BaseStep: BaseStep{
			name:        "ConfigurePatroni",
			description: "Generate Patroni YAML configuration",
		},
	}
}

// Execute 执行配置
func (s *ConfigurePatroniStep) Execute(ctx *Context) error {
	mgr := topology.NewPatroniManager(ctx.Config, ctx.Executor, ctx.Logger)
	if err := mgr.DeployEtcdCluster(); err != nil {
		return err
	}

	for _, node := range ctx.Config.GetAllNodes() {
		if err := mgr.ConfigurePatroniNode(node); err != nil {
			return err
		}
	}

	return nil
}

// generatePatroniConfig 生成Patroni配置
func (s *ConfigurePatroniStep) generatePatroniConfig(ctx *Context, node *config.NodeConfig) string {
	mgr := topology.NewPatroniManager(ctx.Config, ctx.Executor, ctx.Logger)
	config, err := mgr.GeneratePatroniConfig(node)
	if err != nil {
		ctx.Logger.Warn("Failed to generate Patroni config",
			logger.Fields{"node": node.Name, "error": err})
		return ""
	}
	return config
}

// IsCompleted 检查配置是否已完成
func (s *ConfigurePatroniStep) IsCompleted(ctx *Context) (bool, error) {
	nodes := ctx.Config.GetAllNodes()

	for _, node := range nodes {
		configFile := fmt.Sprintf("/etc/patroni/%s.yml", node.Name)
		cmd := fmt.Sprintf("test -f %s", configFile)

		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// StartPatroniClusterStep 启动Patroni集群步骤
type StartPatroniClusterStep struct {
	BaseStep
}

// NewStartPatroniClusterStep 创建启动Patroni集群步骤
func NewStartPatroniClusterStep() *StartPatroniClusterStep {
	return &StartPatroniClusterStep{
		BaseStep: BaseStep{
			name:        "StartPatroniCluster",
			description: "Start Patroni cluster services",
		},
	}
}

// Execute 执行启动
func (s *StartPatroniClusterStep) Execute(ctx *Context) error {
	mgr := topology.NewPatroniManager(ctx.Config, ctx.Executor, ctx.Logger)
	return mgr.StartPatroniCluster()
}

// generateSystemdService 生成systemd服务文件
func (s *StartPatroniClusterStep) generateSystemdService(node *config.NodeConfig) string {
	return ""
}

// IsCompleted 检查Patroni是否已启动
func (s *StartPatroniClusterStep) IsCompleted(ctx *Context) (bool, error) {
	nodes := ctx.Config.GetAllNodes()

	for _, node := range nodes {
		cmd := fmt.Sprintf("systemctl is-active patroni-%s", node.Name)

		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, false)

		if result.Error != nil || !strings.Contains(result.Output, "active") {
			return false, nil
		}
	}

	return true, nil
}

// ConfigureCitusStep 配置Citus步骤
type ConfigureCitusStep struct {
	BaseStep
}

// NewConfigureCitusStep 创建配置Citus步骤
func NewConfigureCitusStep() *ConfigureCitusStep {
	return &ConfigureCitusStep{
		BaseStep: BaseStep{
			name:        "ConfigureCitus",
			description: "Configure Citus extension",
		},
	}
}

// Execute 执行配置
func (s *ConfigureCitusStep) Execute(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()

	ctx.Logger.Info("Configuring Citus extension",
		logger.Fields{
			"node_count": len(nodes),
			"mode":       ctx.Config.DeployMode,
		})

	// Citus 需要 coordinator 和 worker 节点
	coordinatorNode := getCoordinatorNode(ctx)
	if coordinatorNode == nil {
		return fmt.Errorf("no coordinator node found for Citus setup")
	}

	// 在所有节点上安装 Citus 扩展
	for _, node := range nodes {
		// 创建 SQL 命令来配置 Citus
		var sqlCommands []string

		// 在 coordinator 上启用 Citus 并创建扩展
		if node == coordinatorNode {
			sqlCommands = append(sqlCommands, "CREATE EXTENSION IF NOT EXISTS citus;")
			sqlCommands = append(sqlCommands, "CREATE EXTENSION IF NOT EXISTS citus_catalog;")

			// 添加所有 worker 节点
			workerNodes := getWorkerNodes(ctx)
			for _, worker := range workerNodes {
				sqlCommands = append(sqlCommands,
					fmt.Sprintf("SELECT * from master_add_node('%s', %d);",
						worker.Host, worker.Port))
			}
		}

		// 执行 SQL 命令
		for _, sql := range sqlCommands {
			cmd := fmt.Sprintf("sudo -u postgres %s/bin/psql -p %d -c \"%s\"",
				ctx.Config.PGSoftDir, node.Port, sql)

			result := ctx.Executor.RunOnNode(&executor.Node{
				ID:   node.Host,
				Host: node.Host,
				User: ctx.Config.SSHUser,
			}, cmd, true, false)

			if result.Error != nil {
				return fmt.Errorf("failed to configure Citus on %s: %w, SQL: %s",
					node.Host, result.Error, sql)
			}
		}

		ctx.Logger.Info("Citus extension configured on node",
			logger.Fields{
				"node": node.Host,
				"role": getNodeRole(ctx, node),
			})
	}

	return nil
}

// IsCompleted 检查Citus是否已配置
func (s *ConfigureCitusStep) IsCompleted(ctx *Context) (bool, error) {
	// 检查 coordinator 节点上的 Citus 扩展
	coordinatorNode := getCoordinatorNode(ctx)
	if coordinatorNode == nil {
		return false, nil
	}

	// 查询 Citus 扩展是否存在
	cmd := fmt.Sprintf("sudo -u postgres %s/bin/psql -p %d -tAc \"SELECT 1 FROM pg_extension WHERE extname='citus'\"",
		ctx.Config.PGSoftDir, coordinatorNode.Port)

	result := ctx.Executor.RunOnNode(&executor.Node{
		ID:   coordinatorNode.Host,
		Host: coordinatorNode.Host,
		User: ctx.Config.SSHUser,
	}, cmd, true, true) // suppress log for expected failures

	if result.Error != nil {
		return false, nil
	}

	// 检查输出是否为 1
	return strings.TrimSpace(result.Output) == "1", nil
}

// getCoordinatorNode 获取 Citus coordinator 节点
func getCoordinatorNode(ctx *Context) *config.NodeConfig {
	// 在 Citus 模式下，coordinator 通常是第一个组的主节点
	if ctx.Config.DeployMode == config.ModeCitus && len(ctx.Config.Groups) > 0 {
		firstGroup := ctx.Config.Groups[0]
		if len(firstGroup.Nodes) > 0 {
			return firstGroup.Nodes[0]
		}
	}
	return nil
}

// getWorkerNodes 获取所有 Citus worker 节点
func getWorkerNodes(ctx *Context) []*config.NodeConfig {
	var workers []*config.NodeConfig

	if ctx.Config.DeployMode == config.ModeCitus && len(ctx.Config.Groups) > 1 {
		// 除了第一个组（coordinator），其他所有组都是 workers
		for i := 1; i < len(ctx.Config.Groups); i++ {
			workers = append(workers, ctx.Config.Groups[i].Nodes...)
		}
	}

	return workers
}

// getNodeRole 获取节点在 Citus 集群中的角色
func getNodeRole(ctx *Context, node *config.NodeConfig) string {
	coordinatorNode := getCoordinatorNode(ctx)
	if coordinatorNode != nil && node == coordinatorNode {
		return "coordinator"
	}

	// 检查是否为 worker 节点
	for _, worker := range getWorkerNodes(ctx) {
		if node == worker {
			return "worker"
		}
	}

	return "unknown"
}

func uniqueNodesByHost(nodes []*config.NodeConfig, user, password string) []*executor.Node {
	result := make([]*executor.Node, 0, len(nodes))
	seenHosts := make(map[string]bool)

	for _, node := range nodes {
		if seenHosts[node.Host] {
			continue
		}
		seenHosts[node.Host] = true
		result = append(result, &executor.Node{
			ID:       node.Host,
			Host:     node.Host,
			Port:     22,
			User:     user,
			Password: password,
		})
	}

	return result
}

func uniqueConfigNodesByHost(nodes []*config.NodeConfig) []*config.NodeConfig {
	result := make([]*config.NodeConfig, 0, len(nodes))
	seenHosts := make(map[string]bool)

	for _, node := range nodes {
		if seenHosts[node.Host] {
			continue
		}
		seenHosts[node.Host] = true
		result = append(result, node)
	}

	return result
}

// generateRandomPassword 生成安全的随机密码
func generateRandomPassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, length)

	// 使用 crypto/rand 生成安全随机数
	for i := range b {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", fmt.Errorf("failed to generate random password: %w", err)
		}
		b[i] = charset[num.Int64()]
	}

	return string(b), nil
}

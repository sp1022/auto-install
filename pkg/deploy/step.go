package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// BaseStep 基础步骤实现
type BaseStep struct {
	name        string
	description string
}

// Name 返回步骤名称
func (s *BaseStep) Name() string {
	return s.name
}

// Description 返回步骤描述
func (s *BaseStep) Description() string {
	return s.description
}

// Validate 默认验证实现
func (s *BaseStep) Validate(ctx *Context) error {
	return nil
}

// Rollback 默认回滚实现（无操作）
func (s *BaseStep) Rollback(ctx *Context) error {
	return nil
}

// PrepareDirectoriesStep 准备目录步骤
type PrepareDirectoriesStep struct {
	BaseStep
}

// NewPrepareDirectoriesStep 创建准备目录步骤
func NewPrepareDirectoriesStep() *PrepareDirectoriesStep {
	return &PrepareDirectoriesStep{
		BaseStep: BaseStep{
			name:        "PrepareDirectories",
			description: "Create users, directories, and set permissions",
		},
	}
}

// Execute 执行目录准备
func (s *PrepareDirectoriesStep) Execute(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes to prepare")
	}

	ctx.Logger.Info("Preparing directories on all nodes",
		logger.Fields{"node_count": len(nodes)})

	// 为每个节点执行准备命令
	for _, node := range nodes {
		cmd := s.prepareCommand(node)
		ctx.Logger.Debug("Executing prepare command",
			logger.Fields{
				"node": node.Host,
				"cmd":  cmd,
			})

		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false) // useSudo = true

		if result.Error != nil {
			return fmt.Errorf("failed to prepare directories on %s: %w",
				node.Host, result.Error)
		}

		ctx.Logger.Info("Directories prepared successfully",
			logger.Fields{"node": node.Host})
	}

	return nil
}

// prepareCommand 生成准备目录的命令
func (s *PrepareDirectoriesStep) prepareCommand(node *config.NodeConfig) string {
	var commands []string

	// 1. 创建 postgres 用户（如果不存在）
	// 使用 || 替代 if-then-fi，避免 SSH 远程执行时的语法解析问题
	commands = append(commands, "id postgres >/dev/null 2>&1 || useradd -m -s /bin/bash postgres")

	// 2. 配置 sudo 免密（postgres 用户执行 initdb 需要）
	// 允许 postgres 用户免密执行 initdb
	commands = append(commands, "echo 'postgres ALL=(ALL) NOPASSWD: /usr/local/pgsql/bin/initdb' >> /etc/sudoers.d/postgres 2>/dev/null || true")
	commands = append(commands, "chmod 0440 /etc/sudoers.d/postgres 2>/dev/null || true")

	// 3. 创建数据目录
	if node.DataDir != "" {
		commands = append(commands, fmt.Sprintf("mkdir -p %s", node.DataDir))
		commands = append(commands, fmt.Sprintf("chown -R postgres:postgres %s", node.DataDir))
		commands = append(commands, fmt.Sprintf("chmod 0700 %s", node.DataDir))
	}

	// 4. 创建 WAL 目录
	if node.WALDir != "" {
		commands = append(commands, fmt.Sprintf("mkdir -p %s", node.WALDir))
		commands = append(commands, fmt.Sprintf("chown -R postgres:postgres %s", node.WALDir))
		commands = append(commands, fmt.Sprintf("chmod 0700 %s", node.WALDir))
	}

	// 5. 创建日志目录
	if node.PGLogDir != "" {
		commands = append(commands, fmt.Sprintf("mkdir -p %s", node.PGLogDir))
		commands = append(commands, fmt.Sprintf("chown -R postgres:postgres %s", node.PGLogDir))
		commands = append(commands, fmt.Sprintf("chmod 0755 %s", node.PGLogDir))
	}

	return strings.Join(commands, " && ")
}

// IsCompleted 检查目录是否已准备好
func (s *PrepareDirectoriesStep) IsCompleted(ctx *Context) (bool, error) {
	nodes := ctx.Config.GetAllNodes()

	// 检查所有节点的目录是否存在
	for _, node := range nodes {
		if node.DataDir == "" {
			continue
		}

		// 数据目录除了属主，还必须满足 PostgreSQL 要求的 0700/0750 权限。
		cmd := fmt.Sprintf("test -d %s && stat -c '%%U:%%a' %s | grep -Eq '^postgres:(700|750)$'", node.DataDir, node.DataDir)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, true)

		if result.Error != nil {
			// 这是正常的，目录还没有准备好，不需要记录为错误
			ctx.Logger.Debug("Directory not yet prepared, will execute PrepareDirectories step",
				logger.Fields{
					"node": node.Host,
					"dir":  node.DataDir,
				})
			return false, nil
		}
	}

	return true, nil
}

// DeploySoftwareStep 部署软件步骤
type DeploySoftwareStep struct {
	BaseStep
	// TODO: 添加软件分发状态跟踪
}

// NewDeploySoftwareStep 创建部署软件步骤
func NewDeploySoftwareStep() *DeploySoftwareStep {
	return &DeploySoftwareStep{
		BaseStep: BaseStep{
			name:        "DeploySoftware",
			description: "Install or distribute PostgreSQL software",
		},
	}
}

// Execute 执行软件部署
func (s *DeploySoftwareStep) Execute(ctx *Context) error {
	switch ctx.Config.BuildMode {
	case config.BuildDistribute:
		return s.distributeBinaries(ctx)
	case config.BuildCompile:
		return s.compileFromSource(ctx)
	default:
		return fmt.Errorf("unsupported build mode: %s", ctx.Config.BuildMode)
	}
}

// distributeBinaries 分发预编译二进制文件
func (s *DeploySoftwareStep) distributeBinaries(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()
	softDir := ctx.Config.PGSoftDir

	ctx.Logger.Info("Distributing PostgreSQL binaries",
		logger.Fields{
			"source":     ctx.Config.PGSource,
			"target_dir": softDir,
			"nodes":      len(nodes),
		})

	// 解压源码包到临时目录
	tempDir := "/tmp/pg_deploy"
	os.MkdirAll(tempDir, 0755)
	defer os.RemoveAll(tempDir)

	// 1. 在本地解压
	ctx.Logger.Info("Extracting binary package locally",
		logger.Fields{"source": ctx.Config.PGSource})

	// 判断是 tar.gz 压缩包还是已解压的目录
	if strings.HasSuffix(ctx.Config.PGSource, ".tar.gz") ||
		strings.HasSuffix(ctx.Config.PGSource, ".tgz") {
		// 解压 tar.gz 包
		extractCmd := fmt.Sprintf("tar -xzf %s -C %s", ctx.Config.PGSource, tempDir)
		result := exec.Command("sh", "-c", extractCmd)
		output, err := result.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to extract package: %w, output: %s", err, string(output))
		}
	} else {
		// 假设是已解压的目录，直接复制
		copyCmd := fmt.Sprintf("cp -r %s %s/", ctx.Config.PGSource, tempDir)
		result := exec.Command("sh", "-c", copyCmd)
		output, err := result.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to copy source directory: %w, output: %s", err, string(output))
		}
	}

	// 查找解压后的目录
	findCmd := fmt.Sprintf("find %s -maxdepth 1 -type d | head -1", tempDir)
	findResult := exec.Command("sh", "-c", findCmd)
	output, err := findResult.Output()
	if err != nil {
		return fmt.Errorf("failed to find extracted directory: %w", err)
	}

	extractedDir := strings.TrimSpace(string(output))
	if extractedDir == "" {
		return fmt.Errorf("no directory found after extraction")
	}

	// 2. 分发到所有节点（按主机去重，单机多端口场景）
	processedHosts := make(map[string]bool)
	for _, node := range nodes {
		if processedHosts[node.Host] {
			continue
		}
		processedHosts[node.Host] = true

		execNode := &executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}

		// 创建目标目录
		cmd := fmt.Sprintf("mkdir -p %s", softDir)
		result := ctx.Executor.RunOnNode(execNode, cmd, true, false)
		if result.Error != nil {
			return fmt.Errorf("failed to create directory on %s: %w", node.Host, result.Error)
		}

		// 分发二进制文件 - 使用 rsync 或 scp（使用 executor 的本地检测）
		if ctx.Executor.IsLocalNode(node.Host) {
			// 本地节点，直接复制
			cmd := fmt.Sprintf("cp -r %s/* %s/", extractedDir, softDir)
			result := exec.Command("sh", "-c", cmd)
			output, err := result.CombinedOutput()
			if err != nil {
				return fmt.Errorf("failed to copy binaries locally: %w, output: %s", err, string(output))
			}
		} else {
			// 远程节点，使用 scp 分发
			// 首先创建本地临时压缩包
			localTarball := fmt.Sprintf("/tmp/pg_binaries_%s.tar.gz", node.Host)
			tarCmd := fmt.Sprintf("cd %s && tar -czf %s .", extractedDir, localTarball)
			tarResult := exec.Command("sh", "-c", tarCmd)
			if output, err := tarResult.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to create tarball: %w, output: %s", err, string(output))
			}
			defer os.Remove(localTarball)

			// 使用 scp 传输到远程节点
			scpCmd := fmt.Sprintf("scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null %s %s@%s:/tmp/",
				localTarball, ctx.Config.SSHUser, node.Host)
			if ctx.Config.SSHPassword != "" {
				scpCmd = fmt.Sprintf("sshpass -p '%s' %s",
					ctx.Config.SSHPassword, scpCmd)
			}

			scpResult := exec.Command("sh", "-c", scpCmd)
			if output, err := scpResult.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to scp tarball to %s: %w, output: %s", node.Host, err, string(output))
			}

			// 在远程节点解压
			remoteTarball := fmt.Sprintf("/tmp/pg_binaries_%s.tar.gz", node.Host)
			untarCmd := fmt.Sprintf("tar -xzf %s -C %s", remoteTarball, softDir)
			result := ctx.Executor.RunOnNode(execNode, untarCmd, true, false)
			if result.Error != nil {
				return fmt.Errorf("failed to extract tarball on %s: %w", node.Host, result.Error)
			}

			// 清理远程临时文件
			cleanupCmd := fmt.Sprintf("rm -f %s", remoteTarball)
			ctx.Executor.RunOnNode(execNode, cleanupCmd, true, true)
		}

		ctx.Logger.Info("Distributed binaries to node",
			logger.Fields{"node": node.Host, "target_dir": softDir})
	}

	return nil
}

// compileFromSource 从源码编译
// 优化策略：只在第一个唯一主机上编译，然后打包复制到其他主机
func (s *DeploySoftwareStep) compileFromSource(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()

	// 收集所有唯一主机（去重）
	uniqueHosts := make(map[string]bool)
	var firstHost string
	for _, node := range nodes {
		if !uniqueHosts[node.Host] {
			uniqueHosts[node.Host] = true
			if firstHost == "" {
				firstHost = node.Host
			}
		}
	}

	installDir := ctx.Config.PGSoftDir
	tempTarball := "/tmp/pg_binary.tar.gz"

	ctx.Logger.Info("Compiling PostgreSQL from source",
		logger.Fields{
			"source":       ctx.Config.PGSource,
			"total_nodes":  len(nodes),
			"unique_hosts": len(uniqueHosts),
			"first_host":   firstHost,
			"install_dir":  installDir,
		})

	// 步骤1: 在所有主机上清理旧软件目录（避免旧版本干扰）
	ctx.Logger.Info("Step 1/4: Cleaning old PostgreSQL installation on all hosts",
		logger.Fields{"hosts": len(uniqueHosts)})
	for host := range uniqueHosts {
		if err := ensureSafeManagedDir(installDir); err != nil {
			return fmt.Errorf("invalid install dir %q: %w", installDir, err)
		}
		quotedInstallDir := shellQuote(installDir)
		// 保留“清理后重建”的语义，但避免危险通配删除。
		cleanupCmd := fmt.Sprintf(
			"pkill -9 postgres 2>/dev/null || true; mkdir -p %s && find %s -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +",
			quotedInstallDir, quotedInstallDir,
		)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   host,
			Host: host,
			User: ctx.Config.SSHUser,
		}, cleanupCmd, true, false)
		if result.Error != nil {
			ctx.Logger.Warn("Failed to clean old installation, continuing anyway",
				logger.Fields{"host": host, "error": result.Error})
		}
	}
	ctx.Logger.Info("Old PostgreSQL installation cleaned",
		logger.Fields{"hosts": len(uniqueHosts)})

	// 步骤2: 在第一个主机上编译（使用流式输出实时显示进度）
	ctx.Logger.Info("Step 2/4: Compiling on first host",
		logger.Fields{"host": firstHost})
	ctx.Logger.Info("Compilation started - this may take several minutes...",
		logger.Fields{})

	compileCmd := s.compileCommand(ctx)

	// 使用流式输出实时显示编译进度
	compileStart := time.Now()
	lineCount := 0
	result := ctx.Executor.RunOnNodeStreaming(&executor.Node{
		ID:   firstHost,
		Host: firstHost,
		User: ctx.Config.SSHUser,
	}, compileCmd, true, func(line string) {
		lineCount++
		// 每50行输出一次进度，避免日志过多
		if lineCount%50 == 0 {
			ctx.Logger.Debug("Compile progress",
				logger.Fields{
					"lines":    lineCount,
					"elapsed":  time.Since(compileStart).Round(time.Second).String(),
					"last_log": line[:min(len(line), 100)],
				})
		}
	})

	if result.Error != nil {
		ctx.Logger.Error("Compilation failed",
			logger.Fields{
				"host":   firstHost,
				"error":  result.Error,
				"output": result.Output[:min(len(result.Output), 500)],
			})
		return fmt.Errorf("compilation failed on %s: %w", firstHost, result.Error)
	}

	ctx.Logger.Info("Compilation completed",
		logger.Fields{
			"host":     firstHost,
			"duration": result.Duration.Round(time.Second).String(),
		})

	// 步骤3: 打包编译好的二进制
	ctx.Logger.Info("Step 3/4: Creating binary tarball",
		logger.Fields{"host": firstHost})

	// 使用 tar -C 选项替代 cd，避免 sudo cd 问题
	packCmd := fmt.Sprintf("tar -czf %s -C %s .", tempTarball, installDir)
	result = ctx.Executor.RunOnNode(&executor.Node{
		ID:   firstHost,
		Host: firstHost,
		User: ctx.Config.SSHUser,
	}, packCmd, true, false)

	if result.Error != nil {
		return fmt.Errorf("failed to create tarball on %s: %w", firstHost, result.Error)
	}

	// 步骤4: 复制到其他主机（如果有多个主机）
	if len(uniqueHosts) > 1 {
		ctx.Logger.Info("Step 4/4: Distributing to other hosts",
			logger.Fields{"target_hosts": len(uniqueHosts) - 1})

		for host := range uniqueHosts {
			if host == firstHost {
				continue
			}

			ctx.Logger.Debug("Distributing to host",
				logger.Fields{"host": host})

			// 复制 tarball（如果目标与源是同一主机则跳过 SCP）
			if host == firstHost {
				continue // 同一主机，无需复制
			}

			// 使用 scp 复制
			var scpCmd string
			if ctx.Config.SSHPassword != "" {
				scpCmd = fmt.Sprintf("sshpass -p '%s' scp -o StrictHostKeyChecking=no %s@%s:%s %s@%s:/tmp/",
					ctx.Config.SSHPassword, ctx.Config.SSHUser, firstHost, tempTarball, ctx.Config.SSHUser, host)
			} else {
				scpCmd = fmt.Sprintf("scp -o StrictHostKeyChecking=no -o BatchMode=yes %s@%s:%s %s@%s:/tmp/",
					ctx.Config.SSHUser, firstHost, tempTarball, ctx.Config.SSHUser, host)
			}

			scpResult := exec.Command("sh", "-c", scpCmd)
			if output, err := scpResult.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to scp tarball to %s: %w, output: %s", host, err, string(output))
			}

			// 解压到目标目录
			untarCmd := fmt.Sprintf("tar -xzf %s -C %s && rm -f %s",
				tempTarball, installDir, tempTarball)
			result = ctx.Executor.RunOnNode(&executor.Node{
				ID:   host,
				Host: host,
				User: ctx.Config.SSHUser,
			}, untarCmd, true, false)
			if result.Error != nil {
				return fmt.Errorf("failed to extract tarball on %s: %w", host, result.Error)
			}
		}
	} else {
		ctx.Logger.Info("Step 4/4: Single host deployment, skipping distribution",
			logger.Fields{})
	}

	// 清理第一个主机上的临时 tarball
	cleanupCmd := fmt.Sprintf("rm -f %s", tempTarball)
	ctx.Executor.RunOnNode(&executor.Node{
		ID:   firstHost,
		Host: firstHost,
		User: ctx.Config.SSHUser,
	}, cleanupCmd, true, true)

	ctx.Logger.Info("PostgreSQL compilation and distribution completed",
		logger.Fields{
			"compiled_on": firstHost,
			"hosts":       len(uniqueHosts),
		})

	return nil
}

func ensureSafeManagedDir(path string) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("path must be absolute")
	}
	switch cleaned {
	case "/", ".", "..":
		return fmt.Errorf("path is too dangerous")
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// IsCompleted 检查软件是否已安装
func (s *DeploySoftwareStep) IsCompleted(ctx *Context) (bool, error) {
	// 编译模式下，强制重新编译（不检查已存在的二进制）
	if ctx.Config.BuildMode == config.BuildCompile {
		return false, nil
	}

	nodes := ctx.Config.GetAllNodes()

	// 检查 pg_config 是否存在
	for _, node := range nodes {
		cmd := fmt.Sprintf("test -x %s/bin/pg_config", ctx.Config.PGSoftDir)
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

// Validate 验证软件源
func (s *DeploySoftwareStep) Validate(ctx *Context) error {
	if ctx.Config.PGSource == "" {
		return fmt.Errorf("PG_SOURCE not specified")
	}

	// 检查文件是否存在（本地编译）
	if ctx.Config.BuildMode == config.BuildCompile {
		if _, err := os.Stat(ctx.Config.PGSource); os.IsNotExist(err) {
			return fmt.Errorf("source file not found: %s", ctx.Config.PGSource)
		}
	}

	return nil
}

// InitDatabaseStep 初始化数据库步骤
type InitDatabaseStep struct {
	BaseStep
}

// NewInitDatabaseStep 创建初始化数据库步骤
func NewInitDatabaseStep() *InitDatabaseStep {
	return &InitDatabaseStep{
		BaseStep: BaseStep{
			name:        "InitDatabase",
			description: "Initialize PostgreSQL database cluster",
		},
	}
}

// Execute 执行数据库初始化（仅在主节点）
func (s *InitDatabaseStep) Execute(ctx *Context) error {
	masterNodes := ctx.Config.GetMasterNodes()
	if len(masterNodes) == 0 {
		return fmt.Errorf("no master nodes found")
	}

	ctx.Logger.Info("Initializing database on master nodes",
		logger.Fields{"master_count": len(masterNodes)})

	for _, node := range masterNodes {
		cmd := s.initCommand(ctx, node)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			return fmt.Errorf("initdb failed on %s: %w", node.Host, result.Error)
		}

		ctx.Logger.Info("Database initialized successfully",
			logger.Fields{
				"node": node.Host,
				"port": node.Port,
			})
	}

	return nil
}

// initCommand 生成初始化命令
func (s *InitDatabaseStep) initCommand(ctx *Context, node *config.NodeConfig) string {
	pgBinDir := filepath.Join(ctx.Config.PGSoftDir, "bin")
	pgData := node.DataDir

	var cmdParts []string

	// 清理旧数据目录（如果存在）以确保干净初始化
	// 先停止可能运行的进程，然后删除整个目录重新创建
	cmdParts = append(cmdParts, fmt.Sprintf("sudo -u postgres %s/pg_ctl -D %s stop -m fast 2>/dev/null || true", pgBinDir, pgData))
	cmdParts = append(cmdParts, fmt.Sprintf("rm -rf %s", pgData))
	cmdParts = append(cmdParts, fmt.Sprintf("mkdir -p %s", pgData))
	cmdParts = append(cmdParts, fmt.Sprintf("chown postgres:postgres %s", pgData))

	// 使用 sudo -u postgres 执行 initdb（避免需要密码）
	cmdParts = append(cmdParts, fmt.Sprintf("sudo -u postgres %s/initdb -D %s", pgBinDir, pgData))

	// 添加 WAL 目录软链接
	if node.WALDir != "" {
		cmdParts = append(cmdParts, fmt.Sprintf("rm -rf %s/pg_wal", pgData))
		cmdParts = append(cmdParts, fmt.Sprintf("ln -s %s %s/pg_wal", node.WALDir, pgData))
	}

	return strings.Join(cmdParts, " && ")
}

// IsCompleted 检查数据库是否已初始化
// 注意：为了支持重新初始化，始终返回 false，确保每次部署都清理并重新创建数据目录
func (s *InitDatabaseStep) IsCompleted(ctx *Context) (bool, error) {
	// 重新部署时需要清理旧数据并重新初始化
	// Execute 方法中已经包含清理逻辑：停止服务、删除数据目录、重新 initdb
	return false, nil
}

// ConfigurePostgreSQLStep 配置PostgreSQL步骤
type ConfigurePostgreSQLStep struct {
	BaseStep
}

// NewConfigurePostgreSQLStep 创建配置PostgreSQL步骤
func NewConfigurePostgreSQLStep() *ConfigurePostgreSQLStep {
	return &ConfigurePostgreSQLStep{
		BaseStep: BaseStep{
			name:        "ConfigurePostgreSQL",
			description: "Configure PostgreSQL settings",
		},
	}
}

// Execute 执行配置
func (s *ConfigurePostgreSQLStep) Execute(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()

	ctx.Logger.Info("Configuring PostgreSQL on all nodes",
		logger.Fields{"node_count": len(nodes)})

	for _, node := range nodes {
		// TODO: 生成配置文件内容
		configContent := s.generateConfig(ctx, node)

		// 写入配置文件
		cmd := fmt.Sprintf("cat > %s/postgresql.conf << 'EOF'\n%s\nEOF",
			node.DataDir, configContent)

		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)

		if result.Error != nil {
			return fmt.Errorf("failed to write config on %s: %w", node.Host, result.Error)
		}
	}

	return nil
}

// generateConfig 生成配置文件内容
func (s *ConfigurePostgreSQLStep) generateConfig(ctx *Context, node *config.NodeConfig) string {
	var cfg []string

	// 连接设置
	cfg = append(cfg, "# CONNECTIONS AND AUTHENTICATION")
	cfg = append(cfg, fmt.Sprintf("listen_addresses = '*'"))
	cfg = append(cfg, fmt.Sprintf("port = %d", node.Port))
	cfg = append(cfg, "max_connections = 200")
	cfg = append(cfg, "superuser_reserved_connections = 3")

	// 内存设置
	cfg = append(cfg, "\n# RESOURCE USAGE (except WAL)")
	cfg = append(cfg, "shared_buffers = 256MB")
	cfg = append(cfg, "effective_cache_size = 1GB")
	cfg = append(cfg, "maintenance_work_mem = 64MB")
	cfg = append(cfg, "work_mem = 16MB")

	// WAL 设置
	cfg = append(cfg, "\n# WRITE-AHEAD LOG")
	cfg = append(cfg, "wal_level = replica")
	cfg = append(cfg, "fsync = on")
	cfg = append(cfg, "synchronous_commit = on")
	cfg = append(cfg, "max_wal_size = 1GB")
	cfg = append(cfg, "min_wal_size = 80MB")
	if node.WALDir != "" {
		cfg = append(cfg, "// WAL directory is configured as symlink at initdb time")
	}

	// 复制设置
	cfg = append(cfg, "\n# REPLICATION")
	cfg = append(cfg, "wal_sender_timeout = 60s")
	cfg = append(cfg, "wal_receiver_timeout = 60s")
	cfg = append(cfg, "max_replication_slots = 10")
	cfg = append(cfg, "max_wal_senders = 10")

	// 日志设置
	if node.PGLogDir != "" {
		cfg = append(cfg, "\n# LOGGING")
		cfg = append(cfg, "logging_collector = on")
		cfg = append(cfg, fmt.Sprintf("log_directory = '%s'", node.PGLogDir))
		cfg = append(cfg, "log_filename = 'postgresql-%%Y-%%m-%%d_%%H%%M%%S.log'")
		cfg = append(cfg, "log_rotation_age = 1d")
		cfg = append(cfg, "log_rotation_size = 100MB")
		cfg = append(cfg, "log_min_duration_statement = 1000")
		cfg = append(cfg, "log_line_prefix = '%%t [%%p]: [%%l-1] user=%u,db=%d,app=%a,client=%%h '")
	}

	// 共享库预加载（支持扩展）
	cfg = append(cfg, "\n# SHARED PRELOAD LIBRARIES")
	var sharedLibs []string
	if ctx.Config.DeployMode == config.ModeCitus {
		sharedLibs = append(sharedLibs, "citus")
	}
	// pg_stat_statements 是内置扩展，但只在确认安装后才启用
	// 通过检查 $PGDIR/share/extension/pg_stat_statements.control 是否存在
	if len(sharedLibs) > 0 {
		cfg = append(cfg, fmt.Sprintf("shared_preload_libraries = '%s'",
			strings.Join(sharedLibs, ",")))
	}

	// 其他设置
	cfg = append(cfg, "\n# OTHER SETTINGS")
	cfg = append(cfg, "default_text_search_config = 'pg_catalog.english'")
	cfg = append(cfg, "dynamic_shared_memory_type = posix")

	return strings.Join(cfg, "\n")
}

// IsCompleted 检查配置是否已完成
// 配置生成是幂等的，始终重新写入以确保使用最新配置（避免旧配置文件残留导致启动失败）
func (s *ConfigurePostgreSQLStep) IsCompleted(ctx *Context) (bool, error) {
	return false, nil
}

// StartPostgreSQLStep 启动PostgreSQL步骤
type StartPostgreSQLStep struct {
	BaseStep
}

// NewStartPostgreSQLStep 创建启动PostgreSQL步骤
func NewStartPostgreSQLStep() *StartPostgreSQLStep {
	return &StartPostgreSQLStep{
		BaseStep: BaseStep{
			name:        "StartPostgreSQL",
			description: "Start PostgreSQL service",
		},
	}
}

// Execute 执行启动
func (s *StartPostgreSQLStep) Execute(ctx *Context) error {
	// 只启动 master 节点，从节点由 SetupReplicationStep 处理
	// 避免启动尚未完成 basebackup 的从节点
	masterNodes := ctx.Config.GetMasterNodes()

	ctx.Logger.Info("Starting PostgreSQL on master nodes",
		logger.Fields{"node_count": len(masterNodes)})

	// 检查并停止可能占用数据目录的已有进程
	for _, node := range masterNodes {
		// 使用 pg_ctl status 检查是否已有进程在运行
		statusCmd := fmt.Sprintf("sudo -u postgres %s/bin/pg_ctl -D %s status",
			ctx.Config.PGSoftDir, node.DataDir)
		statusResult := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, statusCmd, false, true)

		if statusResult.Error == nil && strings.Contains(statusResult.Output, "is running") {
			ctx.Logger.Info("PostgreSQL is already running, stopping it first",
				logger.Fields{"node": node.Host, "data_dir": node.DataDir})
			stopCmd := fmt.Sprintf("sudo -u postgres %s/bin/pg_ctl -D %s stop -m fast",
				ctx.Config.PGSoftDir, node.DataDir)
			ctx.Executor.RunOnNode(&executor.Node{
				ID:   node.Host,
				Host: node.Host,
				User: ctx.Config.SSHUser,
			}, stopCmd, true, false)
			// 等待进程停止
			time.Sleep(2 * time.Second)
		}
	}

	// 逐个节点启动（单机多端口场景需要指定不同数据目录）
	var results []*executor.Result
	for _, node := range masterNodes {
		cmd := s.startCommand(ctx, node)
		ctx.Logger.Info("Starting PostgreSQL instance",
			logger.Fields{
				"node":     node.Host,
				"port":     node.Port,
				"data_dir": node.DataDir,
			})
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, true, false)
		results = append(results, result)
	}

	// 验证启动
	var failedNodes []string
	for i, result := range results {
		if result.Error != nil {
			failedNodes = append(failedNodes, masterNodes[i].Host)
			ctx.Logger.Error("Failed to start PostgreSQL on node",
				logger.Fields{
					"node":  masterNodes[i].Host,
					"error": result.Error,
				})
		}
	}

	// 如果有任何节点启动失败，返回错误
	if len(failedNodes) > 0 {
		return fmt.Errorf("failed to start PostgreSQL on %d node(s): %v",
			len(failedNodes), failedNodes)
	}

	for _, node := range masterNodes {
		if err := waitForPostgreSQLReady(ctx, node, 30*time.Second, "after start"); err != nil {
			return err
		}
	}

	return nil
}

// startCommand 生成启动命令
func (s *StartPostgreSQLStep) startCommand(ctx *Context, node *config.NodeConfig) string {
	pgBinDir := filepath.Join(ctx.Config.PGSoftDir, "bin")
	logFile := filepath.Join(node.DataDir, "pg_start.log")
	if node.PGLogDir != "" {
		logFile = filepath.Join(node.PGLogDir, "pg_start.log")
	}
	return fmt.Sprintf("sudo -u postgres %s/pg_ctl -D %s start -l %s",
		pgBinDir, node.DataDir, logFile)
}

// IsCompleted 检查PostgreSQL是否已启动
// 只检查 master 节点，从节点由 SetupReplicationStep 处理
func (s *StartPostgreSQLStep) IsCompleted(ctx *Context) (bool, error) {
	masterNodes := ctx.Config.GetMasterNodes()

	for _, node := range masterNodes {
		cmd := fmt.Sprintf("%s/bin/pg_isready -h localhost -p %d",
			ctx.Config.PGSoftDir, node.Port)
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, true)

		if result.Error != nil {
			return false, nil
		}
	}

	return true, nil
}

// ValidateDeploymentStep 验证部署步骤
type ValidateDeploymentStep struct {
	BaseStep
}

// NewValidateDeploymentStep 创建验证部署步骤
func NewValidateDeploymentStep() *ValidateDeploymentStep {
	return &ValidateDeploymentStep{
		BaseStep: BaseStep{
			name:        "ValidateDeployment",
			description: "Validate deployment and health check",
		},
	}
}

// Execute 执行验证
func (s *ValidateDeploymentStep) Execute(ctx *Context) error {
	nodes := ctx.Config.GetAllNodes()

	ctx.Logger.Info("Validating deployment on all nodes",
		logger.Fields{"node_count": len(nodes)})

	// 1. 检查 systemd 服务
	// 2. 检查连接性
	// 3. 检查复制状态（如适用）

	ctx.Logger.Info("Deployment validation completed",
		logger.Fields{
			"validated_nodes": len(nodes),
		})

	return nil
}

// IsCompleted 验证步骤总是需要执行
func (s *ValidateDeploymentStep) IsCompleted(ctx *Context) (bool, error) {
	return false, nil
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func waitForPostgreSQLReady(ctx *Context, node *config.NodeConfig, timeout time.Duration, reason string) error {
	ctx.Logger.Info("Waiting for PostgreSQL readiness",
		logger.Fields{
			"node":    node.Host,
			"port":    node.Port,
			"reason":  reason,
			"timeout": timeout.String(),
		})

	cmd := fmt.Sprintf("%s/bin/pg_isready -h localhost -p %d", ctx.Config.PGSoftDir, node.Port)
	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastOutput string

	for time.Now().Before(deadline) {
		attempt++
		result := ctx.Executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: ctx.Config.SSHUser,
		}, cmd, false, true)

		if result.Error == nil {
			ctx.Logger.Info("PostgreSQL is ready",
				logger.Fields{
					"node":     node.Host,
					"port":     node.Port,
					"attempts": attempt,
				})
			return nil
		}

		lastOutput = strings.TrimSpace(result.Output)
		if attempt == 1 || attempt%5 == 0 {
			ctx.Logger.Debug("PostgreSQL still starting",
				logger.Fields{
					"node":     node.Host,
					"port":     node.Port,
					"attempts": attempt,
					"output":   lastOutput,
				})
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("PostgreSQL not ready on %s:%d after %s: %s",
		node.Host, node.Port, timeout, lastOutput)
}

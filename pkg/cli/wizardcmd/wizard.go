// Package wizardcmd 提供 wizard 子命令（交互式配置向导）
package wizardcmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/spf13/cobra"
)

// NewCommand 创建 wizard 命令
func NewCommand(log *logger.Logger) *cobra.Command {
	var outputFile string

	cmd := &cobra.Command{
		Use:   "wizard",
		Short: "交互式配置向导",
		Long: `通过交互式向导收集部署配置：
  - 部署模式选择
  - SSH 配置
  - 节点配置
  - 保存配置文件`,
		Example: `  # 启动向导
  pg-deploy wizard

  # 保存到指定文件
  pg-deploy wizard -o my-config.conf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			outputFile, _ = cmd.Flags().GetString("output")

			if outputFile == "" {
				outputFile = "deploy.conf"
			}

			fmt.Println("\n🧙 PostgreSQL 部署配置向导")
			fmt.Println("================================")
			fmt.Println()

			// 收集配置
			cfg, err := runWizard(log)
			if err != nil {
				return fmt.Errorf("配置收集失败: %w", err)
			}

			// 保存配置
			if err := cfg.Save(outputFile); err != nil {
				return fmt.Errorf("保存配置失败: %w", err)
			}

			fmt.Printf("\n✅ 配置已保存到: %s\n", outputFile)
			fmt.Println("\n下一步:")
			fmt.Println("  0. 如需密码认证，请先导出环境变量: export SSH_PASSWORD='your-password'")
			fmt.Printf("  1. 检查配置: cat %s\n", outputFile)
			fmt.Printf("  2. 验证连接: pg-deploy validate -c %s\n", outputFile)
			fmt.Printf("  3. 执行部署: pg-deploy deploy -c %s\n", outputFile)

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "输出配置文件路径")

	return cmd
}

// runWizard 运行配置向导
func runWizard(log *logger.Logger) (*config.Config, error) {
	reader := bufio.NewReader(os.Stdin)

	cfg := &config.Config{}

	// 1. SSH 配置
	fmt.Println("📝 步骤 1/4: SSH 配置")
	fmt.Println("----------------------------------------")
	cfg.SSHUser = prompt(reader, "SSH 用户名", "root")
	cfg.SSHPassword = prompt(reader, "SSH 密码", "")

	fmt.Println("\n📝 环境命名")
	fmt.Println("----------------------------------------")
	cfg.EnvironmentName = prompt(reader, "环境名 (可选，如: pg15-dev)", "")
	if cfg.EnvironmentName != "" {
		cfg.EnvironmentPrefix = prompt(reader, "环境前缀 (直接回车使用环境名)", cfg.EnvironmentName)
	}

	// 2. 部署模式
	fmt.Println("\n📝 步骤 2/4: 部署模式")
	fmt.Println("----------------------------------------")
	fmt.Println("可用的部署模式:")
	fmt.Println("  1. standalone  - 单机模式")
	fmt.Println("  2. master-slave - 主从模式")
	fmt.Println("  3. patroni     - Patroni 高可用")
	fmt.Println("  4. citus       - Citus 分布式")

	modeChoice := prompt(reader, "选择部署模式 (1-4)", "1")
	switch modeChoice {
	case "1":
		cfg.DeployMode = config.ModeStandalone
	case "2":
		cfg.DeployMode = config.ModeMasterSlave
	case "3":
		cfg.DeployMode = config.ModePatroni
	case "4":
		cfg.DeployMode = config.ModeCitus
	default:
		return nil, fmt.Errorf("无效的选择")
	}

	// 3. 构建模式
	fmt.Println("\n📝 步骤 3/4: 构建模式")
	fmt.Println("----------------------------------------")
	fmt.Println("可用的构建模式:")
	fmt.Println("  1. compile    - 从源码编译")
	fmt.Println("  2. distribute - 分发预编译二进制")

	buildChoice := prompt(reader, "选择构建模式 (1-2)", "2")
	switch buildChoice {
	case "1":
		cfg.BuildMode = config.BuildCompile
		cfg.PGSource = prompt(reader, "PostgreSQL 源码包路径", "/path/to/postgresql-14.0.tar.gz")
		fmt.Println("\n  configure 选项 (可选):")
		fmt.Println("    默认: --with-openssl --enable-thread-safety")
		fmt.Println("    常用选项: --with-openssl --with-pam --with-perl --with-python --enable-thread-safety")
		cfg.PGConfigureOpts = prompt(reader, "  configure 选项 (直接回车使用默认)", "")
	case "2":
		cfg.BuildMode = config.BuildDistribute
		cfg.PGSource = prompt(reader, "PostgreSQL 二进制包路径", "/path/to/pgsql.tar.gz")
	default:
		return nil, fmt.Errorf("无效的选择")
	}

	defaultSoftDir := "/usr/local/pgsql"
	if cfg.EnvironmentName != "" {
		defaultSoftDir = "/usr/local/{env}/pgsql"
	}
	cfg.PGSoftDir = prompt(reader, "PostgreSQL 安装目录", defaultSoftDir)

	// 4. 扩展配置
	fmt.Println("\n📝 步骤 4/4: 扩展配置")
	fmt.Println("----------------------------------------")
	extensions := prompt(reader, "扩展 (逗号分隔, 如: citus,pg_stat_statements)", "")
	if extensions != "" {
		cfg.Extensions = strings.Split(extensions, ",")
		for i := range cfg.Extensions {
			cfg.Extensions[i] = strings.TrimSpace(cfg.Extensions[i])
		}
	}

	// 5. 节点配置
	fmt.Println("\n📝 节点配置")
	fmt.Println("----------------------------------------")

	// 添加第一组
	if err := addGroup(reader, cfg, 0); err != nil {
		return nil, err
	}

	// 询问是否添加更多组
	for {
		answer := prompt(reader, "\n是否添加更多组? (y/n)", "n")
		if strings.ToLower(answer) != "y" {
			break
		}

		groupNum := len(cfg.Groups)
		if err := addGroup(reader, cfg, groupNum); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// addGroup 添加节点组
func addGroup(reader *bufio.Reader, cfg *config.Config, groupID int) error {
	fmt.Printf("\n=== 配置组 %d ===\n", groupID)

	// 组角色
	var role string
	switch cfg.DeployMode {
	case config.ModePatroni:
		role = prompt(reader, "组角色 (coordinator)", "coordinator")
	case config.ModeCitus:
		answer := prompt(reader, "组角色 (coordinator/worker)", "coordinator")
		if answer != "coordinator" && answer != "worker" {
			return fmt.Errorf("无效的角色")
		}
		role = answer
	default:
		role = "primary"
	}

	groupName := prompt(reader, "组名称", fmt.Sprintf("pg%d", groupID))

	// 节点数量
	nodeCountStr := prompt(reader, "节点数量", "1")
	nodeCount, err := strconv.Atoi(nodeCountStr)
	if err != nil {
		return fmt.Errorf("无效的节点数量: %w", err)
	}

	group := &config.GroupConfig{
		ID:    groupID,
		Name:  groupName,
		Role:  role,
		Nodes: make([]*config.NodeConfig, 0, nodeCount),
	}

	// 添加节点
	for i := 0; i < nodeCount; i++ {
		fmt.Printf("\n--- 节点 %d ---\n", i)

		host := prompt(reader, "主机地址", fmt.Sprintf("192.168.1.%d", 10+i))
		portStr := prompt(reader, "端口", "5432")
		port, _ := strconv.Atoi(portStr)

		defaultDataDir := "/data/pgdata"
		defaultPGLogDir := ""
		if cfg.EnvironmentName != "" {
			defaultDataDir = fmt.Sprintf("/data/{env}/%s%d", groupName, i)
			defaultPGLogDir = fmt.Sprintf("/log/{env}/%s%d", groupName, i)
		}

		dataDir := prompt(reader, "数据目录", defaultDataDir)
		walDir := prompt(reader, "WAL 目录 (可选)", "")
		pglogDir := prompt(reader, "日志目录 (可选)", defaultPGLogDir)

		isMasterStr := prompt(reader, "是否为主节点? (y/n)", "y")
		isMaster := strings.ToLower(isMasterStr) == "y"

		node := &config.NodeConfig{
			ID:       i,
			Name:     fmt.Sprintf("%s%d", groupName, i),
			Role:     role,
			Host:     host,
			Port:     port,
			DataDir:  dataDir,
			WALDir:   walDir,
			PGLogDir: pglogDir,
			IsMaster: isMaster,
		}

		group.Nodes = append(group.Nodes, node)
	}

	cfg.Groups = append(cfg.Groups, group)
	cfg.ApplyEnvironment()

	fmt.Printf("\n✅ 组 %d 配置完成，包含 %d 个节点\n", groupID, nodeCount)
	return nil
}

// prompt 提示用户输入
func prompt(reader *bufio.Reader, question, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", question, defaultValue)
	} else {
		fmt.Printf("%s: ", question)
	}

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if line == "" {
		return defaultValue
	}

	return line
}

// Package validatecmd 提供 validate 子命令
package validatecmd

import (
	"fmt"
	"os"

	"github.com/example/pg-deploy/pkg/cli/common"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/example/pg-deploy/pkg/validator"
	"github.com/spf13/cobra"
)

// NewCommand 创建 validate 命令
func NewCommand(log *logger.Logger) *cobra.Command {
	var (
		configFile  string
		showDetails bool
		sshOnly     bool
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "验证配置、SSH 和 PostgreSQL 连通性",
		Long: `validate 用于在正式部署前快速发现配置和连通性问题。

默认会执行：
  - 配置文件加载
  - SSH 登录验证
  - PostgreSQL 凭证与端口验证

适用场景：
  - 首次部署前确认网络和账号是否可用
  - 重建前确认节点仍可通过 SSH 管理
  - Patroni / 主从环境部署后做基础连通性检查`,
		Example: `  # 验证配置（SSH + PostgreSQL）
  pg-deploy validate -c deploy.conf

  # 仅验证 SSH 连接（适用于新部署）
  pg-deploy validate -c deploy.conf --ssh-only

  # 显示详细信息
  pg-deploy validate -c deploy.conf --details`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ = cmd.Flags().GetString("config")
			showDetails, _ = cmd.Flags().GetBool("details")
			sshOnly, _ = cmd.Flags().GetBool("ssh-only")

			if configFile == "" {
				return fmt.Errorf("请指定配置文件 (-c)")
			}

			if _, err := os.Stat(configFile); os.IsNotExist(err) {
				return fmt.Errorf("配置文件不存在: %s", configFile)
			}

			// 加载配置
			cfg, err := common.LoadConfig(configFile)
			if err != nil {
				return err
			}

			log.Info("Starting validation", logger.Fields{
				"config":   configFile,
				"env_name": cfg.EnvironmentName,
			})

			// 设置 PostgreSQL 用户
			username := "postgres"
			if envUser := os.Getenv("PGUSER"); envUser != "" {
				username = envUser
			}

			// 创建验证器
			valid, err := validator.New(cfg, username, log)
			if err != nil {
				return fmt.Errorf("创建验证器失败: %w", err)
			}

			// 添加凭证
			if pgPassword := os.Getenv("PGPASSWORD"); pgPassword != "" {
				log.Info("Adding credentials to .pgpass", logger.Fields{})
				if err := valid.AddCredentialsForNodes(pgPassword); err != nil {
					log.Warn("Failed to add some credentials", logger.Fields{"error": err})
				}
			}

			fmt.Println("\n🔍 验证部署环境...")

			var results []*validator.ValidationResult
			if sshOnly {
				// 仅验证 SSH
				fmt.Println("模式: 仅 SSH 连接验证")
				results = valid.ValidateSSH()
			} else {
				// 执行完整验证
				fmt.Println("模式: SSH + PostgreSQL 连接验证")
				results = valid.ValidateAll()
			}

			// 生成报告
			report := valid.GenerateReport(results)
			fmt.Println("\n" + report)

			// 显示详细信息
			if showDetails {
				fmt.Println("\n📋 详细信息:")
				for _, result := range results {
					fmt.Printf("\n节点: %s\n", result.NodeID)
					fmt.Printf("  SSH: %v\n", result.SSHSuccess)
					fmt.Printf("  PostgreSQL: %v\n", result.PGSuccess)
					if result.SSHErr != nil {
						fmt.Printf("  SSH Error: %v\n", result.SSHErr)
					}
					if result.PGErr != nil {
						fmt.Printf("  PostgreSQL Error: %v\n", result.PGErr)
					}
				}
			}

			// 检查是否有失败
			hasFailure := false
			for _, r := range results {
				if !r.SSHSuccess || (!sshOnly && !r.PGSuccess) {
					hasFailure = true
					break
				}
			}

			if hasFailure {
				return fmt.Errorf("验证失败，部分节点不可达")
			}

			fmt.Println("\n✅ 所有节点验证通过")
			return nil
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "c", "", "配置文件路径")
	cmd.Flags().BoolVar(&showDetails, "details", false, "显示每个节点的 SSH / PostgreSQL 明细和错误")
	cmd.Flags().BoolVar(&sshOnly, "ssh-only", false, "仅验证 SSH 连接，不检查 PostgreSQL 端口和凭证")

	return cmd
}

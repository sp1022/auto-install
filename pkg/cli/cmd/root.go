// Package cmd 提供 PostgreSQL 部署工具的 CLI 命令
package cmd

import (
	"github.com/example/pg-deploy/pkg/cli/deploycmd"
	"github.com/example/pg-deploy/pkg/cli/envcmd"
	"github.com/example/pg-deploy/pkg/cli/validatecmd"
	"github.com/example/pg-deploy/pkg/cli/wizardcmd"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/spf13/cobra"
)

// Execute 执行 CLI 命令
func Execute(log *logger.Logger) error {
	// 创建根命令
	rootCmd := NewRootCommand(log)

	// 添加子命令
	rootCmd.AddCommand(deploycmd.NewCommand(log))
	rootCmd.AddCommand(envcmd.NewCommand(log))
	rootCmd.AddCommand(validatecmd.NewCommand(log))
	rootCmd.AddCommand(wizardcmd.NewCommand(log))

	// 执行命令
	return rootCmd.Execute()
}

// NewRootCommand 创建根命令
func NewRootCommand(log *logger.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pg-deploy",
		Short: "PostgreSQL 自动化部署与重建工具",
		Long: `pg-deploy 用于批量部署、验证、销毁和重建 PostgreSQL 环境。

支持模式：
  - standalone   单机部署
  - master-slave 主从复制
  - patroni      Patroni + etcd 高可用
  - citus        Citus 分布式集群

典型用途：
  - 用统一配置文件管理多节点 PostgreSQL 环境
  - 在测试环境中反复执行 destroy + redeploy
  - 通过 validate 预检查 SSH 和 PostgreSQL 连通性
  - 通过 env 子命令查看和清理当前配置对应的环境`,
		Example: `  # 查看全部命令
  pg-deploy --help

  # 查看某个子命令的详细帮助
  pg-deploy deploy --help
  pg-deploy env destroy --help

  # 验证环境
  pg-deploy validate -c deploy.conf --ssh-only

  # 正式部署
  pg-deploy deploy -c deploy.conf

  # 先销毁再重建
  pg-deploy deploy -c deploy.conf --destroy-first --yes`,
		Version: "2.0.0",
	}

	// 全局标志
	cmd.PersistentFlags().StringP("config", "c", "", "配置文件路径")
	cmd.PersistentFlags().StringP("output", "o", "", "输出文件路径")
	cmd.PersistentFlags().CountP("verbose", "v", "详细输出级别")
	cmd.PersistentFlags().BoolP("dry-run", "n", false, "模拟运行，不执行实际操作")

	return cmd
}

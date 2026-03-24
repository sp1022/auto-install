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
		Short: "PostgreSQL 自动化部署工具",
		Long: `pg-deploy 是一款功能强大的 PostgreSQL 自动化部署工具
支持单机、主从、Patroni 高可用、Citus 分布式四种部署模式
适用于 10-50 节点的生产环境`,
		Version: "2.0.0-alpha",
	}

	// 全局标志
	cmd.PersistentFlags().StringP("config", "c", "", "配置文件路径")
	cmd.PersistentFlags().StringP("output", "o", "", "输出文件路径")
	cmd.PersistentFlags().CountP("verbose", "v", "详细输出级别")
	cmd.PersistentFlags().BoolP("dry-run", "n", false, "模拟运行，不执行实际操作")

	return cmd
}

// Package deploycmd 提供 deploy 子命令
package deploycmd

import (
	"fmt"
	"os"
	"time"

	"github.com/example/pg-deploy/pkg/cli/common"
	"github.com/example/pg-deploy/pkg/cli/envcmd"
	"github.com/example/pg-deploy/pkg/cli/progress"
	"github.com/example/pg-deploy/pkg/deploy"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/spf13/cobra"
)

// NewCommand 创建 deploy 命令
func NewCommand(log *logger.Logger) *cobra.Command {
	var (
		configFile   string
		dryRun       bool
		destroyFirst bool
		confirm      bool
		keepBinaries bool
		keepData     bool
		keepLogs     bool
		verbose      int
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "执行部署或重建",
		Long: `按照配置文件执行完整部署流程。

deploy 会按 deploy_mode 自动选择对应步骤：
  - standalone: 初始化单机 PostgreSQL
  - master-slave: 初始化主从复制
  - patroni: 部署 PostgreSQL、etcd、Patroni 并完成集群校验
  - citus: 部署 coordinator / worker 并完成基础配置

常见使用方式：
  - 首次部署：直接执行 deploy
  - 环境预演：先执行 --dry-run
  - 全量重建：使用 --destroy-first --yes

注意：
  - --destroy-first 是破坏性操作，会先清理当前配置对应环境，再重新部署
  - --keep-binaries / --keep-data / --keep-logs 只在 --destroy-first 下生效
  - Patroni 模式下，销毁阶段会同时处理 Patroni、etcd、DCS 和已探测到的真实路径`,
		Example: `  # 使用配置文件部署
  pg-deploy deploy -c deploy.conf

  # 首次部署前先做预演
  pg-deploy deploy -c deploy.conf --dry-run

  # 详细输出
  pg-deploy deploy -c deploy.conf -v

  # 先销毁再重建
  pg-deploy deploy -c deploy.conf --destroy-first --yes

  # 销毁后保留二进制和日志
  pg-deploy deploy -c deploy.conf --destroy-first --yes --keep-binaries --keep-logs`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 获取参数
			configFile, _ = cmd.Flags().GetString("config")
			dryRun, _ = cmd.Flags().GetBool("dry-run")
			verbose, _ = cmd.Flags().GetCount("verbose")

			// 设置日志级别
			if verbose > 0 {
				log.SetLevel(logger.LevelDebug)
			}

			// 验证配置文件
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

			log.Info("Configuration loaded",
				logger.Fields{
					"deploy_mode": cfg.DeployMode,
					"env_name":    cfg.EnvironmentName,
					"groups":      len(cfg.Groups),
				})

			if destroyFirst && !confirm && !dryRun {
				return fmt.Errorf("--destroy-first 是破坏性操作，请显式传入 --yes")
			}

			// 模拟运行
			if dryRun {
				fmt.Println("\n🔍 Dry run mode - no actual changes will be made")
				fmt.Printf("Configuration: %s\n", cfg.DeployMode)
				if cfg.EnvironmentName != "" {
					fmt.Printf("Environment: %s\n", cfg.EnvironmentName)
				}
				fmt.Printf("Groups: %d\n", len(cfg.Groups))
				fmt.Printf("Total nodes: %d\n", len(cfg.GetAllNodes()))
				fmt.Printf("PGSoftDir: %s\n", cfg.PGSoftDir)
				if destroyFirst {
					fmt.Println("\nDestroy-before-deploy plan:")
					plan := envcmd.BuildDestroyPlan(cfg, envcmd.DestroyOptions{
						KeepBinaries: keepBinaries,
						KeepData:     keepData,
						KeepLogs:     keepLogs,
					})
					envcmd.PrintDestroyPlan(cfg, plan, true)
				}
				return nil
			}

			exec, err := common.BuildExecutor(cfg, log)
			if err != nil {
				return err
			}

			if destroyFirst {
				log.Info("Destroying existing environment before redeploy",
					logger.Fields{
						"keep_binaries": keepBinaries,
						"keep_data":     keepData,
						"keep_logs":     keepLogs,
					})
				plan := envcmd.BuildDestroyPlan(cfg, envcmd.DestroyOptions{
					KeepBinaries: keepBinaries,
					KeepData:     keepData,
					KeepLogs:     keepLogs,
				})
				envcmd.PrintDestroyPlan(cfg, plan, false)
				if err := envcmd.ExecuteDestroyPlan(cfg, exec, log, plan); err != nil {
					return fmt.Errorf("销毁旧环境失败: %w", err)
				}
			}

			// 创建部署编排器
			orchestrator := deploy.NewOrchestrator(cfg, exec, log)

			// 创建进度条
			progressBar := progress.NewDeployProgressBar(log, len(orchestrator.GetSteps()))
			progressBar.Start()

			// 执行部署
			startTime := time.Now()
			err = orchestrator.Execute()
			duration := time.Since(startTime)

			progressBar.Finish()

			if err != nil {
				log.Error("Deployment failed",
					logger.Fields{
						"error":    err,
						"duration": duration,
					})
				return fmt.Errorf("部署失败: %w", err)
			}

			// 显示部署结果
			deploymentResult := cmd.Flags().Lookup("output")
			if deploymentResult != nil && deploymentResult.Value.String() != "" {
				// 保存结果到文件
				// TODO: 实现结果保存
			}

			// 显示成功信息
			fmt.Printf("\n✅ 部署成功完成！\n")
			fmt.Printf("⏱️  总耗时: %v\n", duration)

			// 显示部署摘要
			deploySummary := orchestrator.GetProgress()
			fmt.Printf("\n📊 部署摘要:\n")
			fmt.Printf("   总步骤数: %d\n", deploySummary.TotalSteps)
			fmt.Printf("   已完成: %d\n", deploySummary.CompletedSteps)
			fmt.Printf("   成功率: %.1f%%\n", deploySummary.Percentage)

			return nil
		},
	}

	// 绑定标志
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "配置文件路径")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "仅输出计划，不执行实际修改")
	cmd.Flags().BoolVar(&destroyFirst, "destroy-first", false, "部署前先清理当前环境，再从头重建")
	cmd.Flags().BoolVar(&confirm, "yes", false, "确认执行破坏性销毁操作（与 --destroy-first 联用）")
	cmd.Flags().BoolVar(&keepBinaries, "keep-binaries", false, "销毁后重建时保留 PostgreSQL/Patroni/etcd 安装目录")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "销毁后重建时保留数据目录、WAL 目录和现有数据")
	cmd.Flags().BoolVar(&keepLogs, "keep-logs", false, "销毁后重建时保留 PostgreSQL / Patroni / etcd 日志目录")
	cmd.Flags().CountP("verbose", "v", "详细级别")

	return cmd
}

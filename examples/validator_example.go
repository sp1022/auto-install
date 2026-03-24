//go:build ignore

// Package main 提供 .pgpass 多节点连接验证的示例
// 演示如何使用验证器在 10-50 节点环境下进行并发连接测试
package main

import (
	"fmt"
	"os"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/example/pg-deploy/pkg/validator"
)

func main() {
	// 创建日志记录器
	log, err := logger.New(logger.Config{
		Level:       logger.LevelInfo,
		UseColor:    true,
		IncludeTime: true,
		OutputFile:  "validation.log",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.Info("Starting PostgreSQL deployment validation",
		logger.Fields{"version": "2.0.0-alpha"})

	// 加载配置文件
	if len(os.Args) < 2 {
		fmt.Println("Usage: validator_example <config_file>")
		fmt.Println("Example: validator_example deploy.conf")
		os.Exit(1)
	}

	configPath := os.Args[1]
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error("Failed to load config",
			logger.Fields{"error": err, "path": configPath})
		os.Exit(1)
	}

	// 记录配置摘要
	cfg.LogInfo(log)

	// 创建验证器
	// 假设使用 postgres 用户连接
	username := "postgres"
	if os.Getenv("PGUSER") != "" {
		username = os.Getenv("PGUSER")
	}

	valid, err := validator.New(cfg, username, log)
	if err != nil {
		log.Error("Failed to create validator",
			logger.Fields{"error": err})
		os.Exit(1)
	}

	// 如果提供了 PostgreSQL 密码，添加到 .pgpass
	if pgPassword := os.Getenv("PGPASSWORD"); pgPassword != "" {
		log.Info("Adding credentials to .pgpass",
			logger.Fields{})
		if err := valid.AddCredentialsForNodes(pgPassword); err != nil {
			log.Warn("Failed to add some credentials",
				logger.Fields{"error": err})
		}
	}

	// 执行完整验证（SSH + PostgreSQL）
	fmt.Println("\n🔍 Validating connections to all nodes...")
	results := valid.ValidateAll()

	// 生成报告
	report := valid.GenerateReport(results)
	fmt.Println("\n" + report)

	// 检查是否有失败的节点
	hasFailure := false
	for _, r := range results {
		if !r.SSHSuccess || !r.PGSuccess {
			hasFailure = true
			break
		}
	}

	if hasFailure {
		log.Warn("Some nodes failed validation",
			logger.Fields{
				"check_log": "validation.log",
			})
		os.Exit(1)
	}

	log.Info("✅ All nodes validated successfully",
		logger.Fields{
			"node_count": len(results),
		})

	// 部署前验证
	fmt.Println("\n🔍 Validating deployment environment...")
	if err := valid.ValidateDeployment(); err != nil {
		log.Error("Deployment validation failed",
			logger.Fields{"error": err})
		os.Exit(1)
	}

	fmt.Println("\n✅ Deployment environment is ready!")
	fmt.Println("You can now proceed with deployment.")
}

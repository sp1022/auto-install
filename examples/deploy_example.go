//go:build ignore

// Package main 提供 PostgreSQL 部署引擎的使用示例
package main

import (
	"fmt"
	"os"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/deploy"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

func main() {
	// 创建日志记录器
	log, err := logger.New(logger.Config{
		Level:       logger.LevelInfo,
		UseColor:    true,
		IncludeTime: true,
		OutputFile:  "deploy.log",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	log.Info("Starting PostgreSQL deployment", logger.Fields{"version": "2.0.0-alpha"})

	// 加载配置文件
	if len(os.Args) < 2 {
		fmt.Println("Usage: deploy_example <config_file>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error("Failed to load config", logger.Fields{"error": err, "path": configPath})
		os.Exit(1)
	}

	// 记录配置摘要
	cfg.LogInfo(log)

	// 创建执行器
	nodes := make([]*executor.Node, 0, len(cfg.GetAllNodes()))
	for _, nodeCfg := range cfg.GetAllNodes() {
		node := &executor.Node{
			ID:       nodeCfg.Host,
			Host:     nodeCfg.Host,
			Port:     22,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
		}
		nodes = append(nodes, node)
	}

	exec, err := executor.New(executor.Config{
		Nodes:         nodes,
		MaxConcurrent: 10,
		Logger:        log,
	})
	if err != nil {
		log.Error("Failed to create executor", logger.Fields{"error": err})
		os.Exit(1)
	}

	// 创建部署编排器
	orchestrator := deploy.NewOrchestrator(cfg, exec, log)

	// 执行部署
	fmt.Println("\n🚀 Starting deployment...")
	if err := orchestrator.Execute(); err != nil {
		log.Error("Deployment failed", logger.Fields{"error": err})
		fmt.Printf("\n❌ Deployment failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ Deployment completed successfully!")

	// 显示部署进度
	progress := orchestrator.GetProgress()
	fmt.Printf("\n📊 Deployment Summary:\n")
	fmt.Printf("   Total Steps: %d\n", progress.TotalSteps)
	fmt.Printf("   Completed: %d\n", progress.CompletedSteps)
	fmt.Printf("   Success Rate: %.1f%%\n", progress.Percentage)
}

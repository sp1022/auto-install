// Package main PostgreSQL 自动化部署工具主程序
package main

import (
	"fmt"
	"os"

	"github.com/example/pg-deploy/pkg/cli/cmd"
	"github.com/example/pg-deploy/pkg/logger"
)

var (
	// Version 版本号
	Version = "2.0.0-alpha"
	// BuildDate 构建日期
	BuildDate = "2026-03-16"
)

func main() {
	// 创建默认日志记录器
	log := logger.NewDefault()

	// 执行 CLI 命令
	if err := cmd.Execute(log); err != nil {
		log.Error("Command execution failed", logger.Fields{"error": err})
		fmt.Fprintf(os.Stderr, "\n❌ Error: %v\n", err)
		os.Exit(1)
	}
}

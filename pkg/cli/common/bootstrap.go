package common

import (
	"fmt"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

func LoadConfig(configFile string) (*config.Config, error) {
	cfg, err := config.Load(configFile)
	if err != nil {
		return nil, fmt.Errorf("加载配置失败: %w", err)
	}
	return cfg, nil
}

func BuildExecutor(cfg *config.Config, log *logger.Logger) (*executor.Executor, error) {
	nodes := make([]*executor.Node, 0, len(cfg.GetAllNodes()))
	for _, nodeCfg := range cfg.GetAllNodes() {
		nodes = append(nodes, &executor.Node{
			ID:       nodeCfg.Host,
			Host:     nodeCfg.Host,
			Port:     22,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
		})
	}

	exec, err := executor.New(executor.Config{
		Nodes:         nodes,
		MaxConcurrent: 10,
		Timeout:       30 * time.Minute,
		Logger:        log,
	})
	if err != nil {
		return nil, fmt.Errorf("创建执行器失败: %w", err)
	}

	return exec, nil
}

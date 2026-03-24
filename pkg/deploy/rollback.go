// Package deploy 提供部署回滚管理器
// 在部署失败时安全地回滚已完成的操作
package deploy

import (
	"fmt"
	"os"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// RollbackManager 回滚管理器
type RollbackManager struct {
	executor    *executor.Executor
	config      *config.Config
	logger      *logger.Logger
	snapshots   []*RollbackSnapshot
	snapshotDir string
}

// RollbackSnapshot 回滚快照
type RollbackSnapshot struct {
	StepName     string
	StepIndex    int
	Timestamp    time.Time
	NodesState   map[string]*NodeState
	ConfigBackup string
}

// NodeState 节点状态
type NodeState struct {
	Host            string
	RunningServices []string
	FilesCreated    []string
	UsersCreated    []string
	DataDirsExist   map[string]bool
}

// NewRollbackManager 创建回滚管理器
func NewRollbackManager(exec *executor.Executor, cfg *config.Config, log *logger.Logger) (*RollbackManager, error) {
	// 创建快照目录
	snapshotDir := "/var/lib/pg-deploy/snapshots"
	os.MkdirAll(snapshotDir, 0755)

	return &RollbackManager{
		executor:    exec,
		config:      cfg,
		logger:      log,
		snapshots:   make([]*RollbackSnapshot, 0),
		snapshotDir: snapshotDir,
	}, nil
}

// CreateSnapshot 创建回滚快照
func (r *RollbackManager) CreateSnapshot(stepName string, stepIndex int) (*RollbackSnapshot, error) {
	r.logger.Info("Creating rollback snapshot",
		logger.Fields{
			"step":  stepName,
			"index": stepIndex,
		})

	snapshot := &RollbackSnapshot{
		StepName:   stepName,
		StepIndex:  stepIndex,
		Timestamp:  time.Now(),
		NodesState: make(map[string]*NodeState),
	}

	// 捕获所有节点状态
	nodes := r.config.GetAllNodes()
	for _, node := range nodes {
		state, err := r.captureNodeState(node)
		if err != nil {
			r.logger.Warn("Failed to capture node state",
				logger.Fields{
					"node":  node.Host,
					"error": err,
				})
			continue
		}
		snapshot.NodesState[node.Host] = state
	}

	// 备份配置文件
	if err := r.backupConfig(snapshot); err != nil {
		r.logger.Warn("Failed to backup config",
			logger.Fields{"error": err})
	}

	// 保存快照
	r.snapshots = append(r.snapshots, snapshot)

	r.logger.Info("Snapshot created successfully",
		logger.Fields{
			"step":           stepName,
			"nodes_captured": len(snapshot.NodesState),
		})

	return snapshot, nil
}

// captureNodeState 捕获节点状态
func (r *RollbackManager) captureNodeState(node *config.NodeConfig) (*NodeState, error) {
	state := &NodeState{
		Host:          node.Host,
		FilesCreated:  make([]string, 0),
		UsersCreated:  make([]string, 0),
		DataDirsExist: make(map[string]bool),
	}

	// 1. 检查运行中的服务
	servicesCmd := "systemctl list-units --type=service --state=running | grep -E 'postgres|patroni' | awk '{print $1}'"
	result := r.executor.RunOnNode(&executor.Node{
		ID:   node.Host,
		Host: node.Host,
		User: r.config.SSHUser,
	}, servicesCmd, false, false)

	if result.Error == nil {
		// 解析服务列表
		state.RunningServices = parseServiceList(result.Output)
	}

	// 2. 检查数据目录
	if node.DataDir != "" {
		existsCmd := fmt.Sprintf("test -d %s && echo 'exists'", node.DataDir)
		result := r.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: r.config.SSHUser,
		}, existsCmd, false, false)

		state.DataDirsExist[node.DataDir] = (result.Error == nil)
	}

	// 3. 检查 WAL 目录
	if node.WALDir != "" {
		existsCmd := fmt.Sprintf("test -d %s && echo 'exists'", node.WALDir)
		result := r.executor.RunOnNode(&executor.Node{
			ID:   node.Host,
			Host: node.Host,
			User: r.config.SSHUser,
		}, existsCmd, false, false)

		state.DataDirsExist[node.WALDir] = (result.Error == nil)
	}

	return state, nil
}

// backupConfig 备份配置文件
func (r *RollbackManager) backupConfig(snapshot *RollbackSnapshot) error {
	// TODO: 实现配置文件备份
	snapshot.ConfigBackup = fmt.Sprintf("%s/backup_%s.conf",
		r.snapshotDir,
		snapshot.Timestamp.Format("20060102_150405"))

	return nil
}

// RollbackToStep 回滚到指定步骤
func (r *RollbackManager) RollbackToStep(targetStepIndex int) error {
	r.logger.Info("Starting rollback",
		logger.Fields{
			"target_step":     targetStepIndex,
			"total_snapshots": len(r.snapshots),
		})

	// 从后向前回滚
	for i := len(r.snapshots) - 1; i >= targetStepIndex; i-- {
		snapshot := r.snapshots[i]
		r.logger.Info(fmt.Sprintf("Rolling back step %d: %s", snapshot.StepIndex, snapshot.StepName),
			logger.Fields{
				"step":      snapshot.StepName,
				"timestamp": snapshot.Timestamp,
			})

		if err := r.rollbackSnapshot(snapshot); err != nil {
			r.logger.Error("Failed to rollback snapshot",
				logger.Fields{
					"step":  snapshot.StepName,
					"error": err,
				})
			// 继续回滚其他步骤
		}
	}

	r.logger.Info("Rollback completed", logger.Fields{})
	return nil
}

// rollbackSnapshot 回滚单个快照
func (r *RollbackManager) rollbackSnapshot(snapshot *RollbackSnapshot) error {
	// 1. 停止服务
	if err := r.stopServices(snapshot); err != nil {
		return fmt.Errorf("failed to stop services: %w", err)
	}

	// 2. 清理文件
	if err := r.cleanupFiles(snapshot); err != nil {
		return fmt.Errorf("failed to cleanup files: %w", err)
	}

	// 3. 删除用户（可选，需要确认）
	// if err := r.removeUsers(snapshot); err != nil {
	// 	return fmt.Errorf("failed to remove users: %w", err)
	// }

	return nil
}

// stopServices 停止服务
func (r *RollbackManager) stopServices(snapshot *RollbackSnapshot) error {
	for host, state := range snapshot.NodesState {
		for _, service := range state.RunningServices {
			cmd := fmt.Sprintf("systemctl stop %s", service)
			result := r.executor.RunOnNode(&executor.Node{
				ID:   host,
				Host: host,
				User: r.config.SSHUser,
			}, cmd, true, false)

			if result.Error != nil {
				r.logger.Warn("Failed to stop service",
					logger.Fields{
						"host":    host,
						"service": service,
						"error":   result.Error,
					})
			} else {
				r.logger.Info("Service stopped",
					logger.Fields{
						"host":    host,
						"service": service,
					})
			}
		}
	}

	return nil
}

// cleanupFiles 清理文件
func (r *RollbackManager) cleanupFiles(snapshot *RollbackSnapshot) error {
	nodes := r.config.GetAllNodes()

	for _, node := range nodes {
		state, exists := snapshot.NodesState[node.Host]
		if !exists {
			continue
		}

		// 清理数据目录（如果是在此步骤中创建的）
		if !state.DataDirsExist[node.DataDir] && node.DataDir != "" {
			// 检查目录是否在当前步骤中创建
			cmd := fmt.Sprintf("rm -rf %s", node.DataDir)
			result := r.executor.RunOnNode(&executor.Node{
				ID:   node.Host,
				Host: node.Host,
				User: r.config.SSHUser,
			}, cmd, true, false)

			if result.Error != nil {
				r.logger.Warn("Failed to remove data directory",
					logger.Fields{
						"host":  node.Host,
						"dir":   node.DataDir,
						"error": result.Error,
					})
			}
		}

		// 清理 WAL 目录
		if !state.DataDirsExist[node.WALDir] && node.WALDir != "" {
			cmd := fmt.Sprintf("rm -rf %s", node.WALDir)
			result := r.executor.RunOnNode(&executor.Node{
				ID:   node.Host,
				Host: node.Host,
				User: r.config.SSHUser,
			}, cmd, true, false)

			if result.Error != nil {
				r.logger.Warn("Failed to remove WAL directory",
					logger.Fields{
						"host":  node.Host,
						"dir":   node.WALDir,
						"error": result.Error,
					})
			}
		}
	}

	return nil
}

// removeUsers 删除用户（谨慎操作）
func (r *RollbackManager) removeUsers(snapshot *RollbackSnapshot) error {
	// TODO: 实现用户删除逻辑
	// 注意：这需要非常谨慎，可能影响其他服务
	return nil
}

// RollbackStep 回滚特定步骤
func (r *RollbackManager) RollbackStep(stepName string) error {
	// 查找快照
	var snapshot *RollbackSnapshot
	for _, s := range r.snapshots {
		if s.StepName == stepName {
			snapshot = s
			break
		}
	}

	if snapshot == nil {
		return fmt.Errorf("snapshot not found for step: %s", stepName)
	}

	r.logger.Info("Rolling back specific step",
		logger.Fields{"step": stepName})

	return r.rollbackSnapshot(snapshot)
}

// CleanupSnapshots 清理旧快照
func (r *RollbackManager) CleanupSnapshots(olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	remaining := make([]*RollbackSnapshot, 0)

	for _, snapshot := range r.snapshots {
		if snapshot.Timestamp.After(cutoff) {
			remaining = append(remaining, snapshot)
		} else {
			// 删除快照文件
			snapshotFile := fmt.Sprintf("%s/snapshot_%d_%s.json",
				r.snapshotDir,
				snapshot.StepIndex,
				snapshot.StepName)
			os.Remove(snapshotFile)
		}
	}

	r.snapshots = remaining
	r.logger.Info("Cleaned up old snapshots",
		logger.Fields{
			"removed":   len(r.snapshots) - len(remaining),
			"remaining": len(remaining),
		})

	return nil
}

// GetSnapshots 获取所有快照
func (r *RollbackManager) GetSnapshots() []*RollbackSnapshot {
	return r.snapshots
}

// parseServiceList 解析服务列表
func parseServiceList(output string) []string {
	var services []string
	lines := splitLines(output)
	for _, line := range lines {
		line = trimSpace(line)
		if line != "" {
			services = append(services, line)
		}
	}
	return services
}

// 辅助函数
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

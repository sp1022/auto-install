// Package deploy 提供PostgreSQL自动化部署引擎
// 支持7步部署流程的编排、幂等性检查和回滚管理
package deploy

import (
	"fmt"
	"sync"
	"time"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
)

// Step 部署步骤接口
type Step interface {
	// Name 返回步骤名称
	Name() string

	// Description 返回步骤描述
	Description() string

	// Execute 执行部署步骤
	Execute(ctx *Context) error

	// Rollback 回滚部署步骤
	Rollback(ctx *Context) error

	// Validate 验证步骤前置条件
	Validate(ctx *Context) error

	// IsCompleted 检查步骤是否已完成（幂等性）
	IsCompleted(ctx *Context) (bool, error)
}

// Context 部署上下文
type Context struct {
	Config    *config.Config
	Executor  *executor.Executor
	Logger    *logger.Logger
	State     *State
	StartTime time.Time
}

// State 部署状态
type State struct {
	mu             sync.RWMutex
	CurrentStep    int
	CompletedSteps map[int]bool
	FailedSteps    map[int]error
	RollbackMode   bool
	TotalSteps     int
	StartTime      time.Time
	StepStartTimes map[int]time.Time
	StepDurations  map[int]time.Duration
}

// Orchestrator 部署编排器
type Orchestrator struct {
	steps   []Step
	context *Context
	logger  *logger.Logger
}

// NewOrchestrator 创建新的部署编排器
func NewOrchestrator(cfg *config.Config, exec *executor.Executor, log *logger.Logger) *Orchestrator {
	// 创建部署状态
	state := &State{
		CompletedSteps: make(map[int]bool),
		FailedSteps:    make(map[int]error),
		StepStartTimes: make(map[int]time.Time),
		StepDurations:  make(map[int]time.Duration),
		StartTime:      time.Now(),
	}

	// 创建部署上下文
	ctx := &Context{
		Config:   cfg,
		Executor: exec,
		Logger:   log,
		State:    state,
	}

	// 初始化7步部署流程
	steps := initializeSteps(cfg)

	return &Orchestrator{
		steps:   steps,
		context: ctx,
		logger:  log,
	}
}

// initializeSteps 根据配置初始化部署步骤
func initializeSteps(cfg *config.Config) []Step {
	// 基础步骤（所有模式通用）
	steps := []Step{
		NewPrepareDirectoriesStep(),
		NewDeploySoftwareStep(),
	}

	// 根据部署模式添加特定步骤
	switch cfg.DeployMode {
	case config.ModeStandalone, config.ModeMasterSlave:
		steps = append(steps,
			NewInitDatabaseStep(),
			NewConfigurePostgreSQLStep(),
			NewStartPostgreSQLStep(),
		)

		// 主从模式需要复制设置
		if cfg.DeployMode == config.ModeMasterSlave {
			steps = append(steps, NewSetupReplicationStep())
		}

	case config.ModePatroni:
		steps = append(steps,
			NewInstallPatroniStep(),
			NewConfigurePatroniStep(),
			NewStartPatroniClusterStep(),
		)

	case config.ModeCitus:
		steps = append(steps,
			NewInitDatabaseStep(),
			NewConfigurePostgreSQLStep(),
			NewStartPostgreSQLStep(),
			NewConfigureCitusStep(),
		)
	}

	// 通用验证步骤
	steps = append(steps, NewValidateDeploymentStep())

	return steps
}

// Execute 执行完整的部署流程
func (o *Orchestrator) Execute() error {
	o.context.State.TotalSteps = len(o.steps)
	o.context.State.StartTime = time.Now()

	o.logger.Info("Starting PostgreSQL deployment",
		logger.Fields{
			"mode":        o.context.Config.DeployMode,
			"total_steps": len(o.steps),
			"groups":      len(o.context.Config.Groups),
		})

	// 执行每个步骤
	for i, step := range o.steps {
		o.context.State.CurrentStep = i + 1
		o.context.State.StepStartTimes[i] = time.Now()

		o.logger.Info(fmt.Sprintf("Step %d/%d: %s", i+1, len(o.steps), step.Name()),
			logger.Fields{
				"step":        step.Name(),
				"description": step.Description(),
			})

		// 1. 验证前置条件
		if err := step.Validate(o.context); err != nil {
			o.logger.Error("Step validation failed",
				logger.Fields{
					"step":  step.Name(),
					"error": err,
				})
			o.context.State.FailedSteps[i] = err
			return o.handleFailure(i, step, err)
		}

		// 2. 检查幂等性
		completed, err := step.IsCompleted(o.context)
		if err != nil {
			o.logger.Warn("Failed to check step completion",
				logger.Fields{
					"step":  step.Name(),
					"error": err,
				})
		} else if completed {
			o.logger.Info("Step already completed, skipping",
				logger.Fields{"step": step.Name()})
			o.context.State.CompletedSteps[i] = true
			continue
		}

		// 3. 执行步骤
		if err := step.Execute(o.context); err != nil {
			o.logger.Error("Step execution failed",
				logger.Fields{
					"step":  step.Name(),
					"error": err,
				})
			o.context.State.FailedSteps[i] = err
			return o.handleFailure(i, step, err)
		}

		// 4. 标记完成
		o.context.State.CompletedSteps[i] = true
		o.context.State.StepDurations[i] = time.Since(o.context.State.StepStartTimes[i])

		o.logger.Info("Step completed successfully",
			logger.Fields{
				"step":     step.Name(),
				"duration": o.context.State.StepDurations[i],
			})
	}

	// 部署成功
	totalDuration := time.Since(o.context.State.StartTime)
	o.logger.Info("Deployment completed successfully",
		logger.Fields{
			"total_duration":  totalDuration,
			"steps_completed": len(o.steps),
		})

	return nil
}

// GetSteps 获取部署步骤列表
func (o *Orchestrator) GetSteps() []Step {
	return o.steps
}

// handleFailure 处理步骤失败
func (o *Orchestrator) handleFailure(failedStepIndex int, failedStep Step, err error) error {
	o.logger.Error("Deployment failed, initiating rollback",
		logger.Fields{
			"failed_step": failedStep.Name(),
			"step_index":  failedStepIndex,
		})

	o.context.State.RollbackMode = true

	// 回滚已完成的步骤（从后向前）
	for i := failedStepIndex - 1; i >= 0; i-- {
		if !o.context.State.CompletedSteps[i] {
			continue
		}

		step := o.steps[i]
		o.logger.Info(fmt.Sprintf("Rolling back step %d: %s", i+1, step.Name()),
			logger.Fields{"step": step.Name()})

		if rollbackErr := step.Rollback(o.context); rollbackErr != nil {
			o.logger.Error("Rollback failed",
				logger.Fields{
					"step":  step.Name(),
					"error": rollbackErr,
				})
			// 继续回滚其他步骤
		} else {
			o.logger.Info("Step rolled back successfully",
				logger.Fields{"step": step.Name()})
		}
	}

	return fmt.Errorf("deployment failed at step %d (%s): %w",
		failedStepIndex+1, failedStep.Name(), err)
}

// GetProgress 获取部署进度
func (o *Orchestrator) GetProgress() *Progress {
	o.context.State.mu.RLock()
	defer o.context.State.mu.RUnlock()

	completedCount := len(o.context.State.CompletedSteps)
	progress := &Progress{
		TotalSteps:     o.context.State.TotalSteps,
		CompletedSteps: completedCount,
		CurrentStep:    o.context.State.CurrentStep,
		Percentage:     float64(completedCount) / float64(o.context.State.TotalSteps) * 100,
		FailedSteps:    make(map[string]string),
		StepDurations:  make(map[string]time.Duration),
	}

	// 复制失败的步骤
	for i, err := range o.context.State.FailedSteps {
		stepName := o.steps[i].Name()
		progress.FailedSteps[stepName] = err.Error()
	}

	// 复制步骤持续时间
	for i, duration := range o.context.State.StepDurations {
		stepName := o.steps[i].Name()
		progress.StepDurations[stepName] = duration
	}

	return progress
}

// Progress 部署进度
type Progress struct {
	TotalSteps     int
	CompletedSteps int
	CurrentStep    int
	Percentage     float64
	FailedSteps    map[string]string
	StepDurations  map[string]time.Duration
}

// Resume 从中断点恢复部署
func (o *Orchestrator) Resume() error {
	o.logger.Info("Resuming deployment",
		logger.Fields{
			"completed": len(o.context.State.CompletedSteps),
			"total":     len(o.steps),
		})

	// 找到第一个未完成的步骤
	startIndex := 0
	for i, step := range o.steps {
		if completed, _ := step.IsCompleted(o.context); !completed {
			startIndex = i
			break
		}
	}

	// 从 startIndex 开始执行
	for i := startIndex; i < len(o.steps); i++ {
		step := o.steps[i]
		o.context.State.CurrentStep = i + 1

		o.logger.Info(fmt.Sprintf("Resuming step %d/%d: %s", i+1, len(o.steps), step.Name()),
			logger.Fields{"step": step.Name()})

		if err := step.Validate(o.context); err != nil {
			return fmt.Errorf("step validation failed: %w", err)
		}

		if err := step.Execute(o.context); err != nil {
			return fmt.Errorf("step execution failed: %w", err)
		}

		o.context.State.CompletedSteps[i] = true
	}

	return nil
}

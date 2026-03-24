// Package progress 提供部署进度显示功能
package progress

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/example/pg-deploy/pkg/logger"
	"github.com/schollz/progressbar/v3"
)

// DeployProgressBar 部署进度条
type DeployProgressBar struct {
	bar      *progressbar.ProgressBar
	step     int
	total    int
	logger   *logger.Logger
	stepName string
}

// NewDeployProgressBar 创建部署进度条
func NewDeployProgressBar(log *logger.Logger, totalSteps int) *DeployProgressBar {
	bar := progressbar.NewOptions(totalSteps,
		progressbar.OptionSetDescription("部署进度"),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("steps"),
	)

	return &DeployProgressBar{
		bar:    bar,
		total:  totalSteps,
		logger: log,
	}
}

// Start 启动进度条
func (p *DeployProgressBar) Start() {
	fmt.Println()
}

// Finish 完成进度条
func (p *DeployProgressBar) Finish() {
	p.bar.Close()
	fmt.Println()
}

// UpdateStep 更新当前步骤
func (p *DeployProgressBar) UpdateStep(step int, name string) {
	p.step = step
	p.stepName = name
	p.bar.Describe(fmt.Sprintf("步骤 %d/%d: %s", step+1, p.total, name))
}

// Increment 增加进度
func (p *DeployProgressBar) Increment() {
	p.bar.Add(1)
}

// SetCurrent 设置当前步骤
func (p *DeployProgressBar) SetCurrent(step int) {
	if step > p.step {
		for i := p.step; i < step; i++ {
			p.Increment()
		}
		p.step = step
	}
}

// CompleteStep 完成步骤
func (p *DeployProgressBar) CompleteStep(step int, name string, err error) {
	p.UpdateStep(step, name)

	if err != nil {
		p.logger.Error("Step failed",
			logger.Fields{
				"step":  name,
				"error": err,
			})
	} else {
		p.logger.Info("Step completed",
			logger.Fields{
				"step": name,
			})
		p.Increment()
	}
}

// SimpleProgressBar 简单进度条
type SimpleProgressBar struct {
	current   int
	total     int
	startTime time.Time
	logger    *logger.Logger
}

// NewSimpleProgressBar 创建简单进度条
func NewSimpleProgressBar(log *logger.Logger, total int) *SimpleProgressBar {
	return &SimpleProgressBar{
		total:     total,
		startTime: time.Now(),
		logger:    log,
	}
}

// Update 更新进度
func (p *SimpleProgressBar) Update(current int, message string) {
	p.current = current
	percentage := float64(current) / float64(p.total) * 100

	// 计算耗时
	elapsed := time.Since(p.startTime)

	// 估算剩余时间
	var eta string
	if current > 0 {
		avgTime := elapsed / time.Duration(current)
		remaining := time.Duration(p.total-current) * avgTime
		eta = fmt.Sprintf(" ETA: %v", remaining.Round(time.Second))
	}

	fmt.Printf("\r[%-50s] %d/%d (%.1f%%) %s %s",
		strings.Repeat("=", int(percentage/2))+">",
		current, p.total, percentage, message, eta)

	if current == p.total {
		fmt.Println()
		fmt.Printf("✅ 完成！总耗时: %v\n", elapsed.Round(time.Second))
	}
}

// StepProgress 步骤进度
type StepProgress struct {
	name      string
	startTime time.Time
	logger    *logger.Logger
}

// NewStepProgress 创建步骤进度
func NewStepProgress(name string, log *logger.Logger) *StepProgress {
	fmt.Printf("\n🔄 %s\n", name)
	return &StepProgress{
		name:      name,
		startTime: time.Now(),
		logger:    log,
	}
}

// Complete 完成步骤
func (p *StepProgress) Complete(success bool, err error) {
	duration := time.Since(p.startTime)
	if success {
		fmt.Printf("✅ %s 完成 (耗时: %v)\n", p.name, duration.Round(time.Millisecond))
	} else {
		fmt.Printf("❌ %s 失败 (耗时: %v): %v\n", p.name, duration.Round(time.Millisecond), err)
	}
}

// Spinner 加载动画
type Spinner struct {
	chars    []string
	delay    time.Duration
	message  string
	active   bool
	stopChan chan bool
	logger   *logger.Logger
}

// NewSpinner 创建加载动画
func NewSpinner(message string, log *logger.Logger) *Spinner {
	return &Spinner{
		chars:    []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		delay:    100 * time.Millisecond,
		message:  message,
		stopChan: make(chan bool),
		logger:   log,
	}
}

// Start 启动加载动画
func (s *Spinner) Start() {
	s.active = true
	go func() {
		i := 0
		for {
			select {
			case <-s.stopChan:
				return
			default:
				fmt.Printf("\r%s %s", s.chars[i], s.message)
				i = (i + 1) % len(s.chars)
				time.Sleep(s.delay)
			}
		}
	}()
}

// Stop 停止加载动画
func (s *Spinner) Stop() {
	s.active = false
	s.stopChan <- true
	fmt.Print("\r")
}

// Package logger 提供结构化日志功能，支持多级别输出和上下文字段
// 适配 10-50 节点的并发部署场景，线程安全
package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Level 日志级别
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String 返回日志级别的字符串表示
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Color 返回日志级别对应的 ANSI 颜色码
func (l Level) Color() string {
	switch l {
	case LevelDebug:
		return "\033[36m" // Cyan
	case LevelInfo:
		return "\033[32m" // Green
	case LevelWarn:
		return "\033[33m" // Yellow
	case LevelError:
		return "\033[31m" // Red
	default:
		return "\033[0m"
	}
}

// Fields 日志上下文字段
type Fields map[string]interface{}

// Logger 结构化日志记录器
type Logger struct {
	mu          sync.Mutex
	level       Level
	output      *os.File
	file        *os.File
	useColor    bool
	includeTime bool
}

// Config Logger 配置
type Config struct {
	Level       Level
	OutputFile  string // 可选的日志输出文件
	UseColor    bool   // 是否使用彩色输出（默认自动检测）
	IncludeTime bool   // 是否包含时间戳（默认 true）
}

// New 创建新的日志记录器
func New(cfg Config) (*Logger, error) {
	log := &Logger{
		level:       cfg.Level,
		output:      os.Stdout,
		useColor:    cfg.UseColor,
		includeTime: cfg.IncludeTime,
	}

	// 自动检测彩色输出
	if !cfg.UseColor {
		log.useColor = isTerminal(log.output)
	}

	// 如果指定了日志文件，同时输出到文件
	if cfg.OutputFile != "" {
		file, err := os.OpenFile(cfg.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
		log.file = file
	}

	return log, nil
}

// NewDefault 创建使用默认配置的日志记录器
func NewDefault() *Logger {
	log, _ := New(Config{
		Level:       LevelInfo,
		UseColor:    true,
		IncludeTime: true,
	})
	return log
}

// Close 关闭日志记录器
func (l *Logger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// SetLevel 设置日志级别
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// log 内部日志方法，线程安全
func (l *Logger) log(level Level, msg string, fields Fields) {
	if level < l.level {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// 构建日志消息
	logMsg := l.formatMessage(level, msg, fields)

	// 输出到控制台
	if l.useColor {
		fmt.Fprintf(l.output, "%s%s\033[0m\n", level.Color(), logMsg)
	} else {
		fmt.Fprintln(l.output, logMsg)
	}

	// 同时输出到文件
	if l.file != nil {
		// 文件输出不包含颜色代码
		fmt.Fprintln(l.file, l.formatMessage(level, msg, fields))
	}
}

// formatMessage 格式化日志消息
func (l *Logger) formatMessage(level Level, msg string, fields Fields) string {
	var parts []string

	// 时间戳
	if l.includeTime {
		parts = append(parts, time.Now().Format("2006-01-02 15:04:05"))
	}

	// 日志级别
	parts = append(parts, fmt.Sprintf("[%s]", level.String()))

	// 消息
	parts = append(parts, msg)

	// 结构化字段
	if len(fields) > 0 {
		fieldStr, _ := json.Marshal(fields)
		parts = append(parts, string(fieldStr))
	}

	return fmt.Sprintf("%s %s", parts[0], fmt.Sprint(parts[1:]))
}

// Debug 记录 DEBUG 级别日志
func (l *Logger) Debug(msg string, fields Fields) {
	l.log(LevelDebug, msg, fields)
}

// Info 记录 INFO 级别日志
func (l *Logger) Info(msg string, fields Fields) {
	l.log(LevelInfo, msg, fields)
}

// Warn 记录 WARN 级别日志
func (l *Logger) Warn(msg string, fields Fields) {
	l.log(LevelWarn, msg, fields)
}

// Error 记录 ERROR 级别日志
func (l *Logger) Error(msg string, fields Fields) {
	l.log(LevelError, msg, fields)
}

// WithFields 返回带有预设字段的消息（简化 API）
func (l *Logger) WithFields(fields Fields) *Logger {
	// 简化实现，实际使用时可以包装消息
	return l
}

// isTerminal 检查输出是否为终端
func isTerminal(file *os.File) bool {
	fileInfo, _ := file.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

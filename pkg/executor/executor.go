// Package executor 提供并发安全的远程命令执行功能
// 支持 SSH 密钥和密码认证，优化 10-50 节点的高并发场景
package executor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/pg-deploy/pkg/logger"
)

// AuthMethod SSH 认证方法
type AuthMethod int

const (
	AuthKey      AuthMethod = iota // SSH 密钥认��
	AuthPassword                   // SSH 密码认证
)

// Node 远程节点定义
type Node struct {
	ID       string
	Host     string
	Port     int
	User     string
	Password string // 密码认证时使用
	KeyPath  string // 密钥认证时使用
}

// Result ��令执行结果
type Result struct {
	Node        *Node
	Command     string
	Output      string
	Error       error
	ExitCode    int
	Duration    time.Duration
	SuppressLog bool // 是否抑制日志（用于预期的失败，如检查文件存在性）
}

// Executor 并发安全的远程命令执行器
type Executor struct {
	nodes         []*Node
	sshPassPath   string // sshpass 可执行文件路径
	logger        *logger.Logger
	maxConcurrent int // 最大并发数（防止连接池耗尽）
	timeout       time.Duration
	localIPs      []string // 本机IP列表，用于检测是否为本地节点
}

// Config Executor 配置
type Config struct {
	Nodes         []*Node
	AuthMethod    AuthMethod
	MaxConcurrent int // 默认 10，适合 10-50 节点场景
	Timeout       time.Duration
	Logger        *logger.Logger
}

// New 创建新的执行器
func New(cfg Config) (*Executor, error) {
	if len(cfg.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes provided")
	}

	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 10 // 默认并发数
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Minute // 默认超时
	}

	exec := &Executor{
		nodes:         cfg.Nodes,
		logger:        cfg.Logger,
		maxConcurrent: cfg.MaxConcurrent,
		timeout:       cfg.Timeout,
	}

	// 检查是否有节点使用密码认证
	needSshPass := false
	for _, node := range cfg.Nodes {
		if node.Password != "" {
			needSshPass = true
			break
		}
	}

	if needSshPass {
		// 查找 sshpass
		path, err := exec.lookPath("sshpass")
		if err != nil {
			return nil, fmt.Errorf("sshpass required for password auth but not found: %w", err)
		}
		exec.sshPassPath = path
		exec.logger.Debug("Found sshpass for password authentication",
			logger.Fields{"path": path})
	}

	// 获取本机IP列表
	localIPs, err := exec.getLocalIPs()
	if err != nil {
		exec.logger.Warn("Failed to get local IPs, local node detection may not work",
			logger.Fields{"error": err})
	} else {
		exec.localIPs = localIPs
		exec.logger.Debug("Detected local IPs",
			logger.Fields{"ips": localIPs})
	}

	return exec, nil
}

// lookPath 查找命令路径
func (e *Executor) lookPath(cmd string) (string, error) {
	path, err := exec.LookPath(cmd)
	if err != nil {
		return "", fmt.Errorf("command '%s' not found in PATH", cmd)
	}
	return path, nil
}

// RunOnNode 在单个节点上执行命令
func (e *Executor) RunOnNode(node *Node, command string, useSudo bool, suppressLog bool) *Result {
	start := time.Now()
	command = strings.TrimSpace(command)

	result := &Result{
		Node:        node,
		Command:     command,
		SuppressLog: suppressLog,
	}

	// 检测是否为本机节点
	isLocal := e.isLocalNode(node.Host)

	var actualCmd string
	if isLocal {
		// 本机直接执行命令
		actualCmd = command
		if useSudo {
			actualCmd = fmt.Sprintf("sudo sh -c %s", shellQuote(command))
		}

		e.logger.Debug("Executing command on local node",
			logger.Fields{
				"node":        node.ID,
				"host":        node.Host,
				"cmd_preview": formatCommandForLog(command),
				"cmd_lines":   commandLineCount(command),
			})
	} else {
		// 远程节点通过SSH执行
		sshCmd, err := e.buildSSHCommand(node, command, useSudo)
		if err != nil {
			result.Error = fmt.Errorf("failed to build SSH command: %w", err)
			result.Duration = time.Since(start)
			return result
		}
		actualCmd = sshCmd

		e.logger.Debug("Executing command on remote node",
			logger.Fields{
				"node":        node.ID,
				"host":        node.Host,
				"cmd_preview": formatCommandForLog(command),
				"cmd_lines":   commandLineCount(command),
			})
	}

	// 执行命令 - 直接通过 sh 执行，避免 bash 的特殊性
	cmd := exec.Command("sh", "-c", actualCmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 使用通道来处理命令执行和超时
	type cmdResult struct {
		err error
	}
	done := make(chan cmdResult, 1)

	// 在 goroutine 中启动命令
	go func() {
		done <- cmdResult{err: cmd.Run()}
	}()

	var err error
	// 等待命令完成或超时
	select {
	case res := <-done:
		err = res.err
	case <-time.After(e.timeout):
		// 超时：杀死进程并等待回收
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		// 必须等待进程结束以回收资源，避免僵尸进程
		cmd.Wait()
		err = fmt.Errorf("command timed out after %v", e.timeout)
	}

	result.Duration = time.Since(start)

	// 收集输出
	result.Output = stdout.String()
	if stderr.Len() > 0 {
		if result.Output != "" {
			result.Output += "\n"
		}
		result.Output += stderr.String()
	}

	// 处理结果
	if err != nil {
		result.Error = err
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		// 只有在非抑制模式下才记录错误日志
		if !result.SuppressLog {
			e.logger.Error("Command failed on node",
				logger.Fields{
					"node":        node.ID,
					"host":        node.Host,
					"cmd_preview": formatCommandForLog(command),
					"cmd_lines":   commandLineCount(command),
					"exit_code":   result.ExitCode,
					"error":       err,
					"output":      result.Output,
				})
		}
	} else {
		result.ExitCode = 0
		e.logger.Debug("Command succeeded on node",
			logger.Fields{
				"node":     node.ID,
				"host":     node.Host,
				"duration": result.Duration,
			})
	}

	return result
}

// RunOnNodeStreaming 在单个节点上执行命令并实时流式输出日志
func (e *Executor) RunOnNodeStreaming(node *Node, command string, useSudo bool, outputCallback func(line string)) *Result {
	start := time.Now()
	command = strings.TrimSpace(command)

	result := &Result{
		Node:        node,
		Command:     command,
		SuppressLog: false,
	}

	// 检测是否为本机节点
	isLocal := e.isLocalNode(node.Host)

	var actualCmd string
	if isLocal {
		actualCmd = command
		if useSudo {
			actualCmd = fmt.Sprintf("sudo sh -c %s", shellQuote(command))
		}
		e.logger.Debug("Executing streaming command on local node",
			logger.Fields{"node": node.ID, "host": node.Host})
	} else {
		sshCmd, err := e.buildSSHCommand(node, command, useSudo)
		if err != nil {
			result.Error = fmt.Errorf("failed to build SSH command: %w", err)
			result.Duration = time.Since(start)
			return result
		}
		actualCmd = sshCmd
		e.logger.Debug("Executing streaming command on remote node",
			logger.Fields{"node": node.ID, "host": node.Host})
	}

	// 执行命令
	cmd := exec.Command("sh", "-c", actualCmd)

	// 获取 stdout 和 stderr 管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stdout pipe: %w", err)
		result.Duration = time.Since(start)
		return result
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Errorf("failed to create stderr pipe: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		result.Error = fmt.Errorf("failed to start command: %w", err)
		result.Duration = time.Since(start)
		return result
	}

	// 使用 WaitGroup 等待两个输出流处理完成
	var wg sync.WaitGroup
	var outputLines []string
	var mu sync.Mutex

	// 处理 stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			outputLines = append(outputLines, line)
			mu.Unlock()
			if outputCallback != nil {
				outputCallback(line)
			}
		}
	}()

	// 处理 stderr
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			outputLines = append(outputLines, line)
			mu.Unlock()
			if outputCallback != nil {
				outputCallback("[stderr] " + line)
			}
		}
	}()

	// 等待命令完成或超时
	done := make(chan error, 1)
	go func() {
		wg.Wait() // 等待所有输出读取完成
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		result.Duration = time.Since(start)
		mu.Lock()
		result.Output = strings.Join(outputLines, "\n")
		mu.Unlock()
		if err != nil {
			result.Error = err
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			}
		}
	case <-time.After(e.timeout):
		cmd.Process.Kill()
		cmd.Wait()
		result.Duration = time.Since(start)
		mu.Lock()
		result.Output = strings.Join(outputLines, "\n")
		mu.Unlock()
		result.Error = fmt.Errorf("command timed out after %v", e.timeout)
	}

	return result
}

// buildSSHCommand 构建 SSH 命令字符串
func (e *Executor) buildSSHCommand(node *Node, command string, useSudo bool) (string, error) {
	var parts []string
	command = strings.TrimSpace(command)

	// sshpass 前缀（密码认证）
	if node.Password != "" {
		parts = append(parts, shellQuote(e.sshPassPath), "-p", shellQuote(node.Password))
	}

	// SSH 基础选项
	sshOpts := []string{
		"ssh",                            // 命令
		"-o", "StrictHostKeyChecking=no", // 跳过主机密钥检查
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10", // 连接超时
		"-o", "ServerAliveInterval=15", // 保持连接
		"-o", "ServerAliveCountMax=3",
	}

	// 密钥认证
	if node.KeyPath != "" {
		sshOpts = append(sshOpts, "-i", node.KeyPath)
	}

	// 主机和端口
	if node.Port != 22 && node.Port != 0 {
		sshOpts = append(sshOpts, "-p", fmt.Sprintf("%d", node.Port))
	}
	sshOpts = append(sshOpts, fmt.Sprintf("%s@%s", node.User, node.Host))

	parts = append(parts, sshOpts...)

	// 远程命令 - 使用单引号包裹，避免 shell 解析
	// 单引号内的一切都是字面量，不需要转义
	remoteCmd := command
	if useSudo {
		remoteCmd = fmt.Sprintf("sudo sh -c %s", shellQuote(command))
	}

	// 如果命令中包含单引号，需要特殊处理
	// 将单引号替换为 '\'' （结束单引号，转义单引号，重新开始单引号）
	remoteCmd = strings.ReplaceAll(remoteCmd, "'", "'\\''")

	parts = append(parts, fmt.Sprintf("'%s'", remoteCmd))

	return strings.Join(parts, " "), nil
}

// RunOnAllNodes 在所有节点上并发执行命令
// 使用信号量控制并发数，防止连接池耗尽
func (e *Executor) RunOnAllNodes(command string, useSudo bool) []*Result {
	return e.RunOnNodes(e.nodes, command, useSudo)
}

// RunOnNodes 在指定节点上并发执行命令
func (e *Executor) RunOnNodes(nodes []*Node, command string, useSudo bool) []*Result {
	e.logger.Info("Executing command on multiple nodes",
		logger.Fields{
			"node_count":  len(nodes),
			"cmd_preview": formatCommandForLog(command),
			"cmd_lines":   commandLineCount(command),
			"max_conc":    e.maxConcurrent,
		})

	results := make([]*Result, len(nodes))
	var wg sync.WaitGroup

	// 使用信号量控制并发
	semaphore := make(chan struct{}, e.maxConcurrent)

	for i, node := range nodes {
		wg.Add(1)
		semaphore <- struct{}{} // 获取信号量

		go func(idx int, n *Node) {
			defer wg.Done()
			defer func() { <-semaphore }() // 释放信号量

			results[idx] = e.RunOnNode(n, command, useSudo, false)
		}(i, node)
	}

	wg.Wait()
	return results
}

// RunSequential 在所有节点上顺序执行命令
// 用于需要严格顺序的场景（如主节点优先）
func (e *Executor) RunSequential(nodes []*Node, command string, useSudo bool) []*Result {
	e.logger.Info("Executing command sequentially",
		logger.Fields{
			"node_count":  len(nodes),
			"cmd_preview": formatCommandForLog(command),
			"cmd_lines":   commandLineCount(command),
		})

	results := make([]*Result, len(nodes))
	for i, node := range nodes {
		results[i] = e.RunOnNode(node, command, useSudo, false)

		// 如果失败，可以选择中断或继续
		if results[i].Error != nil {
			e.logger.Warn("Command failed in sequential execution",
				logger.Fields{
					"node": node.ID,
					"host": node.Host,
				})
			// 可以选择 break 或继续
		}
	}

	return results
}

func commandLineCount(command string) int {
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(command), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func formatCommandForLog(command string) string {
	lines := strings.Split(strings.TrimSpace(command), "\n")
	formatted := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		formatted = append(formatted, line)
	}

	if len(formatted) == 0 {
		return ""
	}

	const maxLines = 8
	const maxLineWidth = 120
	preview := make([]string, 0, min(len(formatted), maxLines)+1)
	for i, line := range formatted {
		if i == maxLines {
			preview = append(preview, "...+"+strconv.Itoa(len(formatted)-maxLines)+" more lines")
			break
		}
		if len(line) > maxLineWidth {
			line = line[:maxLineWidth-3] + "..."
		}
		preview = append(preview, strconv.Itoa(i+1)+". "+line)
	}

	return strings.Join(preview, "\n")
}

// CopyFile 将文件复制到节点
func (e *Executor) CopyFile(localPath, remotePath string, nodes []*Node) []*Result {
	e.logger.Info("Copying file to nodes",
		logger.Fields{
			"local":  localPath,
			"remote": remotePath,
			"nodes":  len(nodes),
		})

	results := make([]*Result, len(nodes))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.maxConcurrent)

	for i, node := range nodes {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(idx int, n *Node) {
			defer wg.Done()
			defer func() { <-semaphore }()

			start := time.Now()
			result := &Result{
				Node:    n,
				Command: fmt.Sprintf("scp %s %s:%s", localPath, n.Host, remotePath),
			}

			// 构建 SCP 命令
			scpCmd, err := e.buildSCPCommand(n, localPath, remotePath)
			if err != nil {
				result.Error = err
				result.Duration = time.Since(start)
				results[idx] = result
				return
			}

			// 执行 SCP
			cmd := exec.Command("sh", "-c", scpCmd)
			output, err := cmd.CombinedOutput()
			result.Duration = time.Since(start)
			result.Output = string(output)

			if err != nil {
				result.Error = err
				e.logger.Error("Failed to copy file",
					logger.Fields{
						"node":  n.ID,
						"host":  n.Host,
						"error": err,
					})
			} else {
				e.logger.Debug("File copied successfully",
					logger.Fields{
						"node":     n.ID,
						"host":     n.Host,
						"duration": result.Duration,
					})
			}

			results[idx] = result
		}(i, node)
	}

	wg.Wait()
	return results
}

// buildSCPCommand 构建 SCP 命令
func (e *Executor) buildSCPCommand(node *Node, localPath, remotePath string) (string, error) {
	var parts []string

	// sshpass 前缀
	if node.Password != "" {
		parts = append(parts, shellQuote(e.sshPassPath), "-p", shellQuote(node.Password))
	}

	// SCP 选项
	scpOpts := []string{
		"scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
	}

	if node.KeyPath != "" {
		scpOpts = append(scpOpts, "-i", node.KeyPath)
	}

	// 目标路径
	target := fmt.Sprintf("%s@%s:%s", node.User, node.Host, remotePath)
	if node.Port != 22 && node.Port != 0 {
		// SCP 的 -P 参数（注意大写）
		scpOpts = append(scpOpts, "-P", fmt.Sprintf("%d", node.Port))
	}

	parts = append(parts, scpOpts...)
	parts = append(parts, shellQuote(localPath), shellQuote(target))

	return strings.Join(parts, " "), nil
}

// TestConnection 测试节点连接性
// 返回可连接的节点列表
func (e *Executor) TestConnection(nodes []*Node) []*Node {
	e.logger.Info("Testing node connectivity",
		logger.Fields{"count": len(nodes)})

	results := make([]*Node, 0, len(nodes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, e.maxConcurrent)

	for _, node := range nodes {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(n *Node) {
			defer wg.Done()
			defer func() { <-semaphore }()

			// 简单的连接测试
			result := e.RunOnNode(n, "echo 'ok'", false, false)

			mu.Lock()
			defer mu.Unlock()

			if result.Error == nil && strings.Contains(result.Output, "ok") {
				results = append(results, n)
				e.logger.Debug("Node connection successful",
					logger.Fields{"node": n.ID, "host": n.Host})
			} else {
				e.logger.Warn("Node connection failed",
					logger.Fields{
						"node":  n.ID,
						"host":  n.Host,
						"error": result.Error,
					})
			}
		}(node)
	}

	wg.Wait()

	e.logger.Info("Connection test completed",
		logger.Fields{
			"total":      len(nodes),
			"successful": len(results),
			"failed":     len(nodes) - len(results),
		})

	return results
}

// StreamCommand 在节点上执行命令并流式输出
// 适合长时间运行的命令（如编译）
func (e *Executor) StreamCommand(node *Node, command string, useSudo bool,
	outputCallback func(line string)) error {

	sshCmd, err := e.buildSSHCommand(node, command, useSudo)
	if err != nil {
		return err
	}

	cmd := exec.Command("sh", "-c", sshCmd)

	// 创建管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// 读取输出
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		streamOutput(stdout, outputCallback)
	}()

	go func() {
		defer wg.Done()
		streamOutput(stderr, outputCallback)
	}()

	// 等待输出完成
	wg.Wait()

	// 等待命令完成
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}

	return nil
}

// streamOutput 流式读取输出
func streamOutput(reader io.Reader, callback func(line string)) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if callback != nil {
			callback(line)
		}
	}
}

// getLocalIPs 获取本机所有IP地址
func (e *Executor) getLocalIPs() ([]string, error) {
	var localIPs []string

	// 添加标准本地回环地址
	localIPs = append(localIPs, "127.0.0.1", "localhost", "::1")

	// 通过 hostname -I 获取IP地址(Linux)
	cmd := exec.Command("hostname", "-I")
	output, err := cmd.Output()
	if err == nil {
		ips := strings.Fields(string(output))
		localIPs = append(localIPs, ips...)
	}

	// 通过 ifconfig 获取IP地址(作为备用)
	cmd = exec.Command("sh", "-c", "ifconfig | grep 'inet ' | awk '{print $2}'")
	output, err = cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "127.") {
				localIPs = append(localIPs, line)
			}
		}
	}

	// 去重
	uniqueIPs := make(map[string]bool)
	var result []string
	for _, ip := range localIPs {
		if !uniqueIPs[ip] {
			uniqueIPs[ip] = true
			result = append(result, ip)
		}
	}

	return result, nil
}

// isLocalNode 检测节点是否为本机
func (e *Executor) isLocalNode(host string) bool {
	if e.localIPs == nil {
		return false
	}

	for _, localIP := range e.localIPs {
		if host == localIP {
			return true
		}
	}
	return false
}

// IsLocalNode 检测节点是否为本机（公开方法）
func (e *Executor) IsLocalNode(host string) bool {
	return e.isLocalNode(host)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

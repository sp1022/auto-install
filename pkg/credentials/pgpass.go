// Package credentials 提供 PostgreSQL .pgpass 文件管理和密码验证
// 支持 10-50 节点的高并发场景，采用线程安全设计
package credentials

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/example/pg-deploy/pkg/logger"
)

// PGPass 管理 PostgreSQL .pgpass 凭证文件
// PostgreSQL .pgpass 格式: hostname:port:database:username:password
type PGPass struct {
	filePath string
	entries  []*Entry
	mu       sync.RWMutex
	logger   *logger.Logger
}

// Entry 表示 .pgpass 文件中的单个凭证条目
type Entry struct {
	Hostname string
	Port     string
	Database string
	Username string
	Password string
}

// NewPGPass 创建或加载 .pgpass 文件管理器
// 优先使用环境变量 PGPASSFILE，默认为 ~/.pgpass
func NewPGPass(log *logger.Logger) (*PGPass, error) {
	filePath := os.Getenv("PGPASSFILE")
	if filePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		filePath = filepath.Join(homeDir, ".pgpass")
	}

	pg := &PGPass{
		filePath: filePath,
		entries:  make([]*Entry, 0),
		logger:   log,
	}

	if err := pg.Load(); err != nil {
		// 如果文件不存在，创建新文件
		if os.IsNotExist(err) {
			if err := pg.createFile(); err != nil {
				return nil, fmt.Errorf("failed to create .pgpass file: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to load .pgpass: %w", err)
		}
	}

	return pg, nil
}

// createFile 创建新的 .pgpass 文件并设置安全权限
func (p *PGPass) createFile() error {
	// 创建文件
	if err := os.WriteFile(p.filePath, []byte("# PostgreSQL password file\n"), 0600); err != nil {
		return err
	}

	p.logger.Info("Created new .pgpass file", logger.Fields{
		"path": p.filePath,
	})

	return nil
}

// Load 从文件加载凭证条目
// 线程安全：使用写锁保护内部状态
func (p *PGPass) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	file, err := os.Open(p.filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// 检查文件权限（必须只有用户可读写）
	info, err := file.Stat()
	if err != nil {
		return err
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		p.logger.Warn("Insecure permissions on .pgpass file, fixing to 0600",
			logger.Fields{"current": fmt.Sprintf("%#o", perm)})
		if err := os.Chmod(p.filePath, 0600); err != nil {
			return fmt.Errorf("failed to fix permissions: %w", err)
		}
	}

	p.entries = make([]*Entry, 0)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		entry, err := parseEntry(line)
		if err != nil {
			p.logger.Warn("Failed to parse line, skipping",
				logger.Fields{"line": lineNum, "error": err})
			continue
		}

		p.entries = append(p.entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading file: %w", err)
	}

	p.logger.Debug("Loaded .pgpass entries",
		logger.Fields{"count": len(p.entries), "path": p.filePath})

	return nil
}

// parseEntry 解析单行凭证条目
// 格式: hostname:port:database:username:password
func parseEntry(line string) (*Entry, error) {
	// PostgreSQL 支持 * 作为通配符
	parts := strings.Split(line, ":")
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid format, expected 5 fields separated by ':'")
	}

	entry := &Entry{
		Hostname: parts[0],
		Port:     parts[1],
		Database: parts[2],
		Username: parts[3],
		Password: parts[4],
	}

	return entry, nil
}

// Add 添加新的凭证条目
// 线程安全：使用写锁，避免并发写入冲突
func (p *PGPass) Add(hostname, port, database, username, password string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 检查是否已存在完全相同的条目
	for _, existing := range p.entries {
		if existing.Hostname == hostname &&
			existing.Port == port &&
			existing.Database == database &&
			existing.Username == username {
			p.logger.Debug("Entry already exists, updating password",
				logger.Fields{
					"host":     hostname,
					"port":     port,
					"database": database,
					"username": username,
				})
			existing.Password = password
			return p.save()
		}
	}

	// 添加新条目
	newEntry := &Entry{
		Hostname: hostname,
		Port:     port,
		Database: database,
		Username: username,
		Password: password,
	}

	p.entries = append(p.entries, newEntry)
	p.logger.Info("Added credential entry",
		logger.Fields{
			"host":     hostname,
			"port":     port,
			"database": database,
			"username": username,
		})

	return p.save()
}

// Find 查找匹配的凭证条目
// PostgreSQL 匹配规则：
// - hostname: 精确匹配或 *（任意主机）
// - port: 精确匹配或 *
// - database: 精确匹配或 *
// - username: 必须精确匹配
// 线程安全：使用读锁，允许高并发查询
func (p *PGPass) Find(hostname, port, database, username string) (*Entry, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var bestMatch *Entry
	bestScore := -1

	for _, entry := range p.entries {
		// 用户名必须精确匹配
		if entry.Username != username {
			continue
		}

		score := 0

		// 计算匹配分数（精确匹配优先于通配符）
		if entry.Hostname == hostname {
			score += 3
		} else if entry.Hostname == "*" {
			score += 1
		} else {
			continue
		}

		if entry.Port == port {
			score += 2
		} else if entry.Port == "*" {
			score += 1
		} else {
			continue
		}

		if entry.Database == database {
			score += 2
		} else if entry.Database == "*" {
			score += 1
		} else {
			continue
		}

		// 选择匹配度最高的条目
		if score > bestScore {
			bestMatch = entry
			bestScore = score
		}
	}

	if bestMatch == nil {
		return nil, fmt.Errorf("no matching entry found for %s@%s:%s/%s",
			username, hostname, port, database)
	}

	return bestMatch, nil
}

// FindByPattern 使用通配符模式查找凭证
// 支持部分匹配，便于批量操作
// 线程安全：使用读锁
func (p *PGPass) FindByPattern(hostnamePattern, portPattern, databasePattern, username string) ([]*Entry, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var results []*Entry

	for _, entry := range p.entries {
		// 用户名必须精确匹配
		if entry.Username != username {
			continue
		}

		// 检查是否匹配所有模式
		if !matchPattern(entry.Hostname, hostnamePattern) {
			continue
		}
		if !matchPattern(entry.Port, portPattern) {
			continue
		}
		if !matchPattern(entry.Database, databasePattern) {
			continue
		}

		results = append(results, entry)
	}

	return results, nil
}

// matchPattern 检查值是否匹配模式（支持 * 通配符）
func matchPattern(value, pattern string) bool {
	if pattern == "*" {
		return true
	}

	// 转换为正则表达式
	regexPattern := regexp.QuoteMeta(pattern)
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, ".*")

	matched, _ := regexp.MatchString("^"+regexPattern+"$", value)
	return matched
}

// Remove 移除指定的凭证条目
// 线程安全：使用写锁
func (p *PGPass) Remove(hostname, port, database, username string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	originalLen := len(p.entries)
	newEntries := make([]*Entry, 0, len(p.entries))

	for _, entry := range p.entries {
		if entry.Hostname == hostname &&
			entry.Port == port &&
			entry.Database == database &&
			entry.Username == username {
			p.logger.Info("Removed credential entry",
				logger.Fields{
					"host":     hostname,
					"port":     port,
					"database": database,
					"username": username,
				})
			continue
		}
		newEntries = append(newEntries, entry)
	}

	if len(newEntries) == originalLen {
		return fmt.Errorf("entry not found")
	}

	p.entries = newEntries
	return p.save()
}

// save 将凭证条目保存到文件
// 必须在持有写锁的情况下调用
func (p *PGPass) save() error {
	// 创建临时文件
	tempFile := p.filePath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// 写入条目
	for _, entry := range p.entries {
		line := fmt.Sprintf("%s:%s:%s:%s:%s\n",
			entry.Hostname, entry.Port, entry.Database,
			entry.Username, entry.Password)
		if _, err := file.WriteString(line); err != nil {
			file.Close()
			os.Remove(tempFile)
			return fmt.Errorf("failed to write entry: %w", err)
		}
	}

	if err := file.Close(); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to close file: %w", err)
	}

	// 原子性重命名
	if err := os.Rename(tempFile, p.filePath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	// 设置安全权限
	if err := os.Chmod(p.filePath, 0600); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	p.logger.Debug("Saved .pgpass file",
		logger.Fields{"entries": len(p.entries), "path": p.filePath})

	return nil
}

// List 列出所有凭证条目
// 线程安全：使用读锁
func (p *PGPass) List() []*Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 返回副本，避免外部修改
	entries := make([]*Entry, len(p.entries))
	copy(entries, p.entries)
	return entries
}

// ValidateConnection 验证数据库连接凭据
// 使用 .pgpass 中的密码尝试连接
func (p *PGPass) ValidateConnection(hostname, port, database, username string) error {
	entry, err := p.Find(hostname, port, database, username)
	if err != nil {
		return fmt.Errorf("credentials not found: %w", err)
	}

	// TODO: 在后续阶段实现实际的连接验证
	// 这里使用 psql 进行简单的连接测试
	// command := exec.Command("psql",
	// 	"-h", hostname,
	// 	"-p", port,
	// 	"-d", database,
	// 	"-U", username,
	// 	"-c", "SELECT 1")

	p.logger.Debug("Credential entry found",
		logger.Fields{
			"host":     hostname,
			"port":     port,
			"database": database,
			"username": username,
		})

	// 验证密码不为空
	if entry.Password == "" {
		return fmt.Errorf("empty password")
	}

	return nil
}

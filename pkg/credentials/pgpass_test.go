package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/example/pg-deploy/pkg/logger"
)

func TestPGPass_New(t *testing.T) {
	// 创建临时目录
	tmpDir := t.TempDir()

	// 设置临时主目录
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	// 创建日志记录器
	log := logger.NewDefault()

	// 测试创建新文件
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 检查文件是否存在
	expectedPath := filepath.Join(tmpDir, ".pgpass")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("pgpass file was not created at %s", expectedPath)
	}

	// 检查权限
	info, err := os.Stat(expectedPath)
	if err != nil {
		t.Fatalf("failed to stat pgpass file: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Errorf("expected permissions 0600, got %#o", info.Mode().Perm())
	}

	_ = pg
}

func TestPGPass_AddAndFind(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	log := logger.NewDefault()
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 添加凭证
	err = pg.Add("localhost", "5432", "postgres", "postgres", "testpass")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 查找凭证
	entry, err := pg.Find("localhost", "5432", "postgres", "postgres")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}

	if entry.Password != "testpass" {
		t.Errorf("expected password 'testpass', got '%s'", entry.Password)
	}
}

func TestPGPass_WildcardMatch(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	log := logger.NewDefault()
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 添加通配符凭证
	err = pg.Add("*", "*", "*", "postgres", "wildcardpass")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 查找应该匹配通配符
	entry, err := pg.Find("anyhost", "9999", "anydb", "postgres")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}

	if entry.Password != "wildcardpass" {
		t.Errorf("expected password 'wildcardpass', got '%s'", entry.Password)
	}
}

func TestPGPrepass_BestMatch(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	log := logger.NewDefault()
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 添加多个凭证，精确匹配应该优先
	_ = pg.Add("*", "*", "*", "postgres", "wildcard")
	_ = pg.Add("localhost", "*", "*", "postgres", "hostmatch")
	_ = pg.Add("localhost", "5432", "*", "postgres", "portmatch")
	_ = pg.Add("localhost", "5432", "postgres", "postgres", "exactmatch")

	// 应该返回精确匹配
	entry, err := pg.Find("localhost", "5432", "postgres", "postgres")
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}

	if entry.Password != "exactmatch" {
		t.Errorf("expected password 'exactmatch', got '%s'", entry.Password)
	}
}

func TestPGPass_Remove(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	log := logger.NewDefault()
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 添加凭证
	_ = pg.Add("localhost", "5432", "postgres", "postgres", "testpass")

	// 移除凭证
	err = pg.Remove("localhost", "5432", "postgres", "postgres")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 验证已移除
	_, err = pg.Find("localhost", "5432", "postgres", "postgres")
	if err == nil {
		t.Error("expected error when finding removed entry")
	}
}

func TestPGPass_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer os.Setenv("HOME", originalHome)
	os.Setenv("HOME", tmpDir)

	log := logger.NewDefault()
	pg, err := NewPGPass(log)
	if err != nil {
		t.Fatalf("NewPGPass failed: %v", err)
	}

	// 并发写入
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			host := fmt.Sprintf("host%d", idx)
			_ = pg.Add(host, "5432", "postgres", "postgres", "pass")
			done <- true
		}(i)
	}

	// 等待所有写入完成
	for i := 0; i < 10; i++ {
		<-done
	}

	// 验证条目数
	entries := pg.List()
	if len(entries) != 10 {
		t.Errorf("expected 10 entries, got %d", len(entries))
	}
}

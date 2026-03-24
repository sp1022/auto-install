// Package topology 提供辅助函数
package topology

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// generateRandomPassword 生成安全的随机密码
func generateRandomPassword(length int) (string, error) {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random password: %w", err)
	}
	return hex.EncodeToString(bytes)[:length], nil
}

// join 连接字符串数组
func join(strs []string, sep string) string {
	return strings.Join(strs, sep)
}

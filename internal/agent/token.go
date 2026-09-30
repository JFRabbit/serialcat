package agent

import (
	"crypto/rand"
	"encoding/hex"
)

// RandomToken 生成随机十六进制令牌（长度 bytes 字节的随机熵，编码为 hex）。
// 用于 --enable-write 开启后的写鉴权，避免明文/硬编码。
func RandomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("随机数生成失败: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

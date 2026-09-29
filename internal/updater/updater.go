// Package updater 实现 CLI 的自更新：从更新源检测版本、下载新二进制并替换当前可执行文件。
// 设计上复用 ag=git mr 的 update 机制：MinIO HTTP 更新源 + 平台替换策略。
package updater

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// VersionInfo 表示更新源 version.json 的内容。
type VersionInfo struct {
	Latest    string            `json:"latest"`
	Platforms map[string]string `json:"platforms"` // 平台("darwin_arm64"等) -> 相对路径或绝对 URL
	SHA256    map[string]string `json:"sha256,omitempty"`
}

// Source 抽象更新源，便于扩展 github 等实现。
type Source interface {
	CheckLatest(ctx context.Context) (*VersionInfo, error)
	Download(ctx context.Context, url, dst string) error
}

// MinioSource 通过 HTTP GET 从 MinIO 获取版本信息与二进制。
type MinioSource struct {
	baseURL string // 地址前缀, 如 http://ip:port/bucket
	appName string // 工具命名空间, 版本信息位于 {baseURL}/{appName}/version.json
	client  *http.Client
}

// NewSource 根据类型创建更新源实例。insecure 为 true 时跳过 TLS 证书校验（自签名证书场景）。
func NewSource(sourceType, baseURL, appName string, insecure bool) (Source, error) {
	switch sourceType {
	case "", "minio":
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 由用户显式开启
		}
		return &MinioSource{
			baseURL: strings.TrimRight(baseURL, "/"),
			appName: appName,
			client:  &http.Client{Timeout: 5 * time.Minute, Transport: transport},
		}, nil
	default:
		return nil, fmt.Errorf("不支持的更新源类型: %s", sourceType)
	}
}

// IsTLSCertError 判断错误是否为 TLS 证书校验类错误，用于自签名证书场景自动降级重试。
func IsTLSCertError(err error) bool {
	if err == nil {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "certificate") && strings.Contains(msg, "authority")
}

// CheckLatest 请求 version.json 获取最新版本信息。
func (s *MinioSource) CheckLatest(ctx context.Context) (*VersionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/"+s.appName+"/version.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求版本信息失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求版本信息失败: HTTP %d", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("解析版本信息失败: %w", err)
	}
	if info.Latest == "" {
		return nil, fmt.Errorf("版本信息中缺少 latest 字段")
	}
	return &info, nil
}

// ResolveURL 将 version.json 中的路径解析为完整下载地址。
// 绝对 URL 原样返回，相对路径与前缀拼接。
func ResolveURL(baseURL, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// Download 下载文件到本地路径，并赋执行权限。
func (s *MinioSource) Download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("写入临时文件失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("写入临时文件失败: %w", closeErr)
	}
	return nil
}

// VerifySHA256 校验文件的 SHA256。
func VerifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA256 校验失败: 期望 %s, 实际 %s", expected, actual)
	}
	return nil
}

// CompareVersions 比较两个语义化版本号（不带 v 前缀）。
// 返回 1 表示 a > b, -1 表示 a < b, 0 表示相等。
func CompareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x > y {
			return 1, nil
		}
		if x < y {
			return -1, nil
		}
	}
	return 0, nil
}

// parseVersion 将 "1.2.3" 解析为数字段。
func parseVersion(v string) ([]int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, fmt.Errorf("版本号为空")
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("非法版本号: %s", v)
		}
		nums = append(nums, n)
	}
	return nums, nil
}
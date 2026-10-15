// Package updater implements CLI self-update: checks the version from an update source, downloads the new binary, and replaces the current executable.
// The design reuses the update mechanism of ag=git mr: MinIO HTTP update source + platform replacement strategy.
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
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// VersionInfo represents the content of the update source's version.json.
type VersionInfo struct {
	Latest    string            `json:"latest"`
	Platforms map[string]string `json:"platforms"` // platform ("darwin_arm64" etc.) -> relative path or absolute URL
	SHA256    map[string]string `json:"sha256,omitempty"`
}

// Source abstracts an update source, making it easy to add implementations like github.
type Source interface {
	CheckLatest(ctx context.Context) (*VersionInfo, error)
	Download(ctx context.Context, url, dst string) error
}

// MinioSource fetches version info and binaries from MinIO over HTTP GET.
type MinioSource struct {
	baseURL string // address prefix, e.g. http://ip:port/bucket
	appName string // tool namespace; version info lives at {baseURL}/{appName}/version.json
	client  *http.Client
}

// NewSource creates an update source instance by type. When insecure is true, TLS certificate verification is skipped (self-signed certificate scenario).
// For sourceType "github", baseURL carries the GitHub repository in the form owner/repo (e.g. "JFRabbit/serialcat") and appName is ignored.
func NewSource(sourceType, baseURL, appName string, insecure bool) (Source, error) {
	switch sourceType {
	case "", "minio":
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicitly enabled by the user
		}
		return &MinioSource{
			baseURL: strings.TrimRight(baseURL, "/"),
			appName: appName,
			client:  &http.Client{Timeout: 5 * time.Minute, Transport: transport},
		}, nil
	case "github":
		return NewGitHubSource(baseURL, insecure), nil
	default:
		return nil, fmt.Errorf("unsupported update source type: %s", sourceType)
	}
}

// IsTLSCertError reports whether an error is a TLS certificate verification error, used for automatic fallback retry in self-signed certificate scenarios.
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

// CheckLatest requests version.json to get the latest version info.
func (s *MinioSource) CheckLatest(ctx context.Context) (*VersionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/"+s.appName+"/version.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request version info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to request version info: HTTP %d", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to parse version info: %w", err)
	}
	if info.Latest == "" {
		return nil, fmt.Errorf("version info is missing the latest field")
	}
	return &info, nil
}

// ResolveURL resolves a path in version.json into a full download URL.
// Absolute URLs are returned as-is; relative paths are joined with the prefix.
func ResolveURL(baseURL, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// Download downloads a file to a local path and sets the executable permission.
func (s *MinioSource) Download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("failed to write temporary file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("failed to write temporary file: %w", closeErr)
	}
	return nil
}

// GitHubSource fetches version info and binaries from GitHub Releases.
// Asset naming convention: {app}-{os}-{arch} (e.g. serialcat-linux-amd64),
// with a ".sha256" sibling asset carrying the checksum; windows binaries use
// the ".exe" suffix (e.g. serialcat-windows-amd64.exe).
type GitHubSource struct {
	repo   string // owner/repo, e.g. "JFRabbit/serialcat"
	apiURL string // GitHub API base URL, defaults to https://api.github.com
	client *http.Client
	assets map[string]string // asset name -> browser download URL (includes .sha256 assets)
}

// NewGitHubSource creates a GitHubSource for the given owner/repo. When insecure is
// true, TLS certificate verification is skipped (self-signed proxy scenario).
func NewGitHubSource(ownerRepo string, insecure bool) *GitHubSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicitly enabled by the user
	}
	return &GitHubSource{
		repo:   ownerRepo,
		apiURL: "https://api.github.com",
		client: &http.Client{Timeout: 5 * time.Minute, Transport: transport},
	}
}

// CheckLatest requests the latest release from GitHub and parses tag_name and assets.
func (s *GitHubSource) CheckLatest(ctx context.Context) (*VersionInfo, error) {
	endpoint := s.apiURL + "/repos/" + s.repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "serialcat")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to request latest release: HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse latest release: %w", err)
	}
	if release.TagName == "" {
		return nil, fmt.Errorf("latest release is missing the tag_name field")
	}
	info := &VersionInfo{
		Latest:    strings.TrimPrefix(release.TagName, "v"),
		Platforms: make(map[string]string),
	}
	s.assets = make(map[string]string, len(release.Assets))
	for _, asset := range release.Assets {
		s.assets[asset.Name] = asset.BrowserDownloadURL
		if strings.HasSuffix(asset.Name, ".sha256") {
			continue
		}
		if key, ok := assetPlatformKey(asset.Name); ok {
			info.Platforms[key] = asset.BrowserDownloadURL
		}
	}
	return info, nil
}

// Download downloads a file to a local path and verifies its integrity against the
// matching ".sha256" asset; on any failure the partial file is removed.
func (s *GitHubSource) Download(ctx context.Context, url, dst string) error {
	if err := s.downloadFile(ctx, url, dst); err != nil {
		return err
	}
	// Integrity check: derive the matching .sha256 asset from the binary name.
	binaryName := binaryAssetName(url)
	shaURL, ok := s.assets[binaryName+".sha256"]
	if !ok {
		_ = os.Remove(dst)
		return fmt.Errorf("integrity check failed: no .sha256 asset found for %s", binaryName)
	}
	body, err := s.fetch(ctx, shaURL)
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("integrity check failed: %w", err)
	}
	expected := sha256HexFromLine(string(body))
	if expected == "" {
		_ = os.Remove(dst)
		return fmt.Errorf("integrity check failed: invalid .sha256 content for %s", binaryName)
	}
	if err := VerifySHA256(dst, expected); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("integrity check failed: %w", err)
	}
	return nil
}

// downloadFile downloads a URL to a local path and sets the executable permission.
func (s *GitHubSource) downloadFile(ctx context.Context, url, dst string) error {
	resp, err := s.fetchResp(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("failed to write temporary file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("failed to write temporary file: %w", closeErr)
	}
	return nil
}

// fetch downloads a URL and returns the full response body.
func (s *GitHubSource) fetch(ctx context.Context, url string) ([]byte, error) {
	resp, err := s.fetchResp(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, nil
}

// fetchResp issues a GET request and returns the 200 response (body owned by the caller).
func (s *GitHubSource) fetchResp(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// assetPlatformKey converts an asset name following the {app}-{os}-{arch} convention
// (e.g. serialcat-linux-amd64, serialcat-windows-amd64.exe) into a platform key such
// as linux_amd64. It reports ok=false for names outside the convention.
func assetPlatformKey(name string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(name, ".exe"), "serialcat-")
	if !ok || rest == "" {
		return "", false
	}
	key := strings.ReplaceAll(rest, "-", "_")
	if !strings.Contains(key, "_") {
		return "", false
	}
	return key, true
}

// binaryAssetName returns the last path segment of a download URL, used to derive the
// matching .sha256 asset name (binary name + ".sha256").
func binaryAssetName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return rawURL
	}
	if i := strings.LastIndex(u.Path, "/"); i >= 0 {
		return u.Path[i+1:]
	}
	return u.Path
}

// sha256HexFromLine extracts the hex checksum from a .sha256 line formatted as
// "<hex>  <filename>" (two-space separated). The filename is not validated.
func sha256HexFromLine(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// VerifySHA256 verifies the SHA256 of a file.
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
		return fmt.Errorf("SHA256 verification failed: expected %s, got %s", expected, actual)
	}
	return nil
}

// CompareVersions compares two semver version strings (without the v prefix).
// Returns 1 when a > b, -1 when a < b, 0 when equal.
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

// parseVersion parses "1.2.3" into numeric segments.
func parseVersion(v string) ([]int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, fmt.Errorf("empty version")
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid version: %s", v)
		}
		nums = append(nums, n)
	}
	return nums, nil
}

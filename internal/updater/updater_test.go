package updater

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompareVersions table-driven coverage of version comparison: equal/greater/less, v prefix, invalid version strings.
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		want    int
		wantErr bool
	}{
		// equal
		{name: "fully equal", a: "1.2.3", b: "1.2.3", want: 0},
		{name: "equal with v prefix", a: "v1.2.3", b: "1.2.3", want: 0},
		{name: "equal with v prefix on both sides", a: "v2.0.0", b: "2.0.0", want: 0},
		// greater
		{name: "greater major", a: "2.0.0", b: "1.9.9", want: 1},
		{name: "greater minor", a: "1.3.0", b: "1.2.9", want: 1},
		{name: "greater patch", a: "1.2.4", b: "1.2.3", want: 1},
		{name: "greater with v prefix", a: "v1.2.3", b: "1.2.2", want: 1},
		// less
		{name: "less major", a: "1.0.0", b: "2.0.0", want: -1},
		{name: "less minor", a: "1.2.9", b: "1.3.0", want: -1},
		{name: "less patch", a: "1.2.3", b: "1.2.4", want: -1},
		{name: "less with v prefix", a: "1.2.3", b: "v1.2.4", want: -1},
		// inconsistent segment counts (missing segments treated as 0)
		{name: "fewer segments equal with zero padding", a: "1.2", b: "1.2.0", want: 0},
		{name: "fewer segments less with zero padding", a: "1.2", b: "1.2.1", want: -1},
		// invalid version strings
		{name: "a invalid", a: "abc", b: "1.2.3", wantErr: true},
		{name: "b invalid", a: "1.2.3", b: "1.x.3", wantErr: true},
		{name: "a empty", a: "", b: "1.2.3", wantErr: true},
		{name: "v prefix only", a: "v", b: "1.2.3", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareVersions(tt.a, tt.b)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CompareVersions(%q, %q) expected an error, got=%d", tt.a, tt.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompareVersions(%q, %q) unexpected error: %v", tt.a, tt.b, err)
			}
			if got != tt.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestParseVersion table-driven coverage of the private function parseVersion: normal parsing, v prefix, whitespace, invalid input.
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{name: "three segments", input: "1.2.3", want: []int{1, 2, 3}},
		{name: "with v prefix", input: "v10.20.30", want: []int{10, 20, 30}},
		{name: "with whitespace and v prefix", input: "  v2.1 ", want: []int{2, 1}},
		{name: "single segment", input: "3", want: []int{3}},
		{name: "empty string", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "v prefix only", input: "v", wantErr: true},
		{name: "non-numeric segment", input: "1.a.3", wantErr: true},
		{name: "negative segment parses via Atoi", input: "1.-2.3", want: []int{1, -2, 3}},
		{name: "space before v still invalid", input: " x.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersion(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVersion(%q) expected an error, got=%v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVersion(%q) unexpected error: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseVersion(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestResolveURL table-driven coverage: absolute URLs returned as-is, relative path joining, and normalization of the trailing slash in baseURL and leading slash in path.
func TestResolveURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{name: "http absolute URL as-is", baseURL: "http://a/bucket", path: "http://cdn/x.exe", want: "http://cdn/x.exe"},
		{name: "https absolute URL as-is", baseURL: "http://a/bucket", path: "https://cdn/x.exe", want: "https://cdn/x.exe"},
		{name: "relative path joining", baseURL: "http://a/bucket", path: "dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "baseURL trailing slash normalized", baseURL: "http://a/bucket/", path: "dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "path leading slash normalized", baseURL: "http://a/bucket", path: "/dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "both slashes normalized", baseURL: "http://a/bucket/", path: "/dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "nested relative path", baseURL: "http://a:9000/b", path: "app/v1/serialcat.exe", want: "http://a:9000/b/app/v1/serialcat.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveURL(tt.baseURL, tt.path); got != tt.want {
				t.Errorf("ResolveURL(%q, %q) = %q, want %q", tt.baseURL, tt.path, got, tt.want)
			}
		})
	}
}

// TestIsTLSCertError table-driven coverage: nil, certificate verification error type, string matching and non-matching branches.
func TestIsTLSCertError(t *testing.T) {
	// Build a tls.CertificateVerificationError to simulate a certificate verification failure.
	certErr := &tls.CertificateVerificationError{Err: errors.New("x509: certificate signed by unknown authority")}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "certificate verification error type", err: certErr, want: true},
		{name: "wrapped certificate verification error", err: fmt.Errorf("failed to request version info: %w", certErr), want: true},
		{name: "string contains certificate and authority", err: errors.New("Get https://x: x509: certificate signed by unknown authority"), want: true},
		{name: "string contains certificate only", err: errors.New("certificate error happened"), want: false},
		{name: "string contains authority only", err: errors.New("unknown authority"), want: false},
		{name: "string contains neither", err: errors.New("connection refused"), want: false},
		{name: "uppercase string matches (lowercased)", err: errors.New("Certificate signed by unknown Authority"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTLSCertError(tt.err); got != tt.want {
				t.Errorf("IsTLSCertError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// gitHubTestServer starts an httptest server mocking the GitHub releases API: the
// /repos/{owner}/{repo}/releases/latest endpoint serves a release JSON with the given
// tag and assets; binary/sha256 asset paths serve their recorded content. It also
// records the Accept and User-Agent headers of the API request.
func gitHubTestServer(t *testing.T, tag string, assets map[string]string, contents map[string][]byte) (*httptest.Server, *string, *string) {
	t.Helper()
	var gotAccept, gotUA string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/JFRabbit/serialcat/releases/latest":
			gotAccept = r.Header.Get("Accept")
			gotUA = r.Header.Get("User-Agent")
			var b strings.Builder
			b.WriteString(`{"tag_name":"` + tag + `","assets":[`)
			i := 0
			for name := range assets {
				if i > 0 {
					b.WriteString(",")
				}
				i++
				b.WriteString(`{"name":"` + name + `","browser_download_url":"` + srv.URL + `/` + name + `"}`)
			}
			b.WriteString(`]}`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(b.String()))
		default:
			name := strings.TrimPrefix(r.URL.Path, "/")
			content, ok := contents[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(content)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gotAccept, &gotUA
}

// Covers GitHubSource.CheckLatest: tag_name maps to Latest with the v prefix stripped,
// binary assets map to Platforms, .sha256 assets are excluded from Platforms but kept
// in the internal asset map, and the GitHub API request headers are set.
func TestGitHubSourceCheckLatest(t *testing.T) {
	assets := map[string]string{
		"serialcat-linux-amd64":         "serialcat-linux-amd64",
		"serialcat-linux-amd64.sha256":  "serialcat-linux-amd64.sha256",
		"serialcat-windows-amd64.exe":   "serialcat-windows-amd64.exe",
		"serialcat-darwin-arm64":        "serialcat-darwin-arm64",
		"serialcat-darwin-arm64.sha256": "serialcat-darwin-arm64.sha256",
	}
	srv, gotAccept, gotUA := gitHubTestServer(t, "v1.2.3", assets, nil)

	src := &GitHubSource{repo: "JFRabbit/serialcat", apiURL: srv.URL, client: srv.Client()}
	info, err := src.CheckLatest(context.Background())
	if err != nil {
		t.Fatalf("CheckLatest unexpected error: %v", err)
	}
	if info.Latest != "1.2.3" {
		t.Errorf("Latest = %q, want %q", info.Latest, "1.2.3")
	}
	if info.SHA256 != nil {
		t.Errorf("SHA256 = %v, want nil", info.SHA256)
	}
	wantPlatforms := map[string]string{
		"linux_amd64":   srv.URL + "/serialcat-linux-amd64",
		"windows_amd64": srv.URL + "/serialcat-windows-amd64.exe",
		"darwin_arm64":  srv.URL + "/serialcat-darwin-arm64",
	}
	if len(info.Platforms) != len(wantPlatforms) {
		t.Fatalf("Platforms = %v, want %d entries", info.Platforms, len(wantPlatforms))
	}
	for key, want := range wantPlatforms {
		if got := info.Platforms[key]; got != want {
			t.Errorf("Platforms[%q] = %q, want %q", key, got, want)
		}
	}
	// The internal asset map must also keep .sha256 assets for Download's integrity check.
	if _, ok := src.assets["serialcat-linux-amd64.sha256"]; !ok {
		t.Error("assets map is missing the .sha256 asset")
	}
	if *gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept header = %q, want %q", *gotAccept, "application/vnd.github+json")
	}
	if *gotUA != "serialcat" {
		t.Errorf("User-Agent header = %q, want %q", *gotUA, "serialcat")
	}
}

// Covers GitHubSource.Download success path: the binary is downloaded and verified
// against its .sha256 asset, and the destination keeps the downloaded content.
func TestGitHubSourceDownloadSuccess(t *testing.T) {
	content := []byte("release-binary-content")
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])
	assets := map[string]string{
		"serialcat-linux-amd64":        "serialcat-linux-amd64",
		"serialcat-linux-amd64.sha256": "serialcat-linux-amd64.sha256",
	}
	contents := map[string][]byte{
		"serialcat-linux-amd64":        content,
		"serialcat-linux-amd64.sha256": []byte(hexSum + "  serialcat-linux-amd64\n"),
	}
	srv, _, _ := gitHubTestServer(t, "v1.2.3", assets, contents)
	src := &GitHubSource{repo: "JFRabbit/serialcat", apiURL: srv.URL, client: srv.Client()}
	if _, err := src.CheckLatest(context.Background()); err != nil {
		t.Fatalf("CheckLatest unexpected error: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "serialcat-linux-amd64")
	downloadURL := srv.URL + "/serialcat-linux-amd64"
	if err := src.Download(context.Background(), downloadURL, dst); err != nil {
		t.Fatalf("Download unexpected error: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
}

// Covers GitHubSource.Download failure path: a SHA256 mismatch reports an error and
// removes the partially downloaded file.
func TestGitHubSourceDownloadSHA256Mismatch(t *testing.T) {
	assets := map[string]string{
		"serialcat-linux-amd64":        "serialcat-linux-amd64",
		"serialcat-linux-amd64.sha256": "serialcat-linux-amd64.sha256",
	}
	contents := map[string][]byte{
		"serialcat-linux-amd64":        []byte("tampered-binary"),
		"serialcat-linux-amd64.sha256": []byte("deadbeef  serialcat-linux-amd64\n"),
	}
	srv, _, _ := gitHubTestServer(t, "v1.2.3", assets, contents)
	src := &GitHubSource{repo: "JFRabbit/serialcat", apiURL: srv.URL, client: srv.Client()}
	if _, err := src.CheckLatest(context.Background()); err != nil {
		t.Fatalf("CheckLatest unexpected error: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "serialcat-linux-amd64")
	downloadURL := srv.URL + "/serialcat-linux-amd64"
	err := src.Download(context.Background(), downloadURL, dst)
	if err == nil {
		t.Fatal("expected integrity-check error, got nil")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Errorf("error should mention integrity check failure, got: %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("partial file was not removed, stat err = %v", statErr)
	}
}

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JFRabbit/serialcat/internal/updater"
)

// fakeSource implements updater.Source, with function fields controlling each method's behavior.
type fakeSource struct {
	checkLatest func(ctx context.Context) (*updater.VersionInfo, error)
	download    func(ctx context.Context, url, dst string) error
}

func (f *fakeSource) CheckLatest(ctx context.Context) (*updater.VersionInfo, error) {
	return f.checkLatest(ctx)
}

func (f *fakeSource) Download(ctx context.Context, url, dst string) error {
	return f.download(ctx, url, dst)
}

// updateTestEnv injects a mock update source and executable path, and resets
// package-level state; it returns a restore function.
// runUpdate under test depends on several package-level variables
// (Version/updateSourceURL/insecure/newUpdaterSource/getExecutablePath), so they
// are saved and restored around each test case to avoid state leaking between tests.
func updateTestEnv(t *testing.T, src updater.Source, exePath string, exeErr error) (restore func()) {
	t.Helper()

	oldVersion := Version
	oldSourceURL := updateSourceURL
	oldInsecure := insecure
	oldFactory := newUpdaterSource
	oldExePath := getExecutablePath

	newUpdaterSource = func(baseURL string, insecure bool) (updater.Source, error) {
		return src, nil
	}
	getExecutablePath = func() (string, error) { return exePath, exeErr }

	return func() {
		Version = oldVersion
		updateSourceURL = oldSourceURL
		insecure = oldInsecure
		newUpdaterSource = oldFactory
		getExecutablePath = oldExePath
	}
}

// newVersionInfo builds a VersionInfo containing only the current platform.
func newVersionInfo(latest, path, sha string) *updater.VersionInfo {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	return &updater.VersionInfo{
		Latest:    latest,
		Platforms: map[string]string{platform: path},
		SHA256:    map[string]string{platform: sha},
	}
}

// Covers the branch: un-injected version info (local build) errors out without touching the update source.
func TestRunUpdateVersionNotInjected(t *testing.T) {
	restore := updateTestEnv(t, &fakeSource{}, "", nil)
	defer restore()
	Version = "unset"

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("expected version-not-injected error, got nil")
	}
}

// Covers the branch: no update source URL configured (no flag / env / compile-time injection) errors out.
func TestRunUpdateNoSourceURL(t *testing.T) {
	restore := updateTestEnv(t, &fakeSource{}, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = ""
	t.Setenv("SERIALCAT_UPDATE_URL", "")
	oldDefault := DefaultUpdateURL
	DefaultUpdateURL = "unset"
	defer func() { DefaultUpdateURL = oldDefault }()

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("expected missing-update-source error, got nil")
	}
}

// Covers the branch: CheckLatest fails, the error is returned as-is.
func TestRunUpdateCheckLatestFailed(t *testing.T) {
	wantErr := errors.New("failed to fetch version info")
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return nil, wantErr
		},
	}
	restore := updateTestEnv(t, src, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// Covers the branch: version comparison fails (the update source returns an invalid version number).
func TestRunUpdateCompareVersionFailed(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("not-a-version", "bin/serialcat", ""), nil
		},
	}
	restore := updateTestEnv(t, src, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("expected version-comparison error, got nil")
	}
}

// Covers the branch: already the latest version (cmp <= 0 and not force), returns normally.
func TestRunUpdateAlreadyLatest(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("1.0.0", "bin/serialcat", ""), nil
		},
	}
	restore := updateTestEnv(t, src, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// Covers the branch: force mode proceeds with the download flow even when already
// latest; after a successful download the executable is replaced and nil is returned
// (the temp file should be cleaned up).
func TestRunUpdateForceDownloadSuccess(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "serialcat")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("new-binary")
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("1.0.0", "bin/serialcat", ""), nil
		},
		download: func(ctx context.Context, url, dst string) error {
			if dst != exe+".new" {
				t.Errorf("download target = %q, want %q", dst, exe+".new")
			}
			return os.WriteFile(dst, content, 0o644)
		},
	}
	restore := updateTestEnv(t, src, exe, nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, true, false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content after replacement = %q, want %q", got, content)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("temp file was not cleaned up, stat err = %v", err)
	}
}

// Covers the branch: a new version is found and checkOnly=true; prints a notice and returns nil without downloading.
func TestRunUpdateCheckOnly(t *testing.T) {
	downloadCalled := false
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/serialcat", ""), nil
		},
		download: func(ctx context.Context, url, dst string) error {
			downloadCalled = true
			return nil
		},
	}
	restore := updateTestEnv(t, src, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(true, false, false); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if downloadCalled {
		t.Fatal("download should not be triggered in check-only mode")
	}
}

// Covers the branch: the update source provides no download for the current platform.
func TestRunUpdatePlatformMissing(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			// Platforms is intentionally empty, without the current platform.
			return &updater.VersionInfo{Latest: "2.0.0", Platforms: map[string]string{}}, nil
		},
	}
	restore := updateTestEnv(t, src, "/tmp/serialcat", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	err := runUpdate(false, false, false)
	if err == nil {
		t.Fatal("expected platform-missing error, got nil")
	}
	if !strings.Contains(err.Error(), runtime.GOOS+"_"+runtime.GOARCH) {
		t.Fatalf("error should mention the current platform, got: %v", err)
	}
}

// Covers the branch: getting the executable path fails, the error is returned as-is.
func TestRunUpdateExePathFailed(t *testing.T) {
	wantErr := errors.New("executable not found")
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/serialcat", ""), nil
		},
	}
	restore := updateTestEnv(t, src, "", wantErr)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

// Covers the branch: download fails, the error is returned as-is and the executable is not replaced.
func TestRunUpdateDownloadFailed(t *testing.T) {
	wantErr := errors.New("download timeout")
	exe := filepath.Join(t.TempDir(), "serialcat")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/serialcat", ""), nil
		},
		download: func(ctx context.Context, url, dst string) error {
			return wantErr
		},
	}
	restore := updateTestEnv(t, src, exe, nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("executable content was modified: %q", got)
	}
}

// Covers the branch: SHA256 verification fails after a successful download; errors and does not replace the executable.
func TestRunUpdateSHA256Mismatch(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "serialcat")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/serialcat", "deadbeef"), nil
		},
		download: func(ctx context.Context, url, dst string) error {
			return os.WriteFile(dst, []byte("tampered"), 0o644)
		},
	}
	restore := updateTestEnv(t, src, exe, nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("expected verification-failure error, got nil")
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("executable content was modified: %q", got)
	}
}

// Covers the withTLSCertFallback branch: update source factory creation fails; the
// error is returned as-is and the callback is not executed.
func TestRunUpdateSourceFactoryFailed(t *testing.T) {
	wantErr := errors.New("unsupported update source type")
	oldFactory := newUpdaterSource
	defer func() { newUpdaterSource = oldFactory }()
	newUpdaterSource = func(baseURL string, insecure bool) (updater.Source, error) {
		return nil, wantErr
	}

	called := false
	err := withTLSCertFallback(context.Background(), false, "http://x", func(src updater.Source, c context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if called {
		t.Fatal("callback should not run when factory creation fails")
	}
}

// Covers resolveUpdateURL priority: flag > env var > compile-time injection.
func TestResolveUpdateURLPriority(t *testing.T) {
	oldFlag, oldDefault := updateSourceURL, DefaultUpdateURL
	defer func() { updateSourceURL, DefaultUpdateURL = oldFlag, oldDefault }()

	updateSourceURL = "http://flag"
	t.Setenv("SERIALCAT_UPDATE_URL", "http://env")
	DefaultUpdateURL = "http://ldflags"
	if got := resolveUpdateURL(); got != "http://flag" {
		t.Fatalf("flag should win: got %q", got)
	}

	updateSourceURL = ""
	if got := resolveUpdateURL(); got != "http://env" {
		t.Fatalf("env var should be next: got %q", got)
	}

	t.Setenv("SERIALCAT_UPDATE_URL", "")
	if got := resolveUpdateURL(); got != "http://ldflags" {
		t.Fatalf("ldflags injection not applied: got %q", got)
	}

	DefaultUpdateURL = "unset"
	if got := resolveUpdateURL(); got != "" {
		t.Fatalf("empty string expected when unconfigured: got %q", got)
	}
}

// Covers the newUpdaterSource factory type detection: a value containing "://" is a
// MinIO HTTP source, any other value is a GitHub repository (owner/repo).
func TestNewUpdaterSourceTypeDetection(t *testing.T) {
	minioSrc, err := newUpdaterSource("http://127.0.0.1:9000/bucket", false)
	if err != nil {
		t.Fatalf("minio factory: unexpected error: %v", err)
	}
	if _, ok := minioSrc.(*updater.MinioSource); !ok {
		t.Fatalf("minio factory returned %T, want *updater.MinioSource", minioSrc)
	}

	ghSrc, err := newUpdaterSource("JFRabbit/serialcat", false)
	if err != nil {
		t.Fatalf("github factory: unexpected error: %v", err)
	}
	if _, ok := ghSrc.(*updater.GitHubSource); !ok {
		t.Fatalf("github factory returned %T, want *updater.GitHubSource", ghSrc)
	}
}

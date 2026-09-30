package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"logdiag/internal/updater"
)

// fakeSource 实现 updater.Source, 通过函数字段控制各方法行为。
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

// updateTestEnv 注入 mock 更新源与可执行路径, 并重置包级状态; 返回还原函数。
// 被测 runUpdate 依赖多个包级变量 (Version/updateSourceURL/insecure/newUpdaterSource/getExecutablePath),
// 每次用例前后都要保存还原, 避免测试间状态泄漏。
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

// newVersionInfo 构造一个仅包含当前平台的 VersionInfo。
func newVersionInfo(latest, path, sha string) *updater.VersionInfo {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	return &updater.VersionInfo{
		Latest:    latest,
		Platforms: map[string]string{platform: path},
		SHA256:    map[string]string{platform: sha},
	}
}

// 覆盖分支: 版本信息未注入 (本地构建) 直接报错, 不访问更新源。
func TestRunUpdateVersionNotInjected(t *testing.T) {
	restore := updateTestEnv(t, &fakeSource{}, "", nil)
	defer restore()
	Version = "unset"

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("期望返回版本未注入错误, 实际为 nil")
	}
}

// 覆盖分支: 未配置更新源地址 (无 flag / 环境变量 / 编译注入) 时报错。
func TestRunUpdateNoSourceURL(t *testing.T) {
	restore := updateTestEnv(t, &fakeSource{}, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = ""
	t.Setenv("LOGDIAG_UPDATE_URL", "")
	oldDefault := DefaultUpdateURL
	DefaultUpdateURL = "unset"
	defer func() { DefaultUpdateURL = oldDefault }()

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("期望返回未配置更新源错误, 实际为 nil")
	}
}

// 覆盖分支: CheckLatest 失败, 错误原样返回。
func TestRunUpdateCheckLatestFailed(t *testing.T) {
	wantErr := errors.New("拉取版本信息失败")
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

// 覆盖分支: 版本比较失败 (更新源返回非法版本号) 时报错。
func TestRunUpdateCompareVersionFailed(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("not-a-version", "bin/logdiag", ""), nil
		},
	}
	restore := updateTestEnv(t, src, "", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	if err := runUpdate(false, false, false); err == nil {
		t.Fatal("期望返回版本比较错误, 实际为 nil")
	}
}

// 覆盖分支: 当前已是最新版本 (cmp <= 0 且非 force), 正常返回。
func TestRunUpdateAlreadyLatest(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("1.0.0", "bin/logdiag", ""), nil
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

// 覆盖分支: force 模式下即使已是最新也继续走下载流程;
// 下载成功后完成替换并返回 nil (临时文件应被清理)。
func TestRunUpdateForceDownloadSuccess(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "logdiag")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	content := []byte("new-binary")
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("1.0.0", "bin/logdiag", ""), nil
		},
		download: func(ctx context.Context, url, dst string) error {
			if dst != exe+".new" {
				t.Errorf("下载目标 = %q, want %q", dst, exe+".new")
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
		t.Fatalf("替换后内容 = %q, want %q", got, content)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("临时文件未被清理, stat err = %v", err)
	}
}

// 覆盖分支: 发现新版本且 checkOnly=true, 打印提示后不下载直接返回 nil。
func TestRunUpdateCheckOnly(t *testing.T) {
	downloadCalled := false
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/logdiag", ""), nil
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
		t.Fatal("check-only 模式下不应触发下载")
	}
}

// 覆盖分支: 更新源未提供当前平台的下载地址时报错。
func TestRunUpdatePlatformMissing(t *testing.T) {
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			// Platforms 故意为空, 不含当前平台。
			return &updater.VersionInfo{Latest: "2.0.0", Platforms: map[string]string{}}, nil
		},
	}
	restore := updateTestEnv(t, src, "/tmp/logdiag", nil)
	defer restore()
	Version = "1.0.0"
	updateSourceURL = "http://127.0.0.1:9000/bucket"

	err := runUpdate(false, false, false)
	if err == nil {
		t.Fatal("期望返回平台缺失错误, 实际为 nil")
	}
	if !strings.Contains(err.Error(), runtime.GOOS+"_"+runtime.GOARCH) {
		t.Fatalf("错误信息应包含当前平台, 实际为: %v", err)
	}
}

// 覆盖分支: 获取可执行文件路径失败, 错误原样返回。
func TestRunUpdateExePathFailed(t *testing.T) {
	wantErr := errors.New("找不到可执行文件")
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/logdiag", ""), nil
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

// 覆盖分支: 下载失败, 错误原样返回, 且不替换可执行文件。
func TestRunUpdateDownloadFailed(t *testing.T) {
	wantErr := errors.New("下载超时")
	exe := filepath.Join(t.TempDir(), "logdiag")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/logdiag", ""), nil
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
		t.Fatalf("可执行文件内容被改动: %q", got)
	}
}

// 覆盖分支: 下载成功但 SHA256 校验失败时报错, 且不替换可执行文件。
func TestRunUpdateSHA256Mismatch(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "logdiag")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{
		checkLatest: func(ctx context.Context) (*updater.VersionInfo, error) {
			return newVersionInfo("2.0.0", "bin/logdiag", "deadbeef"), nil
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
		t.Fatal("期望返回校验失败错误, 实际为 nil")
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("可执行文件内容被改动: %q", got)
	}
}

// 覆盖 withTLSCertFallback 分支: 更新源工厂创建失败, 错误原样返回且不执行回调。
func TestRunUpdateSourceFactoryFailed(t *testing.T) {
	wantErr := errors.New("不支持的更新源类型")
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
		t.Fatal("工厂创建失败时不应执行回调")
	}
}

// 覆盖 resolveUpdateURL 优先级: flag > 环境变量 > 编译注入。
func TestResolveUpdateURLPriority(t *testing.T) {
	oldFlag, oldDefault := updateSourceURL, DefaultUpdateURL
	defer func() { updateSourceURL, DefaultUpdateURL = oldFlag, oldDefault }()

	updateSourceURL = "http://flag"
	t.Setenv("LOGDIAG_UPDATE_URL", "http://env")
	DefaultUpdateURL = "http://ldflags"
	if got := resolveUpdateURL(); got != "http://flag" {
		t.Fatalf("flag 优先: got %q", got)
	}

	updateSourceURL = ""
	if got := resolveUpdateURL(); got != "http://env" {
		t.Fatalf("环境变量次之: got %q", got)
	}

	t.Setenv("LOGDIAG_UPDATE_URL", "")
	if got := resolveUpdateURL(); got != "http://ldflags" {
		t.Fatalf("编译注入兜底: got %q", got)
	}

	DefaultUpdateURL = "unset"
	if got := resolveUpdateURL(); got != "" {
		t.Fatalf("未配置应返回空串: got %q", got)
	}
}

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"logdiag/internal/constant"
	"logdiag/internal/logger"
	"logdiag/internal/updater"

	"github.com/spf13/cobra"
)

// 更新源地址优先级: --source-url > 环境变量 LOGDIAG_UPDATE_URL > 编译时注入 DefaultUpdateURL
var (
	updateSourceURL string
	// DefaultUpdateURL 由 ldflags 注入更新源地址前缀; "unset" 表示未注入。
	DefaultUpdateURL = "unset"
)

// updaterSourceFactory 创建更新源, 便于测试注入 mock。
type updaterSourceFactory func(baseURL string, insecure bool) (updater.Source, error)

// newUpdaterSource 默认更新源工厂 (MinIO HTTP)。
var newUpdaterSource updaterSourceFactory = func(baseURL string, insecure bool) (updater.Source, error) {
	return updater.NewSource("minio", baseURL, constant.AppName, insecure)
}

// getExecutablePath 返回当前可执行文件路径, 便于测试注入。
var getExecutablePath = selfPath

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "检查并更新到最新版本",
	Long:  "从更新源检测最新版本, 下载并替换当前可执行文件。",
	RunE: func(cmd *cobra.Command, args []string) error {
		checkOnly, err := cmd.Flags().GetBool("check")
		if err != nil {
			return err
		}
		force, err := cmd.Flags().GetBool("force")
		if err != nil {
			return err
		}
		return runUpdate(checkOnly, force, insecure)
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().StringVar(&updateSourceURL, "source-url", "", "更新源地址前缀 (http(s)://ip:port/bucket)")
	updateCmd.Flags().Bool("check", false, "仅检查是否有新版本, 不执行更新")
	updateCmd.Flags().Bool("force", false, "强制更新, 即使当前已是最新版本")
}

func runUpdate(checkOnly, force, insecure bool) error {
	if Version == "" || Version == "unset" {
		return fmt.Errorf("当前版本信息未注入（本地构建）, 不支持自动更新")
	}

	baseURL := resolveUpdateURL()
	if baseURL == "" {
		return fmt.Errorf("未配置更新源地址, 请使用 --source-url 或环境变量 LOGDIAG_UPDATE_URL")
	}
	if !insecure {
		insecure = os.Getenv("LOGDIAG_UPDATE_INSECURE") == "1" || os.Getenv("LOGDIAG_UPDATE_INSECURE") == "true"
	}

	ctx := context.Background()
	logger.Info("正在检查最新版本: %s", baseURL)
	var info *updater.VersionInfo
	err := withTLSCertFallback(ctx, insecure, baseURL, func(src updater.Source, c context.Context) error {
		var e error
		info, e = src.CheckLatest(c)
		return e
	})
	if err != nil {
		return err
	}

	logger.Info("当前版本: %s, 最新版本: %s", Version, info.Latest)
	cmp, err := updater.CompareVersions(info.Latest, Version)
	if err != nil {
		return err
	}
	if cmp <= 0 && !force {
		logger.Info("当前已是最新版本")
		fmt.Println("当前已是最新版本")
		return nil
	}
	if checkOnly {
		logger.Info("发现新版本: %s (使用 logdiag update 进行更新)", info.Latest)
		fmt.Printf("发现新版本: %s\n", info.Latest)
		return nil
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH
	path, ok := info.Platforms[platform]
	if !ok {
		return fmt.Errorf("更新源未提供当前平台 (%s) 的下载地址", platform)
	}
	downloadURL := updater.ResolveURL(baseURL, path)

	exePath, err := getExecutablePath()
	if err != nil {
		return err
	}
	// 临时文件与可执行文件同目录, 保证同一分区, 避免跨分区重命名失败。
	tmpFile := exePath + ".new"
	defer func() { _ = os.Remove(tmpFile) }()

	logger.Info("正在下载新版本: %s", downloadURL)
	if err := withTLSCertFallback(ctx, insecure, baseURL, func(src updater.Source, c context.Context) error {
		return src.Download(c, downloadURL, tmpFile)
	}); err != nil {
		return err
	}

	if expected, ok := info.SHA256[platform]; ok && expected != "" {
		logger.Info("正在校验文件完整性")
		if err := updater.VerifySHA256(tmpFile, expected); err != nil {
			return err
		}
	}

	logger.Info("正在替换可执行文件: %s", exePath)
	if err := updater.ReplaceExecutable(exePath, tmpFile); err != nil {
		return fmt.Errorf("替换可执行文件失败: %w", err)
	}

	if runtime.GOOS == "windows" {
		logger.Info("更新已调度, 进程退出后将自动完成替换")
		fmt.Println("更新已调度, 进程退出后将自动完成替换")
	} else {
		logger.Info("更新完成, 当前版本: %s, 请重新运行 logdiag", info.Latest)
		fmt.Printf("更新完成, 当前版本: %s\n", info.Latest)
	}
	return nil
}

// withTLSCertFallback 执行更新源操作, 证书校验失败时自动降级为跳过校验重试一次。
func withTLSCertFallback(ctx context.Context, insecure bool, baseURL string, fn func(updater.Source, context.Context) error) error {
	source, err := newUpdaterSource(baseURL, insecure)
	if err != nil {
		return err
	}
	fnErr := fn(source, ctx)
	if fnErr == nil || insecure || !updater.IsTLSCertError(fnErr) {
		return fnErr
	}
	logger.Warn("更新源证书校验失败, 已自动跳过证书校验重试 (自签名证书场景); 如需屏蔽此提示, 请使用 --insecure 参数")
	source, err = newUpdaterSource(baseURL, true)
	if err != nil {
		return err
	}
	return fn(source, ctx)
}

// resolveUpdateURL 按优先级解析更新源地址。
func resolveUpdateURL() string {
	if updateSourceURL != "" {
		return updateSourceURL
	}
	if env := os.Getenv("LOGDIAG_UPDATE_URL"); env != "" {
		return env
	}
	if DefaultUpdateURL != "unset" {
		return DefaultUpdateURL
	}
	return ""
}

// selfPath 返回当前可执行文件的真实路径（解析符号链接）。
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取可执行文件路径失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("解析可执行文件路径失败: %w", err)
	}
	return resolved, nil
}
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/JFRabbit/serialcat/internal/constant"
	"github.com/JFRabbit/serialcat/internal/logger"
	"github.com/JFRabbit/serialcat/internal/updater"

	"github.com/spf13/cobra"
)

// Update source URL priority: --source-url > env SERIALCAT_UPDATE_URL > compile-time injected DefaultUpdateURL
var (
	updateSourceURL string
	// DefaultUpdateURL is the update source URL prefix injected via ldflags; "unset" means not injected.
	DefaultUpdateURL = "unset"
)

// updaterSourceFactory creates an update source, injectable by tests with a mock.
type updaterSourceFactory func(baseURL string, insecure bool) (updater.Source, error)

// newUpdaterSource is the default update source factory. A value containing "://"
// is treated as a MinIO HTTP source (http(s)://ip:port/bucket); any other value is
// treated as a GitHub repository in the form owner/repo (e.g. "JFRabbit/serialcat").
var newUpdaterSource updaterSourceFactory = func(baseURL string, insecure bool) (updater.Source, error) {
	if strings.Contains(baseURL, "://") {
		return updater.NewSource("minio", baseURL, constant.AppName, insecure)
	}
	return updater.NewSource("github", baseURL, "", insecure)
}

// getExecutablePath returns the current executable path, injectable by tests.
var getExecutablePath = selfPath

// insecure skips TLS certificate verification (for self-signed update sources). Local flag used only by the update command.
var insecure bool

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check and update to the latest version",
	Long:  "Check the latest version from the update source, download it, and replace the current executable.",
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
	updateCmd.Flags().StringVar(&updateSourceURL, "source-url", "", "GitHub repository (owner/repo) or update source URL")
	updateCmd.Flags().Bool("check", false, "only check for a new version without updating")
	updateCmd.Flags().Bool("force", false, "force update even if already the latest version")
	updateCmd.Flags().BoolVar(&insecure, "insecure", false, "skip TLS certificate verification (self-signed update source)")
}

func runUpdate(checkOnly, force, insecure bool) error {
	if Version == "" || Version == "unset" {
		return fmt.Errorf("version info not injected (local build), automatic update is not supported")
	}

	baseURL := resolveUpdateURL()
	if baseURL == "" {
		return fmt.Errorf("update source URL not configured, use --source-url or the SERIALCAT_UPDATE_URL environment variable")
	}
	if !insecure {
		insecure = os.Getenv("SERIALCAT_UPDATE_INSECURE") == "1" || os.Getenv("SERIALCAT_UPDATE_INSECURE") == "true"
	}

	ctx := context.Background()
	logger.Info("checking latest version: %s", baseURL)
	var info *updater.VersionInfo
	err := withTLSCertFallback(ctx, insecure, baseURL, func(src updater.Source, c context.Context) error {
		var e error
		info, e = src.CheckLatest(c)
		return e
	})
	if err != nil {
		return err
	}

	logger.Info("current version: %s, latest version: %s", Version, info.Latest)
	cmp, err := updater.CompareVersions(info.Latest, Version)
	if err != nil {
		return err
	}
	if cmp <= 0 && !force {
		logger.Info("already the latest version")
		fmt.Println("already the latest version")
		return nil
	}
	if checkOnly {
		logger.Info("new version found: %s (run serialcat update to install)", info.Latest)
		fmt.Printf("new version found: %s\n", info.Latest)
		return nil
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH
	path, ok := info.Platforms[platform]
	if !ok {
		return fmt.Errorf("update source provides no download for the current platform (%s)", platform)
	}
	downloadURL := updater.ResolveURL(baseURL, path)

	exePath, err := getExecutablePath()
	if err != nil {
		return err
	}
	// Keep the temp file in the same directory as the executable so they share a
	// volume, avoiding cross-volume rename failures.
	tmpFile := exePath + ".new"
	defer func() { _ = os.Remove(tmpFile) }()

	logger.Info("downloading new version: %s", downloadURL)
	if err := withTLSCertFallback(ctx, insecure, baseURL, func(src updater.Source, c context.Context) error {
		return src.Download(c, downloadURL, tmpFile)
	}); err != nil {
		return err
	}

	if expected, ok := info.SHA256[platform]; ok && expected != "" {
		logger.Info("verifying file integrity")
		if err := updater.VerifySHA256(tmpFile, expected); err != nil {
			return err
		}
	}

	logger.Info("replacing executable: %s", exePath)
	if err := updater.ReplaceExecutable(exePath, tmpFile); err != nil {
		return fmt.Errorf("failed to replace executable: %w", err)
	}

	if runtime.GOOS == "windows" {
		logger.Info("update scheduled; replacement will finish after the process exits")
		fmt.Println("update scheduled; replacement will finish after the process exits")
	} else {
		logger.Info("update complete, current version: %s, please rerun serialcat", info.Latest)
		fmt.Printf("update complete, current version: %s\n", info.Latest)
	}
	return nil
}

// withTLSCertFallback runs the update source operation, retrying once with
// verification skipped when certificate verification fails.
func withTLSCertFallback(ctx context.Context, insecure bool, baseURL string, fn func(updater.Source, context.Context) error) error {
	source, err := newUpdaterSource(baseURL, insecure)
	if err != nil {
		return err
	}
	fnErr := fn(source, ctx)
	if fnErr == nil || insecure || !updater.IsTLSCertError(fnErr) {
		return fnErr
	}
	logger.Warn("update source certificate verification failed; retried with verification skipped (self-signed certificate scenario); use --insecure to suppress this message")
	source, err = newUpdaterSource(baseURL, true)
	if err != nil {
		return err
	}
	return fn(source, ctx)
}

// resolveUpdateURL resolves the update source URL by priority.
func resolveUpdateURL() string {
	if updateSourceURL != "" {
		return updateSourceURL
	}
	if env := os.Getenv("SERIALCAT_UPDATE_URL"); env != "" {
		return env
	}
	if DefaultUpdateURL != "unset" {
		return DefaultUpdateURL
	}
	return ""
}

// selfPath returns the real path of the current executable (resolving symlinks).
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("failed to resolve executable path: %w", err)
	}
	return resolved, nil
}

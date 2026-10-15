//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// ReplaceExecutable: on Windows the current exe is locked and cannot be overwritten directly.
// Generates a batch script that waits for the current process to exit, replaces the file, and
// deletes itself; starts the script with a hidden window and returns immediately, so the caller
// should exit the process as soon as possible.
func ReplaceExecutable(target, newFile string) error {
	dir := filepath.Dir(target)
	batPath := filepath.Join(dir, filepath.Base(target)+".update.bat")

	script := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak > nul
move /y "%s" "%s"
del "%%~f0"
`, newFile, target)

	if err := os.WriteFile(batPath, []byte(script), 0o644); err != nil {
		return fmt.Errorf("failed to create update script: %w", err)
	}
	cmd := exec.Command("cmd", "/c", batPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start update script: %w", err)
	}
	return nil
}

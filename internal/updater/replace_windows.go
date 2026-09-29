//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// ReplaceExecutable Windows 下当前 exe 被锁定，无法直接覆盖。
// 生成批处理脚本：等待当前进程退出后替换文件并自删除，
// 以隐藏窗口启动脚本后立即返回，调用方应尽快退出进程。
func ReplaceExecutable(target, newFile string) error {
	dir := filepath.Dir(target)
	batPath := filepath.Join(dir, filepath.Base(target)+".update.bat")

	script := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak > nul
move /y "%s" "%s"
del "%%~f0"
`, newFile, target)

	if err := os.WriteFile(batPath, []byte(script), 0o644); err != nil {
		return fmt.Errorf("创建更新脚本失败: %w", err)
	}
	cmd := exec.Command("cmd", "/c", batPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新脚本失败: %w", err)
	}
	return nil
}
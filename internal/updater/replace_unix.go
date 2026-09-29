//go:build !windows

package updater

import "os"

// ReplaceExecutable 用新文件原子覆盖当前可执行文件。
// 进程已加载到内存，Unix 下可直接覆盖正在运行的二进制。
func ReplaceExecutable(target, newFile string) error {
	if err := os.Chmod(newFile, 0o755); err != nil {
		return err
	}
	return os.Rename(newFile, target)
}
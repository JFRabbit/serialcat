//go:build !windows

package updater

import "os"

// ReplaceExecutable atomically overwrites the current executable with the new file.
// The process is already loaded into memory, so on Unix the running binary can be overwritten directly.
func ReplaceExecutable(target, newFile string) error {
	if err := os.Chmod(newFile, 0o755); err != nil {
		return err
	}
	return os.Rename(newFile, target)
}

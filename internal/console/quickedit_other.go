//go:build !windows

package console

// DisableQuickEdit 非 Windows 平台无控制台快速编辑行为,空实现。
func DisableQuickEdit() {}

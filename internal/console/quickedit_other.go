//go:build !windows

package console

// DisableQuickEdit is a no-op since non-Windows platforms have no console QuickEdit behavior.
func DisableQuickEdit() {}

//go:build windows

package console

import "golang.org/x/sys/windows"

// STD_INPUT_HANDLE is the standard input handle constant (-10).
const stdinHandle = uint32(0xFFFFFFF6) // -10 as uint32

// DisableQuickEdit turns off QuickEdit mode for the current console.
// With QuickEdit on, once the user selects text inside the CMD window, conhost suspends that
// window's standard output, backpressuring high-frequency scrolling programs (e.g. the run
// command's real-time console printing).
// This only affects a real console; when stdout/stdin are redirected, GetConsoleMode fails and
// is safely ignored.
func DisableQuickEdit() {
	h, err := windows.GetStdHandle(stdinHandle)
	if err != nil {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return // skip for non-interactive consoles (redirected/pipe); does not affect normal functionality
	}
	newMode := mode &^ windows.ENABLE_QUICK_EDIT_MODE
	if newMode != mode {
		_ = windows.SetConsoleMode(h, newMode)
	}
}

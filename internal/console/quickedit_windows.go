//go:build windows

package console

import "golang.org/x/sys/windows"

// STD_INPUT_HANDLE 是标准输入句柄常量（-10）。
const stdinHandle = uint32(0xFFFFFFF6) // -10 转 uint32

// DisableQuickEdit 关闭当前控制台的快速编辑模式。
// QuickEdit 开启时,一旦用户在 CMD 窗口内选中文本,conhost 会挂起该窗口的标准输出,
// 使高频滚屏的程序（如 run 的 console 实时打印）被背压堵住。
// 此函数仅对真实控制台生效;stdout/stdin 被重定向时 GetConsoleMode 失败,忽略即可。
func DisableQuickEdit() {
	h, err := windows.GetStdHandle(stdinHandle)
	if err != nil {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return // 非交互控制台（重定向/管道）时跳过,不影响正常功能
	}
	newMode := mode &^ windows.ENABLE_QUICK_EDIT_MODE
	if newMode != mode {
		_ = windows.SetConsoleMode(h, newMode)
	}
}

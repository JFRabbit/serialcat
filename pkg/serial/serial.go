// Package serial 提供串口驱动的平台抽象，供本地串口直连(source:serial)、
// send 本地写、以及 Windows agent 复用同一套驱动。
//
// 跨平台约定：Windows 由 go.bug.st/serial 提供真实实现；其余平台为占位桩，
// Open/ListPorts 返回明确的"仅支持 Windows"错误。
package serial

import (
	"errors"
	"io"
)

// errUnsupported 用于非 Windows 平台占位：串口驱动不提供物理能力。
var ErrUnsupported = errors.New("串口能力仅支持 Windows，请在 Windows 上运行")

// Driver 是串口驱动的平台抽象。Windows 由 go.bug.st/serial 实现，其余平台为占位桩。
type Driver interface {
	// ListPorts 枚举可用串口名（如 COM5）。
	ListPorts() ([]string, error)
	// Open 打开指定串口（8 数据位/无校验/1 停止位）。
	Open(name string, baud int) (io.ReadWriteCloser, error)
}

// NewDriver 构造平台串口驱动。非 Windows 平台返回的驱动仅报 unsupported。
func NewDriver() (Driver, error) { return newDriver() }

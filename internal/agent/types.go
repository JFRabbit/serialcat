// Package agent 实现 Windows 上运行的串口代理：对接串口并经 HTTP 暴露枚举/实时流/下行能力。
// 该子命令应在 Windows 上运行（串口物理存在），logdiag 主进程作为 DAG 引擎跨平台纯 HTTP 拉取。
package agent

import (
	"errors"
	"io"
	"time"
)

// Frame 是经 HTTP /api/stream 传递的单条串口记录（NDJSON）。
type Frame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// errUnsupported 用于非 Windows 平台占位：串口代理不提供物理能力。
var errUnsupported = errors.New("串口代理仅支持 Windows，请在 Windows 上运行 logdiag agent")

// Driver 是串口驱动的平台抽象。Windows 由 go.bug.st/serial 实现，其余平台为占位桩。
type Driver interface {
	// ListPorts 枚举可用串口名（如 COM5）。
	ListPorts() ([]string, error)
	// Open 打开指定串口（8 数据位/无校验/1 停止位）。
	Open(name string, baud int) (io.ReadWriteCloser, error)
}

// NewDriver 构造平台串口驱动。非 Windows 平台返回的驱动仅报 unsupported。
func NewDriver() (Driver, error) { return newDriver() }

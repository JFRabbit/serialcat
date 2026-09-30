// Package agent 实现 Windows 上运行的串口代理：对接串口并经 HTTP 暴露枚举/实时流/下行能力。
// 该子命令应在 Windows 上运行（串口物理存在），logdiag 主进程作为 DAG 引擎跨平台纯 HTTP 拉取。
package agent

import "time"

// Frame 是经 HTTP /api/stream 传递的单条串口记录（NDJSON）。
type Frame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

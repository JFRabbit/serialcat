package nodes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"logdiag/pkg/dag"
	"logdiag/pkg/serial"
)

// LocalSerial 是 source 节点：从本机串口读行 → 切批发射，接入上游同一套批量 DAG。
// 串口物理能力由 pkg/serial 提供（仅 Windows 可用）；其它平台打开串口时返回明确的"仅支持 Windows"错误。
type LocalSerial struct {
	id   string
	port string
	baud int
	out  chan []dag.Item
	// onOpen 可选：串口打开成功后回调，供上层（run）注入下行写能力，复用同一句柄避免重复 Open 冲突。
	onOpen func(ctx context.Context, p io.ReadWriteCloser)
}

var _ dag.SourceNode = (*LocalSerial)(nil)

// SetOnOpen 注册串口打开成功后的回调（通常用于本地写 HTTP 通道）。
func (n *LocalSerial) SetOnOpen(f func(ctx context.Context, p io.ReadWriteCloser)) { n.onOpen = f }

// NewLocalSerial 构造本地串口来源。baud<=0 时默认 115200；buf<=0 时默认 128 批缓冲。
func NewLocalSerial(id, port string, baud, buf int) *LocalSerial {
	if buf <= 0 {
		buf = 128
	}
	if baud <= 0 {
		baud = 115200
	}
	return &LocalSerial{id: id, port: port, baud: baud, out: make(chan []dag.Item, buf)}
}

// Name 实现 dag.Node。
func (n *LocalSerial) Name() string { return n.id }

// Out 实现 dag.SourceNode。
func (n *LocalSerial) Out() <-chan []dag.Item { return n.out }

// Run 打开本机串口并批量发射。串口为连续源：非 EOF 的读取错误按异常返回（不做正常 EOS）。
// in 对 source 为 nil。
func (n *LocalSerial) Run(ctx context.Context, _ <-chan []dag.Item) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}
	p, err := drv.Open(n.port, n.baud)
	if err != nil {
		return fmt.Errorf("打开串口 %s@%d: %w", n.port, n.baud, err)
	}
	defer p.Close()
	defer close(n.out) // 终止即关闭 Out（EOS 或异常都向引擎宣告结束）

	// 串口成功打开后告知上层（如 run 的本地写通道），复用同一句柄下发命令。
	if n.onOpen != nil {
		n.onOpen(ctx, p)
	}

	br := bufio.NewReader(p)

	var batch []dag.Item
	bytes := 0

	// flush 发射当前半批（若非空）。
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		select {
		case n.out <- batch:
			batch = nil
			bytes = 0
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 读取放到独立协程：避免读阻塞时无法执行半批发射/侦听取消。
	lines := make(chan string, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				lines <- strings.TrimRight(line, "\r\n")
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	tick := time.NewTicker(flushIdle)
	defer tick.Stop()
	for {
		select {
		case line := <-lines:
			batch = append(batch, dag.Item{Ts: time.Now(), Text: line})
			bytes += len(line)
			if len(batch) >= dag.BatchMaxItems || bytes >= dag.BatchMaxBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-tick.C:
			// 低速率/安静流：周期性地吐掉半批，降低端到端延迟。
			if err := flush(); err != nil {
				return err
			}
		case err := <-readErr:
			if ferr := flush(); ferr != nil {
				return ferr
			}
			if err == io.EOF {
				return nil // 串口被关闭
			}
			return fmt.Errorf("串口读取中断: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

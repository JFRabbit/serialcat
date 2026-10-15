package nodes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/JFRabbit/serialcat/internal/logger"
	"github.com/JFRabbit/serialcat/pkg/dag"
)

// Process 是 process 节点：把上游 batch（[]Item）交给外部子进程处理，结果译回 []Item 后传下游。
// 它扮演"子进程通信壳"：不做任何业务判断，具体逻辑由外部脚本（command+args）承担。
//
// 通信协议为批量 NDJSON：
//   - 发送（Go→脚本，stdin）：每批一个 JSON 数组（元素为 {ts,text,src,meta}），一行一个数组。
//   - 接收（脚本→Go，stdout）：脚本逐行输出 JSON 数组，每个数组译回一批 []Item。
//   - script 可增删改 Item；返回空数组即丢弃该批。
//   - stderr 原样透传到日志（便于调试），不影响数据流。
type Process struct {
	id      string
	command string
	args    []string
	out     chan []dag.Item
}

var _ dag.ProcessNode = (*Process)(nil)

// NewProcess 构造子进程加工节点。buf<=0 时用默认缓冲（128 批）。
func NewProcess(id, command string, args []string, buf int) *Process {
	if buf <= 0 {
		buf = 128
	}
	return &Process{
		id:      id,
		command: command,
		args:    args,
		out:     make(chan []dag.Item, buf),
	}
}

// Name 实现 dag.Node。
func (n *Process) Name() string { return n.id }

// Out 实现 dag.ProcessNode。
func (n *Process) Out() (chan []dag.Item, bool) { return n.out, true }

// Run 启动子进程，循环"写 stdin 批 / 读 stdout 批"，直到 in 关闭（EOS）。
//
// 并发模型：写与读各占一个 goroutine，由 out 统一汇合，避免批读互锁。
//   - in 关闭 → close(stdin) → 写协程结束；读协程读到 stdout EOF 后结束并 close(out)。
//   - 任何错误（启动失败 / 中途退出 / 解码失败 / ctx 取消）→ 报错并关闭 out。
func (n *Process) Run(ctx context.Context, in <-chan []dag.Item) error {
	cmd := exec.CommandContext(ctx, n.command, n.args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("node %s: 获取 stdin 失败: %w", n.id, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("node %s: 获取 stdout 失败: %w", n.id, err)
	}
	// stderr 透传：起协程循环读，防止脚本写满 stderr 缓冲而阻塞自身。
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("node %s: 获取 stderr 失败: %w", n.id, err)
	}
	var stderrWg sync.WaitGroup
	stderrWg.Add(1)
	go func() {
		defer stderrWg.Done()
		_ = copyToLog(stderr, n.id)
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("node %s: 启动子进程失败 %q: %w", n.id, n.command, err)
	}

	// 结果是否已关闭 out 的互斥，编译期防重复 close。
	var closeOnce sync.Once
	closeOut := func() { closeOnce.Do(func() { close(n.out) }) }

	// ---- 写协程：in 批 → stdin ----
	writeErr := make(chan error, 1)
	go func() {
		defer func() {
			// in 关闭（EOS）后，关闭子进程 stdin，提醒脚本"输入结束"。
			_ = stdin.Close()
		}()
		enc := json.NewEncoder(stdin)
		for batch := range in {
			if err := enc.Encode(batchToFrameSlice(batch)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	// ---- 读协程：stdout 批 → out ----
	readErr := make(chan error, 1)
	go func() {
		defer closeOut()
		dec := json.NewDecoder(bufio.NewReader(stdout))
		for {
			var frames []frame
			if err := dec.Decode(&frames); err != nil {
				if err == io.EOF {
					readErr <- nil
					return
				}
				readErr <- fmt.Errorf("解析脚本输出失败: %w", err)
				return
			}
			if len(frames) == 0 {
				continue // 空批=丢弃，不发下游
			}
			batch := make([]dag.Item, 0, len(frames))
			for _, f := range frames {
				batch = append(batch, f.item())
			}
			select {
			case n.out <- batch:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
	}()

	waitC := make(chan error, 1)
	go func() { waitC <- cmd.Wait() }()

	// 汇合：等写结束（即 in 已被消费完），再读读到 EOF 结束。
	// 顺序：等 writeErr → 关 stdin（写协程 defer 已做）→ 等 stdout EOF → 等 wait。
	if werr := <-writeErr; werr != nil {
		_ = cmd.Process.Kill()
		closeOut()
		return fmt.Errorf("node %s: 写入子进程失败: %w", n.id, werr)
	}

	// 写正常结束后，等待读结束；随后回收进程。
	var rerr error
	select {
	case rerr = <-readErr:
	case <-ctx.Done():
		rerr = ctx.Err()
	}
	stderrWg.Wait()
	_ = <-waitC // 忽略 0 退出码（EOS 正常结束）；异常由 cmd.Wait 的错误在下方报告
	if rerr != nil {
		return fmt.Errorf("node %s: %w", n.id, rerr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// batchToFrameSlice 把批量 Item 转为可序列化的 frame 切片（NDJSON 一行对应这批）。
func batchToFrameSlice(batch []dag.Item) []frame {
	frames := make([]frame, 0, len(batch))
	for _, it := range batch {
		frames = append(frames, frame{Ts: it.Ts, Text: it.Text})
	}
	return frames
}

// copyToLog 把 r 逐行复制到日志（透传 stderr），供调试。
func copyToLog(r io.Reader, node string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		logger.Warn("node %s script stderr: %s", node, sc.Text())
	}
	return sc.Err()
}

// frame 是子进程往来的记录结构，field 对齐 dag.Item 中需要外传的字段。
// 复用于序列化/反序列化。
type frame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

func (f frame) item() dag.Item {
	return dag.Item{Ts: f.Ts, Text: f.Text}
}
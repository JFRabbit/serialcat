package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"logdiag/pkg/dag"
)

// remoteFrame 对应远端 agent 流式下发的单条串口记录（NDJSON）。fields 与内部 agent 对齐。
type remoteFrame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// SerialRemote 是 source 节点：从远端"logdiag agent"服务的 /api/stream 拉取串口行，
// 包装成 dag.Item 后按批发射，接入上游同一套批量 DAG。
// 该节点纯 HTTP、跨平台，串口物理能力由 Windows 上的 agent 提供。
type SerialRemote struct {
	id     string
	base   string
	out    chan []dag.Item
	client *http.Client
}

var _ dag.SourceNode = (*SerialRemote)(nil)

// NewSerialRemote 构造远端串口来源节点。baseURL 形如 http://192.168.1.10:9100 或带尾斜杠;
// 缺协议(如 192.168.1.10:9100)时自动补 http://。
func NewSerialRemote(id, baseURL string, buf int) *SerialRemote {
	if buf <= 0 {
		buf = 128
	}
	base := strings.TrimSpace(baseURL)
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &SerialRemote{
		id:     id,
		base:   base,
		out:    make(chan []dag.Item, buf),
		client: &http.Client{Timeout: 0}, // 流式长连接，不设总超时
	}
}

// Name 实现 dag.Node。
func (n *SerialRemote) Name() string { return n.id }

// Out 实现 dag.SourceNode。
func (n *SerialRemote) Out() <-chan []dag.Item { return n.out }

// Run 连接远端流并批量发射。连接中断按错误返回（触发 Run 失败），
// 避免把实时的"断连"误当成正常 EOS。in 对 source 为 nil。
func (n *SerialRemote) Run(ctx context.Context, _ <-chan []dag.Item) error {
	// 首次连接带重试，容忍 agent 启动晚于本节点（如自动部署重启竞态）。
	resp, err := n.dialStream(ctx)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	defer close(n.out) // 无论成功/出错/取消，终止即关闭 Out（EOS 或异常都向引擎宣告结束）

	dec := json.NewDecoder(resp.Body)

	// 解码放到独立协程：避免解码阻塞时无法执行半批发射/侦听取消。
	frames := make(chan remoteFrame, 64)
	decErr := make(chan error, 1)
	go func() {
		var f remoteFrame
		for {
			if err := dec.Decode(&f); err != nil {
				decErr <- err
				return
			}
			frames <- f
		}
	}()

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

	tick := time.NewTicker(flushIdle)
	defer tick.Stop()
	for {
		select {
		case f := <-frames:
			// 时间戳用 agent 下发值。
			batch = append(batch, dag.Item{Ts: f.Ts, Text: f.Text})
			bytes += len(f.Text)
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
		case err := <-decErr:
			// 尽力发射剩余半批，再报告中断（连续源"断连"=异常，不做 EOS）。
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return fmt.Errorf("远端串口流中断: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// flushIdle 是低速率串口的半批周期发射间隔。
const flushIdle = 100 * time.Millisecond

// dialStream 建立到 /api/stream 的长连接，带重试。返回响应体。
func (n *SerialRemote) dialStream(ctx context.Context) (*http.Response, error) {
	const attempts = 30
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/api/stream", nil)
		if err != nil {
			return nil, err
		}
		resp, err := n.client.Do(req)
		if err == nil {
			return resp, nil
		}
		// 失败退避后重试。
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("无法连接远端串口代理 %s/api/stream（重试 %d 次后放弃）", n.base, attempts)
}

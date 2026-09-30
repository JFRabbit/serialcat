package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
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

// NewSerialRemote 构造远端串口来源节点。baseURL 形如 http://192.168.1.10:7100 或带尾斜杠;
// 缺协议(如 192.168.1.10:7100)时自动补 http://。
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

// Run 连接远端流并批量发射。断线自动重连续流：每次接入一轮连接读取；
// 终端中断不算 DAG 失败，而是打"断档"标记后退避重连。仅在 ctx 取消/永久错误时结束。
// in 对 source 为 nil。
func (n *SerialRemote) Run(ctx context.Context, _ <-chan []dag.Item) error {
	defer close(n.out)

	backoff := backoffBase
	for {
		body, perm, derr := n.dialStream(ctx)
		if derr != nil {
			if perm {
				return derr // 永久错误（如 4xx/URL 配置错）：不再重试，让用户尽快发现
			}
			if !sleepWithBackoff(ctx, &backoff) {
				return ctx.Err()
			}
			continue
		}

		backoff = backoffBase // 连上后重置退避
		_ = n.readLoop(ctx, body)
		_ = body.Close()
		if ctx.Err() != nil {
			return ctx.Err() // 用户主动取消/退出，正常结束
		}
		// 流中断：先向输出插入"断档"标记，再退避重连，做到连续不静默。
		if gerr := n.emitGap(ctx); gerr != nil {
			return gerr
		}
		if !sleepWithBackoff(ctx, &backoff) {
			return ctx.Err()
		}
	}
}

// readLoop 消费一次已建立连接的远端流：解码 → 切批 → 发射，期间压缩 100ms 半批。
// 返回 nil 表示 ctx 取消正常结束；返回 err 表示流中断（由外层重连）。
func (n *SerialRemote) readLoop(ctx context.Context, body io.Reader) error {
	dec := json.NewDecoder(body)
	frames := make(chan remoteFrame, 64)
	decErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		var f remoteFrame
		for {
			if err := dec.Decode(&f); err != nil {
				select {
				case decErr <- err:
				case <-done:
				}
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
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
			// 尽力发射剩余半批，再报告中断（连续源"断连"=异常，但不结束，外层重连）。
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// emitGap 向输出插入一条断档标记项，让下游（console/落盘）显式看到缺口。
func (n *SerialRemote) emitGap(ctx context.Context) error {
	gap := dag.Item{
		Ts:   time.Now(),
		Text: "……串口流中断，此处有断档……",
		Meta: map[string]string{"gap": "break"},
	}
	select {
	case n.out <- []dag.Item{gap}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flushIdle 是低速率串口的半批周期发射间隔。
const flushIdle = 100 * time.Millisecond

// 重连退避：初始 500ms，指数增长封顶 30s，各次带随机抖动防多端齐撞。
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 30 * time.Second
)

// sleepWithBackoff 等待一次退避间隔（含抖动）并倍增 backoff。返回 false 表示 ctx 已取消。
func sleepWithBackoff(ctx context.Context, backoff *time.Duration) bool {
	jitter := time.Duration(rand.Int63n(int64(*backoff) / 2))
	delay := *backoff + jitter
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
	}
	*backoff *= 2
	if *backoff > backoffMax {
		*backoff = backoffMax
	}
	return true
}

// dialStream 发起一次到 /api/stream 的请求，返回响应体。
// perm 为 true 表示永久错误（4xx：URL 配错/鉴权失败），不应重试；否则视为可重连的瞬时错误。
func (n *SerialRemote) dialStream(ctx context.Context) (io.ReadCloser, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/api/stream", nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, false, err // 网络错：可重试
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, resp.StatusCode < 500, fmt.Errorf("远端串口代理 /api/stream 返回 %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, false, nil
}

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"logdiag/pkg/dag"
)

// testGrepNode 测试用过滤节点：转发含 sub 的行。
type testGrepNode struct {
	id  string
	sub string
	out chan []dag.Item
}

func newTestGrepNode(id, sub string) *testGrepNode {
	return &testGrepNode{id: id, sub: sub, out: make(chan []dag.Item, 8)}
}
func (n *testGrepNode) Name() string                 { return n.id }
func (n *testGrepNode) Out() (chan []dag.Item, bool) { return n.out, true }
func (n *testGrepNode) Run(ctx context.Context, in <-chan []dag.Item) error {
	for batch := range in {
		var out []dag.Item
		for _, it := range batch {
			if strings.Contains(it.Text, n.sub) {
				out = append(out, it)
			}
		}
		if len(out) == 0 {
			continue
		}
		select {
		case n.out <- out:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(n.out)
	return nil
}

// collectSinkNode 测试用 sink：收集所有传入 Item。
type collectSinkNode struct {
	id    string
	items []dag.Item
}

func (c *collectSinkNode) Name() string                 { return c.id }
func (c *collectSinkNode) Out() (chan []dag.Item, bool) { return nil, false }
func (c *collectSinkNode) Run(_ context.Context, in <-chan []dag.Item) error {
	for batch := range in {
		c.items = append(c.items, batch...)
	}
	return nil
}

// fakeAgentStream 模拟 logdiag agent 的 /api/stream：发射 n 帧后保持连接，等待客户端断开。
func fakeAgentStream(n int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stream" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		for i := 0; i < n; i++ {
			_ = enc.Encode(remoteFrame{Ts: time.Now(), Text: fmt.Sprintf("slog line %d", i)})
			fl.Flush()
		}
		<-r.Context().Done() // 保持连接直到客户端断开
	}))
}

// TestSerialRemoteStream 验证 serial-remote 节点能从远端流取数并批量发射。
func TestSerialRemoteStream(t *testing.T) {
	const n = 5000 // 超阈值，触发批量切分
	srv := fakeAgentStream(n)
	defer srv.Close()

	target := NewSerialRemote("ser", srv.URL, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- target.Run(ctx, nil) }()

	got := 0
collect:
	for got < n {
		select {
		case b, ok := <-target.Out():
			if !ok {
				break collect
			}
			got += len(b)
		case <-done:
			break collect
		case <-time.After(10 * time.Second):
			t.Fatalf("等待数据超时, 已收 %d/%d", got, n)
		}
	}
	if got != n {
		t.Fatalf("收到 %d 条, 期望 %d", got, n)
	}
	cancel()
}

// TestSerialRemoteRetriesConnect 验证代理未就绪时节点会重试连接（而非立即失败）。
func TestSerialRemoteRetriesConnect(t *testing.T) {
	var url string
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ready // 未就绪时挂起，模拟 agent 未启动
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(remoteFrame{Ts: time.Now(), Text: "hello"})
		fl.Flush()
		<-r.Context().Done()
	}))
	url = srv.URL
	defer srv.Close()

	target := NewSerialRemote("ser", url, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- target.Run(ctx, nil) }()

	close(ready) // agent 就绪，连接应建立并收到数据

	select {
	case b := <-target.Out():
		if len(b) < 1 || b[0].Text != "hello" {
			t.Fatalf("收到异常数据: %+v", b)
		}
	case err := <-done:
		t.Fatalf("意外结束: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("超时未收到数据")
	}
	cancel()
}

// TestSerialRemoteFeedDAG 验证 serial-remote 能作为 source 接入整个 DAG（过批量引擎 + 扇出）。
func TestSerialRemoteFeedDAG(t *testing.T) {
	const n = 30
	srv := fakeAgentStream(n)
	defer srv.Close()

	g := dag.NewGraph()
	g.AddNode(NewSerialRemote("ser", srv.URL, 0))
	g.AddNode(newTestGrepNode("grep", "line"))
	sink := &collectSinkNode{id: "sink"}
	g.AddNode(sink)
	g.AddEdge("ser", "grep", 0)
	g.AddEdge("grep", "sink", 0)

	// 连续源不会自然 EOS：用超时 ctx 停止。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.Run(ctx) // 超时 caused cancel，err 忽略

	if got := len(sink.items); got != n {
		t.Fatalf("DAG 扇出收到 %d 条, 期望 %d", got, n)
	}
}

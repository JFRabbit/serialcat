package dag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 写一个含 n 行（每行含 "want"）的临时文件，供 testSource 读取。
func writeLinesFile(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "in.txt")
	var in string
	for i := 0; i < n; i++ {
		in += "line want " + strings.Repeat("x", i%3) + "\n"
	}
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGraphRunFanIn 覆盖多上游扇入：writeCount 合并两个 writer，
// 两个 source 都 EOS 后下游缓冲才被关闭，sink 收集到两路合并的数据。
func TestGraphRunFanIn(t *testing.T) {
	g := NewGraph()
	s1 := newTestSource("s1", writeLinesFile(t, 6))
	s2 := newTestSource("s2", writeLinesFile(t, 4))
	g.AddNode(s1)
	g.AddNode(s2)
	c := &collectSink{id: "c"}
	g.AddNode(c)
	// 扇入：s1、s2 同时送 c
	g.AddEdge("s1", "c", 0)
	g.AddEdge("s2", "c", 0)

	if err := g.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.items) != 10 {
		t.Fatalf("扇入合并结果错误: got=%d want=10", len(c.items))
	}
}

// failNode：Run 立即返回错误的 source，用于验证错误传播与全图取消。
type failNode struct {
	id  string
	err error
}

func (n *failNode) Name() string       { return n.id }
func (n *failNode) Out() <-chan []Item { return make(chan []Item) } // 永不关闭也不发送
func (n *failNode) Run(context.Context, <-chan []Item) error {
	return n.err
}

// TestGraphRunNodeError 覆盖：某节点 Run 返回错误时全图 cancel，
// 且返回错误带 "node %s:" 前缀（fail 的 failNode 扇出到阻塞 sink，验证不挂死）。
func TestGraphRunNodeError(t *testing.T) {
	g := NewGraph()
	boom := errors.New("boom")
	g.AddNode(&failNode{id: "bad", err: boom})
	c := &collectSink{id: "c"}
	g.AddNode(c)
	g.AddEdge("bad", "c", 0)

	done := make(chan error, 1)
	go func() { done <- g.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("期望返回错误，实际为 nil")
		}
		want := "node bad:"
		if !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("错误前缀不符: got=%q want 前缀 %q", err.Error(), want)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("错误未包裹原始错误: got=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 挂死：节点错误未触发全图取消")
	}
}

// blockNode：Run 阻塞在 ctx.Done 上的 source，用于验证外部取消时 Run 正常返回。
type blockNode struct {
	id string
}

func (n *blockNode) Name() string       { return n.id }
func (n *blockNode) Out() <-chan []Item { return make(chan []Item) } // 永不发送、永不关闭
func (n *blockNode) Run(ctx context.Context, _ <-chan []Item) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestGraphRunCtxCancel 覆盖：外部 ctx 取消时 Run 能正常返回（不挂死），
// 返回的错误可经 errors.Is 判定为 context.Canceled。
func TestGraphRunCtxCancel(t *testing.T) {
	g := NewGraph()
	g.AddNode(&blockNode{id: "blk"})
	c := &collectSink{id: "c"}
	g.AddNode(c)
	g.AddEdge("blk", "c", 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	cancel() // 立即取消

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应包裹 context.Canceled，got=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 挂死：ctx 取消后未返回")
	}
}

// TestGraphRunBufNonPositive 覆盖：AddEdge 的 Buf<=0 时走 defaultBufSize 默认值，
// 图仍能正确完成 EOS 与数据传递。
func TestGraphRunBufNonPositive(t *testing.T) {
	g := NewGraph()
	src := newTestSource("src", writeLinesFile(t, 5))
	g.AddNode(src)
	c := &collectSink{id: "c"}
	g.AddNode(c)
	// Buf 传负数，应回退到默认缓冲，不影响数据传递
	g.AddEdge("src", "c", -8)

	if err := g.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.items) != 5 {
		t.Fatalf("Buf<=0 时数据错误: got=%d want=5", len(c.items))
	}
}

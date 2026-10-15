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

// writeLinesFile writes a temp file with n lines (each containing "want") for testSource to read.
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

// TestGraphRunFanIn covers multi-upstream fan-in: writeCount merges two writers,
// the downstream buffer is only closed after both sources EOS, and the sink collects the merged data from both paths.
func TestGraphRunFanIn(t *testing.T) {
	g := NewGraph()
	s1 := newTestSource("s1", writeLinesFile(t, 6))
	s2 := newTestSource("s2", writeLinesFile(t, 4))
	g.AddNode(s1)
	g.AddNode(s2)
	c := &collectSink{id: "c"}
	g.AddNode(c)
	// fan-in: s1 and s2 both feed c
	g.AddEdge("s1", "c", 0)
	g.AddEdge("s2", "c", 0)

	if err := g.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.items) != 10 {
		t.Fatalf("fan-in merge result wrong: got=%d want=10", len(c.items))
	}
}

// failNode: a source whose Run returns an error immediately, used to verify error propagation and graph-wide cancellation.
type failNode struct {
	id  string
	err error
}

func (n *failNode) Name() string       { return n.id }
func (n *failNode) Out() <-chan []Item { return make(chan []Item) } // never closes or sends
func (n *failNode) Run(context.Context, <-chan []Item) error {
	return n.err
}

// TestGraphRunNodeError covers: when a node's Run returns an error, the whole graph
// is cancelled, and the returned error carries the "node %s:" prefix
// (the failing failNode fans out to a blocking sink to verify no hang).
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
			t.Fatal("expected an error, got nil")
		}
		want := "node bad:"
		if !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("error prefix mismatch: got=%q want prefix %q", err.Error(), want)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("error does not wrap the original error: got=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung: node error did not trigger graph-wide cancel")
	}
}

// blockNode: a source whose Run blocks on ctx.Done, used to verify Run returns normally on external cancel.
type blockNode struct {
	id string
}

func (n *blockNode) Name() string       { return n.id }
func (n *blockNode) Out() <-chan []Item { return make(chan []Item) } // never sends or closes
func (n *blockNode) Run(ctx context.Context, _ <-chan []Item) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestGraphRunCtxCancel covers: Run returns normally (no hang) when the external
// ctx is cancelled, and the returned error can be matched to context.Canceled via errors.Is.
func TestGraphRunCtxCancel(t *testing.T) {
	g := NewGraph()
	g.AddNode(&blockNode{id: "blk"})
	c := &collectSink{id: "c"}
	g.AddNode(c)
	g.AddEdge("blk", "c", 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	cancel() // cancel immediately

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("should wrap context.Canceled, got=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung: did not return after ctx cancel")
	}
}

// TestGraphRunBufNonPositive covers: AddEdge with Buf<=0 falls back to the defaultBufSize,
// and the graph still completes EOS and data delivery correctly.
func TestGraphRunBufNonPositive(t *testing.T) {
	g := NewGraph()
	src := newTestSource("src", writeLinesFile(t, 5))
	g.AddNode(src)
	c := &collectSink{id: "c"}
	g.AddNode(c)
	// negative Buf should fall back to the default buffer without affecting data delivery
	g.AddEdge("src", "c", -8)

	if err := g.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.items) != 5 {
		t.Fatalf("data error with Buf<=0: got=%d want=5", len(c.items))
	}
}

package dag

import (
	"bufio"
	"context"
	"os"
	"strings"
	"sync"
)

// testSource：复用文件语义的最小 source（读文件按批发射，EOF 关 out）。
type testSource struct {
	id   string
	path string
	out  chan []Item
}

func newTestSource(id, path string) *testSource {
	return &testSource{id: id, path: path, out: make(chan []Item, 8)}
}
func (n *testSource) Name() string       { return n.id }
func (n *testSource) Out() <-chan []Item { return n.out }
func (n *testSource) Run(ctx context.Context, _ <-chan []Item) error {
	f, err := os.Open(n.path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var batch []Item
	for sc.Scan() {
		batch = append(batch, NewItem(sc.Text()))
		if len(batch) >= 4 { // 小批量便于测试切批，与引擎扇出/EOS 无关
			if err := emit(ctx, n.out, batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if err := emit(ctx, n.out, batch); err != nil {
			return err
		}
	}
	close(n.out)
	return nil
}

func emit(ctx context.Context, out chan []Item, batch []Item) error {
	select {
	case out <- batch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// testGrep：按子串过滤的 process。
type testGrep struct {
	id  string
	sub string
	out chan []Item
}

func newTestGrep(id string) *testGrep {
	return &testGrep{id: id, sub: "want", out: make(chan []Item, 8)}
}
func (n *testGrep) Name() string             { return n.id }
func (n *testGrep) Out() (chan []Item, bool) { return n.out, true }
func (n *testGrep) Run(ctx context.Context, in <-chan []Item) error {
	for batch := range in {
		var out []Item
		for _, it := range batch {
			if strings.Contains(it.Text, n.sub) {
				out = append(out, it)
			}
		}
		if len(out) == 0 {
			continue
		}
		if err := emit(ctx, n.out, out); err != nil {
			return err
		}
	}
	close(n.out)
	return nil
}

// collectSink：收集所有 Item 的 sink。
type collectSink struct {
	id    string
	mu    sync.Mutex
	items []Item
}

func (c *collectSink) Name() string             { return c.id }
func (c *collectSink) Out() (chan []Item, bool) { return nil, false }
func (c *collectSink) Run(_ context.Context, in <-chan []Item) error {
	for batch := range in {
		c.mu.Lock()
		c.items = append(c.items, batch...)
		c.mu.Unlock()
	}
	return nil
}

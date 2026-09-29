package dag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphRunEOSAndFanout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in.txt")
	// 构造输入：10 行，其中 3 行含 "want"
	var in string
	for i := 0; i < 10; i++ {
		line := "line " + strings.Repeat("x", i)
		if i%3 == 0 {
			line += " want"
		}
		in += line + "\n"
	}
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}

	g := NewGraph()
	src := newTestSource("src", path)
	g.AddNode(src)
	g.AddNode(newTestGrep("grep"))
	g.AddEdge("src", "grep", 0)
	c1 := &collectSink{id: "c1"}
	g.AddNode(c1)
	c2 := &collectSink{id: "c2"}
	g.AddNode(c2)
	// 扇出：grep 同时送 c1、c2
	g.AddEdge("grep", "c1", 0)
	g.AddEdge("grep", "c2", 0)

	if err := g.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 期望 4 行含 want（i=0,3,6,9）
	want := 4
	if len(c1.items) != want || len(c2.items) != want {
		t.Fatalf("扇出结果不一致: c1=%d c2=%d want=%d", len(c1.items), len(c2.items), want)
	}
}
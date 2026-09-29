package nodes

import (
	"context"
	"strings"
	"testing"
	"time"

	"logdiag/pkg/dag"
)

// TestProcessRoundTrip 验证 process 节点批量 NDJSON 往返：
// 输入批经外部 python 脚本（upper）转大写后回传，TEXT 变大写。
func TestProcessRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	p := NewProcess("proc", "python3", []string{"-c", upperInline}, 4)

	in := make(chan []dag.Item, 1)
	in <- []dag.Item{{Ts: time.Now(), Text: "hello"}, {Ts: time.Now(), Text: "world"}}
	close(in)

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(context.Background(), in) }()

	var texts []string
	for b := range p.out {
		for _, it := range b {
			texts = append(texts, it.Text)
		}
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.Join(texts, " "); got != "HELLO WORLD" {
		t.Fatalf("往返结果不符: got=%q want=%q", got, "HELLO WORLD")
	}
}

// upperInline 是与 scripts/upper.py 等价的内联版，测试不依赖仓库文件。
const upperInline = `
import json, sys
for line in sys.stdin:
    line=line.strip()
    if not line: continue
    batch=json.loads(line)
    out=[{**it, "text": it.get("text","").upper()} for it in batch]
    sys.stdout.write(json.dumps(out)+"\n")
    sys.stdout.flush()
`
package nodes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

// FileSource 是一次性读取文件的 source 节点。
// 逐行读取，按 dag.BatchMaxItems / BatchMaxBytes 切批后以 []Item 发射；
// 读到 EOF 后发射剩余批次并关闭 Out，触发 EOS 传播。
type FileSource struct {
	id   string
	path string
	out  chan []dag.Item
}

var _ dag.SourceNode = (*FileSource)(nil)

// NewFileSource 构造文件来源节点。
func NewFileSource(id, path string, buf int) *FileSource {
	if buf <= 0 {
		buf = 128
	}
	return &FileSource{id: id, path: path, out: make(chan []dag.Item, buf)}
}

// Name 实现 dag.Node。
func (n *FileSource) Name() string { return n.id }

// Out 实现 dag.SourceNode。
func (n *FileSource) Out() <-chan []dag.Item { return n.out }

// Run 读取整份文件，切批发射。in 对 source 为 nil。
func (n *FileSource) Run(ctx context.Context, _ <-chan []dag.Item) error {
	f, err := os.Open(n.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", n.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	// 一批共享一个时间戳，避免逐行调用 time.Now()。
	ts := dag.LazyTime()
	buf := make([]dag.Item, 0, dag.BatchMaxItems)
	var bytes int
	emit := func() error {
		if len(buf) == 0 {
			return nil
		}
		select {
		case n.out <- buf:
			buf = make([]dag.Item, 0, dag.BatchMaxItems)
			bytes = 0
			ts = dag.LazyTime()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for sc.Scan() {
		text := sc.Text()
		buf = append(buf, dag.Item{Ts: ts, Text: text})
		bytes += len(text)
		// 达到行数或字节任一阈值即切批，防止单批过大。
		if len(buf) >= dag.BatchMaxItems || bytes >= dag.BatchMaxBytes {
			if err := emit(); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read %s: %w", n.path, err)
	}
	if err := emit(); err != nil { // 发射剩余半批
		return err
	}
	close(n.out)
	return nil
}

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

// FileSource is a source node that reads a file once.
// It reads line by line, splits into batches by dag.BatchMaxItems / BatchMaxBytes
// and emits them as []Item; after EOF it emits the remaining batch and closes Out,
// triggering EOS propagation.
type FileSource struct {
	id   string
	path string
	out  chan []dag.Item
}

var _ dag.SourceNode = (*FileSource)(nil)

// NewFileSource builds a file source node.
func NewFileSource(id, path string, buf int) *FileSource {
	if buf <= 0 {
		buf = 128
	}
	return &FileSource{id: id, path: path, out: make(chan []dag.Item, buf)}
}

// Name implements dag.Node.
func (n *FileSource) Name() string { return n.id }

// Out implements dag.SourceNode.
func (n *FileSource) Out() <-chan []dag.Item { return n.out }

// Run reads the whole file and emits it in batches. in is nil for sources.
func (n *FileSource) Run(ctx context.Context, _ <-chan []dag.Item) error {
	f, err := os.Open(n.path)
	if err != nil {
		return fmt.Errorf("open %s: %w", n.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	// A batch shares one timestamp, avoiding a time.Now() call per line.
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
		// Split on either the line or byte threshold, preventing a single batch from growing too large.
		if len(buf) >= dag.BatchMaxItems || bytes >= dag.BatchMaxBytes {
			if err := emit(); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read %s: %w", n.path, err)
	}
	if err := emit(); err != nil { // emit the remaining partial batch
		return err
	}
	close(n.out)
	return nil
}

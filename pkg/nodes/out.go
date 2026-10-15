package nodes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

// Grep is a filter node: reads batches from in, applies regex matching per item,
// and writes hits to out in batches.
// Filtering does not change the nature of the data; it only "narrows down" the logs.
type Grep struct {
	id  string
	re  *regexp.Regexp
	out chan []dag.Item
}

var _ dag.ProcessNode = (*Grep)(nil)

// NewGrep builds a filter node. pattern is compiled as a regular expression.
func NewGrep(id, pattern string, buf int) (*Grep, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if buf <= 0 {
		buf = 128
	}
	return &Grep{id: id, re: re, out: make(chan []dag.Item, buf)}, nil
}

// Name implements dag.Node.
func (n *Grep) Name() string { return n.id }

// Out implements dag.ProcessNode.
func (n *Grep) Out() (chan []dag.Item, bool) { return n.out, true }

// Run reads from in and filters. It ends when in closes (upstream EOS), then closes out.
// Hits are regrouped into new batches and emitted; empty batches are skipped.
func (n *Grep) Run(ctx context.Context, in <-chan []dag.Item) error {
	for batch := range in {
		out := make([]dag.Item, 0, len(batch))
		for _, it := range batch {
			if n.re.MatchString(it.Text) {
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

// Console is a sink node: reads from in and prints, with no output.
type Console struct {
	id string
	w  io.Writer
}

var _ dag.ProcessNode = (*Console)(nil)

// NewConsole builds a console output node.
func NewConsole(id string) *Console { return &Console{id: id, w: os.Stdout} }

// Name implements dag.Node.
func (n *Console) Name() string { return n.id }

// Out implements dag.ProcessNode: sinks have no output.
func (n *Console) Out() (chan []dag.Item, bool) { return nil, false }

// Run prints line by line. It ends when in closes.
func (n *Console) Run(ctx context.Context, in <-chan []dag.Item) error {
	if n.w == nil {
		n.w = os.Stdout
	}
	for batch := range in {
		for _, it := range batch {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if _, err := fmt.Fprintln(n.w, it.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

// FileSink is a write-to-disk node: reads from in, writes lines to a file, flushes when in closes.
// path supports the {ts} template: it expands at the start of Run to the run's start timestamp
// (format 20060102-150405, local timezone), archiving each run into its own file.
type FileSink struct {
	id       string
	pathTmpl string // template that may contain {ts}
}

var _ dag.ProcessNode = (*FileSink)(nil)

// NewFileSink builds a write-to-disk output node.
func NewFileSink(id, path string) *FileSink { return &FileSink{id: id, pathTmpl: path} }

// Name implements dag.Node.
func (n *FileSink) Name() string { return n.id }

// Out implements dag.ProcessNode: sinks have no output.
func (n *FileSink) Out() (chan []dag.Item, bool) { return nil, false }

// expandPath replaces the {ts} placeholder in pathTmpl with the current run timestamp.
func (n *FileSink) expandPath() string {
	ts := time.Now().Format("20060102-150405")
	return strings.ReplaceAll(n.pathTmpl, "{ts}", ts)
}

// Run writes line by line and flushes per line (real-time disk write, preventing
// data from being stranded in the buffer and lost mid-run).
// If the target file is externally deleted/moved during the run, it automatically
// recreates a same-named file and keeps appending, so no data is lost.
func (n *FileSink) Run(ctx context.Context, in <-chan []dag.Item) error {
	path := n.expandPath()
	// Automatically create parent directories, covering the case where the target path does not exist.
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	w := bufio.NewWriter(f)
	curPath := path
	defer func() { _ = w.Flush(); _ = f.Close() }()

	for batch := range in {
		for _, it := range batch {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			// When the file is externally deleted/moved, recreate a same-named file and keep appending (consistent with the agent's disk write).
			if _, err := os.Stat(curPath); err != nil {
				if nf, npath, e := recreateSinkFile(curPath); e == nil {
					_ = w.Flush()
					_ = f.Close()
					f, w, curPath = nf, bufio.NewWriter(nf), npath
				} else {
					return fmt.Errorf("recreate sink file %s: %w", curPath, e)
				}
			}
			if _, err := w.WriteString(it.Text + "\n"); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

// recreateSinkFile recreates the sink file after it has been deleted: same name first;
// if creating the same name fails (e.g. occupied by a directory), an incrementing suffix is appended.
func recreateSinkFile(name string) (*os.File, string, error) {
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", fmt.Errorf("recreate directory %s: %w", dir, err)
		}
	}
	if f, err := os.Create(name); err == nil {
		return f, name, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i <= 1000; i++ {
		alt := fmt.Sprintf("%s-%d%s", base, i, ext)
		if f, err := os.Create(alt); err == nil {
			return f, alt, nil
		}
	}
	return nil, "", fmt.Errorf("cannot recreate sink file (same name and 1000 suffixes all failed): %s", name)
}

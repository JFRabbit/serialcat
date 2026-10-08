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

	"logdiag/pkg/dag"
)

// Grep 过滤节点：从 in 按批读取，逐条做正则判定，命中按批写 out。
// 过滤不改变数据本质，只是"缩小"日志。
type Grep struct {
	id  string
	re  *regexp.Regexp
	out chan []dag.Item
}

var _ dag.ProcessNode = (*Grep)(nil)

// NewGrep 构造过滤节点。pattern 编译为正则。
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

// Name 实现 dag.Node。
func (n *Grep) Name() string { return n.id }

// Out 实现 dag.ProcessNode。
func (n *Grep) Out() (chan []dag.Item, bool) { return n.out, true }

// Run 从 in 读取并过滤。in 关闭即结束（上游 EOS），然后关闭 out。
// 命中项重组为新批次发射，空批次跳过。
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

// Console 是 sink 节点：从 in 读取并打印，无输出。
type Console struct {
	id string
	w  io.Writer
}

var _ dag.ProcessNode = (*Console)(nil)

// NewConsole 构造控制台输出节点。
func NewConsole(id string) *Console { return &Console{id: id, w: os.Stdout} }

// Name 实现 dag.Node。
func (n *Console) Name() string { return n.id }

// Out 实现 dag.ProcessNode：sink 无输出。
func (n *Console) Out() (chan []dag.Item, bool) { return nil, false }

// Run 逐条打印。in 关闭即结束。
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

// FileSink 是落盘节点：从 in 读取，逐行写入文件，in 关闭时 flush。
// path 支持 {ts} 模板：会在 Run 开始时展开为本次运行的起始时间戳
// （格式 20060102-150405，本地时区），便于每次运行归档独立文件。
type FileSink struct {
	id       string
	pathTmpl string // 可能含 {ts} 的模板
}

var _ dag.ProcessNode = (*FileSink)(nil)

// NewFileSink 构造落盘输出节点。
func NewFileSink(id, path string) *FileSink { return &FileSink{id: id, pathTmpl: path} }

// Name 实现 dag.Node。
func (n *FileSink) Name() string { return n.id }

// Out 实现 dag.ProcessNode：sink 无输出。
func (n *FileSink) Out() (chan []dag.Item, bool) { return nil, false }

// expandPath 将 pathTmpl 中的 {ts} 占位替换为本次运行时间戳。
func (n *FileSink) expandPath() string {
	ts := time.Now().Format("20060102-150405")
	return strings.ReplaceAll(n.pathTmpl, "{ts}", ts)
}

// Run 逐行写入并每行 flush（实时落盘，防止数据滞留在缓冲区导致中途丢失）。
// 若目标文件在运行期被外部删除/移走，自动重建同名文件继续追加，保证不丢数据。
func (n *FileSink) Run(ctx context.Context, in <-chan []dag.Item) error {
	path := n.expandPath()
	// 自动创建父目录，兼容目标路径不存在的场景。
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
			// 文件被外部删除/移走时，重建同名文件继续追加（与 agent 落盘一致）。
			if _, err := os.Stat(curPath); err != nil {
				if nf, npath, e := recreateSinkFile(curPath); e == nil {
					_ = w.Flush()
					_ = f.Close()
					f, w, curPath = nf, bufio.NewWriter(nf), npath
				} else {
					return fmt.Errorf("重建落盘文件 %s: %w", curPath, e)
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

// recreateSinkFile 在落盘文件被删除后重建：优先同名；同名创建失败（如被目录占用）则自动递增后缀。
func recreateSinkFile(name string) (*os.File, string, error) {
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", fmt.Errorf("重建目录 %s: %w", dir, err)
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
	return nil, "", fmt.Errorf("无法重建落盘文件（同名及 1000 个后缀均失败）: %s", name)
}

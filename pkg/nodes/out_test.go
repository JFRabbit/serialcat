package nodes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logdiag/pkg/dag"
)

// feedBatches 把若干批次写入 in 后关闭，模拟上游 EOS。
func feedBatches(in chan<- []dag.Item, batches [][]dag.Item) {
	for _, b := range batches {
		in <- b
	}
	close(in)
}

func itemBatch(texts ...string) []dag.Item {
	ts := time.Now()
	items := make([]dag.Item, 0, len(texts))
	for _, s := range texts {
		items = append(items, dag.Item{Ts: ts, Text: s})
	}
	return items
}

// TestFileSinkRunRecreate 覆盖运行期文件被外部删除后的重建分支：
// 写完第一行后删除目标文件，后续行应触发重建（同名能建则继续同名），数据不丢。
func TestFileSinkRunRecreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	sink := NewFileSink("k", path)

	in := make(chan []dag.Item, 4)
	done := make(chan error, 1)
	go func() { done <- sink.Run(context.Background(), in) }()

	in <- itemBatch("line-1")
	// 等第一行落盘后删掉文件，模拟外部移走。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待首行落盘超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除目标文件: %v", err)
	}
	// 第二批：应走重建分支，写入同名新文件。
	in <- itemBatch("line-2", "line-3")
	close(in)

	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取重建后的文件: %v", err)
	}
	// line-1 在被删的旧 fd 上（已随文件删除），重建文件只承接删除之后的数据。
	got := strings.TrimRight(string(data), "\n")
	if got != "line-2\nline-3" {
		t.Fatalf("重建后内容不对，得到 %q", got)
	}
}

// TestFileSinkRunDirOccupiedMidRun 覆盖运行期同名被目录占用的回退分支：
// 文件被删且同名目录占位后，os.Stat 对目录仍成功（不会走重建），
// 行写入旧的已删除 fd 不报错，Run 应整体顺利完成、不因异常中断。
// 真正的同名重建递增后缀逻辑见 TestRecreateSinkFileDirOccupied。
func TestFileSinkRunDirOccupiedMidRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	sink := NewFileSink("k", path)

	in := make(chan []dag.Item, 4)
	done := make(chan error, 1)
	go func() { done <- sink.Run(context.Background(), in) }()

	in <- itemBatch("before")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待首行落盘超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除原文件: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("放同名目录: %v", err)
	}

	in <- itemBatch("after-dir")
	close(in)
	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	// 同名路径现在是目录，Run 不应再往里写，也不应报错。
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("同名目录应保持为目录: %v", err)
	}
}

// TestRecreateSinkFileDirOccupied 直接覆盖 recreateSinkFile 的同名被目录占用分支。
func TestRecreateSinkFileDirOccupied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("放同名目录: %v", err)
	}

	f, alt, err := recreateSinkFile(path)
	if err != nil {
		t.Fatalf("recreateSinkFile 返回错误: %v", err)
	}
	defer f.Close()
	if alt == path {
		t.Fatalf("同名被目录占用时应改用替代名，仍返回 %s", alt)
	}
	if filepath.Dir(alt) != dir || !strings.HasPrefix(filepath.Base(alt), "x-") {
		t.Fatalf("替代名不符合约定: %s", alt)
	}
}

// TestRecreateSinkFileSameName 覆盖同名可建时直接复用原名的分支。
func TestRecreateSinkFileSameName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "y.log")
	f, alt, err := recreateSinkFile(path)
	if err != nil {
		t.Fatalf("recreateSinkFile 返回错误: %v", err)
	}
	defer f.Close()
	if alt != path {
		t.Fatalf("同名可建时应返回原名，得到 %s", alt)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应已创建: %v", err)
	}
}

// TestConsoleRun 注入 bytes.Buffer 到 Console.w，验证逐行打印输出。
func TestConsoleRun(t *testing.T) {
	c := NewConsole("c")
	var buf bytes.Buffer
	c.w = &buf

	in := make(chan []dag.Item, 2)
	in <- itemBatch("a", "b")
	in <- itemBatch("c")
	close(in)

	if err := c.Run(context.Background(), in); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if got := buf.String(); got != "a\nb\nc\n" {
		t.Fatalf("输出不对，得到 %q", got)
	}
}

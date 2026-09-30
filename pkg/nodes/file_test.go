package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"logdiag/pkg/dag"
)

// drainOut 把 source 输出通道读到关闭为止，返回各批长度。
func drainOut(t *testing.T, out <-chan []dag.Item) []int {
	t.Helper()
	var sizes []int
	for batch := range out {
		sizes = append(sizes, len(batch))
	}
	return sizes
}

// writeLines 生成 n 行定长文本写入临时文件，返回路径。
func writeLines(t *testing.T, n int, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.log")
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("写输入文件: %v", err)
	}
	return path
}

// TestFileSourceRunSmall 验证普通读取：行数远小于切批阈值时，整份文件作为一批发射，
// 尾部半批（不满阈值）仍会发射，且 EOF 后 out 被关闭。
func TestFileSourceRunSmall(t *testing.T) {
	path := writeLines(t, 10, "hello")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if len(sizes) != 1 || sizes[0] != 10 {
		t.Fatalf("期望单批 10 行，得到 %v", sizes)
	}
}

// TestFileSourceRunOpenError 覆盖文件打不开的分支。
func TestFileSourceRunOpenError(t *testing.T) {
	src := NewFileSource("s", filepath.Join(t.TempDir(), "not-exist.log"), 0)
	if err := src.Run(context.Background(), nil); err == nil {
		t.Fatal("期望 open 错误，得到 nil")
	}
}

// TestFileSourceRunBatchByItems 覆盖行数阈值切批：
// 2*BatchMaxItems+1 行应切成 BatchMaxItems、BatchMaxItems、1 三批。
func TestFileSourceRunBatchByItems(t *testing.T) {
	total := dag.BatchMaxItems*2 + 1
	path := writeLines(t, total, "x")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	want := []int{dag.BatchMaxItems, dag.BatchMaxItems, 1}
	if len(sizes) != len(want) {
		t.Fatalf("期望批数 %d，得到 %d: %v", len(want), len(sizes), sizes)
	}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("第 %d 批期望 %d 行，得到 %d（全部 %v）", i, w, sizes[i], sizes)
		}
	}
}

// TestFileSourceRunBatchByBytes 覆盖字节阈值切批：
// 每行 1KB，BatchMaxBytes/1024 行恰好凑满 64KB 即切批，剩余行为尾部半批。
func TestFileSourceRunBatchByBytes(t *testing.T) {
	per := 1024
	perBatch := dag.BatchMaxBytes / per // 64 行一批
	total := perBatch + perBatch/2      // 64 + 32 = 96 行 → 64 + 32 两批
	path := writeLines(t, total, strings.Repeat("a", per))
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	want := []int{perBatch, total - perBatch}
	if len(sizes) != len(want) {
		t.Fatalf("期望批数 %d，得到 %d: %v", len(want), len(sizes), sizes)
	}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("第 %d 批期望 %d 行，得到 %d（全部 %v）", i, w, sizes[i], sizes)
		}
	}
}

// TestFileSourceRunExactFullBatches 覆盖"恰好好批、无尾部半批"场景：
// 行数恰为 BatchMaxItems 的整数倍时，最后一批由阈值切出，不应多出一个空批。
func TestFileSourceRunExactFullBatches(t *testing.T) {
	path := writeLines(t, dag.BatchMaxItems, "y")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
	if len(sizes) != 1 || sizes[0] != dag.BatchMaxItems {
		t.Fatalf("期望单批 %d 行，得到 %v", dag.BatchMaxItems, sizes)
	}
}

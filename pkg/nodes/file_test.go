package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

// drainOut reads a source's output channel until it closes, returning the length of each batch.
func drainOut(t *testing.T, out <-chan []dag.Item) []int {
	t.Helper()
	var sizes []int
	for batch := range out {
		sizes = append(sizes, len(batch))
	}
	return sizes
}

// writeLines writes n fixed-length text lines to a temp file and returns the path.
func writeLines(t *testing.T, n int, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.log")
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write input file: %v", err)
	}
	return path
}

// TestFileSourceRunSmall verifies normal reading: when the line count is far below the split
// threshold, the whole file is emitted as one batch; the trailing partial batch (below the
// threshold) is still emitted, and out is closed after EOF.
func TestFileSourceRunSmall(t *testing.T) {
	path := writeLines(t, 10, "hello")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(sizes) != 1 || sizes[0] != 10 {
		t.Fatalf("expected a single batch of 10 lines, got %v", sizes)
	}
}

// TestFileSourceRunOpenError covers the branch where the file cannot be opened.
func TestFileSourceRunOpenError(t *testing.T) {
	src := NewFileSource("s", filepath.Join(t.TempDir(), "not-exist.log"), 0)
	if err := src.Run(context.Background(), nil); err == nil {
		t.Fatal("expected open error, got nil")
	}
}

// TestFileSourceRunBatchByItems covers line-count threshold splitting:
// 2*BatchMaxItems+1 lines should split into BatchMaxItems, BatchMaxItems, 1 batches.
func TestFileSourceRunBatchByItems(t *testing.T) {
	total := dag.BatchMaxItems*2 + 1
	path := writeLines(t, total, "x")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []int{dag.BatchMaxItems, dag.BatchMaxItems, 1}
	if len(sizes) != len(want) {
		t.Fatalf("expected %d batches, got %d: %v", len(want), len(sizes), sizes)
	}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("batch %d expected %d lines, got %d (all %v)", i, w, sizes[i], sizes)
		}
	}
}

// TestFileSourceRunBatchByBytes covers byte-threshold splitting:
// each line is 1KB, BatchMaxBytes/1024 lines exactly fill 64KB and trigger a split; the remaining lines form the trailing partial batch.
func TestFileSourceRunBatchByBytes(t *testing.T) {
	per := 1024
	perBatch := dag.BatchMaxBytes / per // 64 lines per batch
	total := perBatch + perBatch/2      // 64 + 32 = 96 lines → 64 + 32 two batches
	path := writeLines(t, total, strings.Repeat("a", per))
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []int{perBatch, total - perBatch}
	if len(sizes) != len(want) {
		t.Fatalf("expected %d batches, got %d: %v", len(want), len(sizes), sizes)
	}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("batch %d expected %d lines, got %d (all %v)", i, w, sizes[i], sizes)
		}
	}
}

// TestFileSourceRunExactFullBatches covers the "exact full batches, no trailing partial batch" scenario:
// when the line count is an exact multiple of BatchMaxItems, the last batch is cut by the threshold and no extra empty batch should appear.
func TestFileSourceRunExactFullBatches(t *testing.T) {
	path := writeLines(t, dag.BatchMaxItems, "y")
	src := NewFileSource("s", path, 0)

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), nil) }()
	sizes := drainOut(t, src.Out())

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(sizes) != 1 || sizes[0] != dag.BatchMaxItems {
		t.Fatalf("expected a single batch of %d lines, got %v", dag.BatchMaxItems, sizes)
	}
}

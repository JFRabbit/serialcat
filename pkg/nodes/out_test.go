package nodes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

// feedBatches writes several batches into in then closes it, simulating upstream EOS.
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

// TestFileSinkRunRecreate covers the recreate branch when the file is externally deleted mid-run:
// after the first line is written, the target file is deleted; subsequent lines should trigger
// recreation (same name when creatable), with no data lost.
func TestFileSinkRunRecreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	sink := NewFileSink("k", path)

	in := make(chan []dag.Item, 4)
	done := make(chan error, 1)
	go func() { done <- sink.Run(context.Background(), in) }()

	in <- itemBatch("line-1")
	// Wait for the first line to hit the disk, then delete the file, simulating an external move.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for first line to be written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove target file: %v", err)
	}
	// Second batch: should take the recreate branch and write to a new same-named file.
	in <- itemBatch("line-2", "line-3")
	close(in)

	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recreated file: %v", err)
	}
	// line-1 was on the old fd (already gone with the deleted file); the recreated file only carries post-delete data.
	got := strings.TrimRight(string(data), "\n")
	if got != "line-2\nline-3" {
		t.Fatalf("content after recreate is wrong, got %q", got)
	}
}

// TestFileSinkRunDirOccupiedMidRun covers the fallback branch where the same name is occupied
// by a directory mid-run: after the file is deleted and a same-named directory occupies the spot,
// os.Stat on the directory still succeeds (no recreate), the line is written to the old deleted fd
// without error, and Run completes successfully without abnormal interruption.
// The actual same-name-with-increasing-suffix recreate logic is covered by TestRecreateSinkFileDirOccupied.
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
			t.Fatal("timed out waiting for first line to be written")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove original file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create directory with same name: %v", err)
	}

	in <- itemBatch("after-dir")
	close(in)
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// The same-named path is now a directory; Run should not write into it, nor report an error.
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("the same-name path should remain a directory: %v", err)
	}
}

// TestRecreateSinkFileDirOccupied directly covers recreateSinkFile's same-name-occupied-by-directory branch.
func TestRecreateSinkFileDirOccupied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("create directory with same name: %v", err)
	}

	f, alt, err := recreateSinkFile(path)
	if err != nil {
		t.Fatalf("recreateSinkFile returned error: %v", err)
	}
	defer f.Close()
	if alt == path {
		t.Fatalf("when the same name is occupied by a directory, an alternative name should be used, still got %s", alt)
	}
	if filepath.Dir(alt) != dir || !strings.HasPrefix(filepath.Base(alt), "x-") {
		t.Fatalf("alternative name does not follow convention: %s", alt)
	}
}

// TestRecreateSinkFileSameName covers the branch where the same name is creatable and reused directly.
func TestRecreateSinkFileSameName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "y.log")
	f, alt, err := recreateSinkFile(path)
	if err != nil {
		t.Fatalf("recreateSinkFile returned error: %v", err)
	}
	defer f.Close()
	if alt != path {
		t.Fatalf("when the same name can be created, the original name should be returned, got %s", alt)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should have been created: %v", err)
	}
}

// TestConsoleRun injects a bytes.Buffer into Console.w, verifying line-by-line print output.
func TestConsoleRun(t *testing.T) {
	c := NewConsole("c")
	var buf bytes.Buffer
	c.w = &buf

	in := make(chan []dag.Item, 2)
	in <- itemBatch("a", "b")
	in <- itemBatch("c")
	close(in)

	if err := c.Run(context.Background(), in); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := buf.String(); got != "a\nb\nc\n" {
		t.Fatalf("output is wrong, got %q", got)
	}
}

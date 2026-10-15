// App-layer tests: Validate valid/invalid config branches, and Run's end-to-end file → grep → file pipeline.
// Uses only the standard library testing; the test file lives in the same directory as the package under test
// and accesses only exported functions, hence the _test external test package.
package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JFRabbit/serialcat/internal/app"
)

// writeCfg writes a YAML config into t.TempDir() and returns its path.
func writeCfg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return p
}

// TestValidate covers the success branch for a valid config and the error branches for invalid configs.
func TestValidate(t *testing.T) {
	valid := `
graph:
  - id: src
    kind: source
    type: file
    path: input.txt
  - id: hit
    kind: filter
    type: grep
    match: "ERROR"
    from: [src]
  - id: out
    kind: sink
    type: file
    path: out.txt
    from: [hit]
`

	// Invalid config: grep's match regex is invalid, nodes.Build fails.
	invalidRegexp := `
graph:
  - id: src
    kind: source
    type: file
    path: input.txt
  - id: hit
    kind: filter
    type: grep
    match: "(["
    from: [src]
  - id: out
    kind: sink
    type: file
    path: out.txt
    from: [hit]
`

	// Invalid config: the topology contains a cycle.
	cycle := `
graph:
  - id: a
    kind: filter
    type: grep
    match: "x"
    from: [b]
  - id: b
    kind: filter
    type: grep
    match: "x"
    from: [a]
`

	tests := []struct {
		name    string
		cfgPath string // empty means use a non-existent path
		content string
		wantErr bool
	}{
		{name: "valid config", content: valid},
		{name: "invalid regexp", content: invalidRegexp, wantErr: true},
		{name: "topology cycle", content: cycle, wantErr: true},
		{name: "config file missing", cfgPath: filepath.Join(t.TempDir(), "missing.yaml"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.cfgPath
			if p == "" {
				p = writeCfg(t, tt.content)
			}
			err := app.Validate(p)
			if tt.wantErr && err == nil {
				t.Fatalf("Validate(%s) expected an error, got nil", p)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate(%s) expected success, got error: %v", p, err)
			}
		})
	}
}

// TestRunEndToEnd runs the file → grep → file pipeline end to end:
// the input file contains both matching and non-matching lines; the sink output must contain only matching lines.
// Run is blocking, and the bounded source:file finishes naturally; an extra timeout ctx prevents hangs.
// t.Chdir isolates the run directory so default/relative sink paths do not pollute the repo.
func TestRunEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	input := strings.Join([]string{
		"boot ok",
		"ERROR: nullptr crash",
		"all fine",
		"ERROR: timeout again",
		"",
	}, "\n")
	if err := os.WriteFile("input.txt", []byte(input), 0o644); err != nil {
		t.Fatalf("failed to write input file: %v", err)
	}

	cfg := `
graph:
  - id: src
    kind: source
    type: file
    path: input.txt
  - id: err
    kind: filter
    type: grep
    match: "ERROR"
    from: [src]
  - id: dump
    kind: sink
    type: file
    path: err_lines.txt
    from: [err]
`
	cfgPath := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.Run(ctx, cfgPath, app.RunOptions{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	got, err := os.ReadFile("err_lines.txt")
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines of matching output, got %d lines: %q", len(lines), string(got))
	}
	for _, ln := range lines {
		if !strings.Contains(ln, "ERROR") {
			t.Fatalf("output line does not contain the match keyword: %q", ln)
		}
	}
}

// TestRunBadConfig covers Run passing through errors when config load/build fails.
func TestRunBadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfgPath := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte("graph: [ not yaml"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Run(ctx, cfgPath, app.RunOptions{}); err == nil {
		t.Fatal("Run expected an error for an invalid config, got nil")
	}
}

package nodes

import (
	"strings"
	"testing"

	"github.com/JFRabbit/serialcat/pkg/config"
)

// buildErr builds a config with the given nodes and executes Build, returning the error (expected to be non-nil).
// The first node must be valid by itself, so Validate passes and the error lands in the construction branch of a later node.
func buildErr(t *testing.T, ncs ...config.NodeConfig) error {
	t.Helper()
	_, err := Build(&config.Config{Graph: ncs})
	return err
}

// grepFromSource returns a valid upstream source:file node for filter/process nodes to reference.
func grepFromSource() config.NodeConfig {
	return config.NodeConfig{
		ID:   "src",
		Kind: "source",
		Type: "file",
		Conf: map[string]any{"path": "x.log"},
	}
}

// TestBuildFileSourceMissingPath covers the error branch of source:file missing path.
func TestBuildFileSourceMissingPath(t *testing.T) {
	nc := config.NodeConfig{ID: "s", Kind: "source", Type: "file", Conf: map[string]any{}}
	err := buildErr(t, nc)
	if err == nil || !strings.Contains(err.Error(), "missing config key path") {
		t.Fatalf("expected missing config key path, got %v", err)
	}
}

// TestBuildGrepMissingMatch covers the error branch of filter:grep missing match.
func TestBuildGrepMissingMatch(t *testing.T) {
	nc := config.NodeConfig{
		ID: "g", Kind: "filter", Type: "grep", From: []string{"src"},
		Conf: map[string]any{},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "missing config key match") {
		t.Fatalf("expected missing config key match, got %v", err)
	}
}

// TestBuildGrepInvalidRegexp covers the compile failure branch of an invalid match regexp.
func TestBuildGrepInvalidRegexp(t *testing.T) {
	nc := config.NodeConfig{
		ID: "g", Kind: "filter", Type: "grep", From: []string{"src"},
		Conf: map[string]any{"match": "["},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "failed to compile match") {
		t.Fatalf("expected match compile failure, got %v", err)
	}
}

// TestBuildProcessArgsWrongType covers the error branch of process:script args being a non-string array.
func TestBuildProcessArgsWrongType(t *testing.T) {
	nc := config.NodeConfig{
		ID: "p", Kind: "process", Type: "script", From: []string{"src"},
		Conf: map[string]any{"command": "echo", "args": "not-a-list"},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "args must be a string array") {
		t.Fatalf("expected args must be a string array, got %v", err)
	}
}

// TestBuildProcessArgsElemWrongType covers the error branch of an args element being non-string.
func TestBuildProcessArgsElemWrongType(t *testing.T) {
	nc := config.NodeConfig{
		ID: "p", Kind: "process", Type: "script", From: []string{"src"},
		Conf: map[string]any{"command": "echo", "args": []any{"ok", 42}},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "each element of config key args must be a string") {
		t.Fatalf("expected each element of config key args must be a string, got %v", err)
	}
}

// TestBuildUnsupportedKindType covers the fallback branch of an unsupported kind:type.
func TestBuildUnsupportedKindType(t *testing.T) {
	nc := config.NodeConfig{ID: "u", Kind: "source", Type: "nosuch", Conf: map[string]any{}}
	err := buildErr(t, nc)
	if err == nil || !strings.Contains(err.Error(), "unsupported node type") {
		t.Fatalf("expected unsupported node type, got %v", err)
	}
}

// TestBuildOK covers the happy path: source:file + filter:grep + sink:file can build the full graph.
func TestBuildOK(t *testing.T) {
	cfg := &config.Config{Graph: []config.NodeConfig{
		{ID: "s", Kind: "source", Type: "file", Conf: map[string]any{"path": "x.log"}},
		{ID: "g", Kind: "filter", Type: "grep", From: []string{"s"}, Conf: map[string]any{"match": "err"}},
		{ID: "k", Kind: "sink", Type: "console", From: []string{"g"}},
	}}
	g, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if g == nil {
		t.Fatal("graph should not be nil")
	}
}

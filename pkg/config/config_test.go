package config_test

import (
	"strings"
	"testing"

	"github.com/JFRabbit/serialcat/pkg/config"
)

// node is a shorthand for building test cases.
func node(id, kind, typ string, from ...string) config.NodeConfig {
	return config.NodeConfig{ID: id, Kind: kind, Type: typ, From: from}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		graph   []config.NodeConfig
		wantErr string // expected error substring; empty means it should pass
	}{
		{
			name: "valid single chain source-filter-sink",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "sink", "console", "b"),
			},
		},
		{
			name: "valid DAG: source fans out to multiple downstreams",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "sink", "file", "a"),
				node("d", "sink", "console", "b"),
			},
		},
		{
			name:  "empty graph (no nodes) is valid",
			graph: []config.NodeConfig{},
		},
		{
			name: "empty id errors",
			graph: []config.NodeConfig{
				node("", "source", "file"),
			},
			wantErr: "id must not be empty",
		},
		{
			name: "duplicate id errors",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("a", "filter", "grep", "a"),
			},
			wantErr: "duplicate node id",
		},
		{
			name: "missing type errors",
			graph: []config.NodeConfig{
				node("a", "source", ""),
			},
			wantErr: "missing type",
		},
		{
			name: "invalid kind errors",
			graph: []config.NodeConfig{
				node("a", "transformer", "file"),
			},
			wantErr: "invalid kind",
		},
		{
			name: "empty kind treated as invalid",
			graph: []config.NodeConfig{
				node("a", "", "file"),
			},
			wantErr: "invalid kind",
		},
		{
			name: "source must not have from errors",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "source", "file", "a"),
			},
			wantErr: "must not have from",
		},
		{
			name: "non-source missing from errors",
			graph: []config.NodeConfig{
				node("a", "sink", "console"),
			},
			wantErr: "has no from",
		},
		{
			name: "referencing nonexistent upstream errors",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "ghost"),
			},
			wantErr: "references nonexistent upstream",
		},
		{
			name: "sink referenced as upstream errors",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "sink", "console", "a"),
				node("c", "filter", "grep", "b"),
			},
			wantErr: "cannot be used as upstream",
		},
		{
			name: "two-node self-cycle errors",
			graph: []config.NodeConfig{
				node("a", "filter", "grep", "b"),
				node("b", "filter", "grep", "a"),
			},
			wantErr: "config forms a cycle",
		},
		{
			name: "long cycle errors (source outside the cycle)",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "filter", "grep", "b"),
				node("d", "filter", "grep", "c", "e"),
				node("e", "process", "script", "d"),
				node("f", "sink", "console", "c"),
			},
			wantErr: "config forms a cycle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Graph: tt.graph}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected to pass, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, but passed", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

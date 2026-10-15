package config_test

import (
	"strings"
	"testing"

	"github.com/JFRabbit/serialcat/pkg/config"
)

// node 是构造测试用例的简写。
func node(id, kind, typ string, from ...string) config.NodeConfig {
	return config.NodeConfig{ID: id, Kind: kind, Type: typ, From: from}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		graph   []config.NodeConfig
		wantErr string // 期望的错误子串；为空表示应通过
	}{
		{
			name: "合法单链 source-filter-sink",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "sink", "console", "b"),
			},
		},
		{
			name: "合法 DAG：source 扇出到多个下游",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "sink", "file", "a"),
				node("d", "sink", "console", "b"),
			},
		},
		{
			name:  "空图（无节点）合法",
			graph: []config.NodeConfig{},
		},
		{
			name: "空 id 报错",
			graph: []config.NodeConfig{
				node("", "source", "file"),
			},
			wantErr: "id 不能为空",
		},
		{
			name: "重复 id 报错",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("a", "filter", "grep", "a"),
			},
			wantErr: "重复节点 id",
		},
		{
			name: "缺少 type 报错",
			graph: []config.NodeConfig{
				node("a", "source", ""),
			},
			wantErr: "缺少 type",
		},
		{
			name: "非法 kind 报错",
			graph: []config.NodeConfig{
				node("a", "transformer", "file"),
			},
			wantErr: "kind 非法",
		},
		{
			name: "空 kind 视为非法",
			graph: []config.NodeConfig{
				node("a", "", "file"),
			},
			wantErr: "kind 非法",
		},
		{
			name: "source 不应有 from 报错",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "source", "file", "a"),
			},
			wantErr: "不应有 from",
		},
		{
			name: "非 source 缺 from 报错",
			graph: []config.NodeConfig{
				node("a", "sink", "console"),
			},
			wantErr: "没有 from",
		},
		{
			name: "引用不存在的上游报错",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "ghost"),
			},
			wantErr: "引用不存在的上游",
		},
		{
			name: "sink 作为上游被引用报错",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "sink", "console", "a"),
				node("c", "filter", "grep", "b"),
			},
			wantErr: "不能作为上游",
		},
		{
			name: "两节点自环报错",
			graph: []config.NodeConfig{
				node("a", "filter", "grep", "b"),
				node("b", "filter", "grep", "a"),
			},
			wantErr: "配置成环",
		},
		{
			name: "长环报错（source 在环外）",
			graph: []config.NodeConfig{
				node("a", "source", "file"),
				node("b", "filter", "grep", "a"),
				node("c", "filter", "grep", "b"),
				node("d", "filter", "grep", "c", "e"),
				node("e", "process", "script", "d"),
				node("f", "sink", "console", "c"),
			},
			wantErr: "配置成环",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Graph: tt.graph}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错包含 %q，实际通过", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tt.wantErr, err)
			}
		})
	}
}

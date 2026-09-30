package nodes

import (
	"strings"
	"testing"

	"logdiag/pkg/config"
)

// buildErr 构造含给定节点的配置并执行 Build，返回错误（预期非 nil）。
// 第一个节点需自身合法，使 Validate 通过、错误落在后续节点的构造分支。
func buildErr(t *testing.T, ncs ...config.NodeConfig) error {
	t.Helper()
	_, err := Build(&config.Config{Graph: ncs})
	return err
}

// grepFromSource 给出一个合法的上游 source:file 节点，供 filter/process 节点引用。
func grepFromSource() config.NodeConfig {
	return config.NodeConfig{
		ID:   "src",
		Kind: "source",
		Type: "file",
		Conf: map[string]any{"path": "x.log"},
	}
}

// TestBuildFileSourceMissingPath 覆盖 source:file 缺 path 的错误分支。
func TestBuildFileSourceMissingPath(t *testing.T) {
	nc := config.NodeConfig{ID: "s", Kind: "source", Type: "file", Conf: map[string]any{}}
	err := buildErr(t, nc)
	if err == nil || !strings.Contains(err.Error(), "缺少配置项 path") {
		t.Fatalf("期望 缺少配置项 path，得到 %v", err)
	}
}

// TestBuildGrepMissingMatch 覆盖 filter:grep 缺 match 的错误分支。
func TestBuildGrepMissingMatch(t *testing.T) {
	nc := config.NodeConfig{
		ID: "g", Kind: "filter", Type: "grep", From: []string{"src"},
		Conf: map[string]any{},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "缺少配置项 match") {
		t.Fatalf("期望 缺少配置项 match，得到 %v", err)
	}
}

// TestBuildGrepInvalidRegexp 覆盖 match 非法正则的编译失败分支。
func TestBuildGrepInvalidRegexp(t *testing.T) {
	nc := config.NodeConfig{
		ID: "g", Kind: "filter", Type: "grep", From: []string{"src"},
		Conf: map[string]any{"match": "["},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "match 编译失败") {
		t.Fatalf("期望 match 编译失败，得到 %v", err)
	}
}

// TestBuildProcessArgsWrongType 覆盖 process:script 的 args 非字符串数组的错误分支。
func TestBuildProcessArgsWrongType(t *testing.T) {
	nc := config.NodeConfig{
		ID: "p", Kind: "process", Type: "script", From: []string{"src"},
		Conf: map[string]any{"command": "echo", "args": "not-a-list"},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "args 应为字符串数组") {
		t.Fatalf("期望 args 应为字符串数组，得到 %v", err)
	}
}

// TestBuildProcessArgsElemWrongType 覆盖 args 元素非字符串的错误分支。
func TestBuildProcessArgsElemWrongType(t *testing.T) {
	nc := config.NodeConfig{
		ID: "p", Kind: "process", Type: "script", From: []string{"src"},
		Conf: map[string]any{"command": "echo", "args": []any{"ok", 42}},
	}
	err := buildErr(t, grepFromSource(), nc)
	if err == nil || !strings.Contains(err.Error(), "每个元素应为字符串") {
		t.Fatalf("期望 每个元素应为字符串，得到 %v", err)
	}
}

// TestBuildUnsupportedKindType 覆盖不支持的 kind:type 默认分支。
func TestBuildUnsupportedKindType(t *testing.T) {
	nc := config.NodeConfig{ID: "u", Kind: "source", Type: "nosuch", Conf: map[string]any{}}
	err := buildErr(t, nc)
	if err == nil || !strings.Contains(err.Error(), "不支持的节点类型") {
		t.Fatalf("期望 不支持的节点类型，得到 %v", err)
	}
}

// TestBuildOK 覆盖 happy path：source:file + filter:grep + sink:file 能完整建图。
func TestBuildOK(t *testing.T) {
	cfg := &config.Config{Graph: []config.NodeConfig{
		{ID: "s", Kind: "source", Type: "file", Conf: map[string]any{"path": "x.log"}},
		{ID: "g", Kind: "filter", Type: "grep", From: []string{"s"}, Conf: map[string]any{"match": "err"}},
		{ID: "k", Kind: "sink", Type: "console", From: []string{"g"}},
	}}
	g, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build 返回错误: %v", err)
	}
	if g == nil {
		t.Fatal("图不应为 nil")
	}
}

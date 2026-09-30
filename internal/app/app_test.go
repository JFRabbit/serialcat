// app 层测试：Validate 的合法/非法配置分支，Run 的端到端 file → grep → file 流水。
// 只用标准库 testing；测试文件与被测包同目录，仅访问导出函数，故用 _test 外部测试包。
package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logdiag/internal/app"
)

// writeCfg 把 YAML 配置写入 t.TempDir() 并返回路径。
func writeCfg(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	return p
}

// TestValidate 覆盖合法配置成功与非法配置报错的分支。
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

	// 非法配置：grep 的 match 正则不合法，nodes.Build 阶段报错。
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

	// 非法配置：拓扑成环。
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
		cfgPath string // 为空表示使用不存在的路径
		content string
		wantErr bool
	}{
		{name: "合法配置", content: valid},
		{name: "非法正则", content: invalidRegexp, wantErr: true},
		{name: "拓扑成环", content: cycle, wantErr: true},
		{name: "配置文件不存在", cfgPath: filepath.Join(t.TempDir(), "missing.yaml"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.cfgPath
			if p == "" {
				p = writeCfg(t, tt.content)
			}
			err := app.Validate(p)
			if tt.wantErr && err == nil {
				t.Fatalf("Validate(%s) 期望报错，实际为 nil", p)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate(%s) 期望成功，实际报错: %v", p, err)
			}
		})
	}
}

// TestRunEndToEnd 端到端跑 file → grep → file：
// 输入文件里既有匹配行也有不匹配行，断言 sink 落盘内容只含匹配行。
// Run 是阻塞的，source:file 有界会自然结束；另加超时 ctx 兜底防挂死。
// t.Chdir 隔离运行目录，避免 sink 默认路径 / 相对路径污染仓库。
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
		t.Fatalf("写输入文件失败: %v", err)
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
		t.Fatalf("写配置失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.Run(ctx, cfgPath, app.RunOptions{}); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	got, err := os.ReadFile("err_lines.txt")
	if err != nil {
		t.Fatalf("读输出文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行匹配输出，实际 %d 行: %q", len(lines), string(got))
	}
	for _, ln := range lines {
		if !strings.Contains(ln, "ERROR") {
			t.Fatalf("输出行不含匹配关键字: %q", ln)
		}
	}
}

// TestRunBadConfig 覆盖 Run 在配置加载/构建失败时透传报错的分支。
func TestRunBadConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfgPath := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte("graph: [ not yaml"), 0o644); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Run(ctx, cfgPath, app.RunOptions{}); err == nil {
		t.Fatal("Run 对非法配置期望报错，实际为 nil")
	}
}

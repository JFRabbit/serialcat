package updater

import (
	"crypto/tls"
	"errors"
	"fmt"
	"testing"
)

// TestCompareVersions 表驱动覆盖版本号比较：相等/大于/小于、v 前缀、非法版本串。
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		want    int
		wantErr bool
	}{
		// 相等
		{name: "完全相等", a: "1.2.3", b: "1.2.3", want: 0},
		{name: "带v前缀相等", a: "v1.2.3", b: "1.2.3", want: 0},
		{name: "双侧v前缀相等", a: "v2.0.0", b: "2.0.0", want: 0},
		// 大于
		{name: "主版本大于", a: "2.0.0", b: "1.9.9", want: 1},
		{name: "次版本大于", a: "1.3.0", b: "1.2.9", want: 1},
		{name: "修订版本大于", a: "1.2.4", b: "1.2.3", want: 1},
		{name: "带v前缀大于", a: "v1.2.3", b: "1.2.2", want: 1},
		// 小于
		{name: "主版本小于", a: "1.0.0", b: "2.0.0", want: -1},
		{name: "次版本小于", a: "1.2.9", b: "1.3.0", want: -1},
		{name: "修订版本小于", a: "1.2.3", b: "1.2.4", want: -1},
		{name: "带v前缀小于", a: "1.2.3", b: "v1.2.4", want: -1},
		// 段数不一致（短版本缺失段按 0 处理）
		{name: "段数少补零相等", a: "1.2", b: "1.2.0", want: 0},
		{name: "段数少补零小于", a: "1.2", b: "1.2.1", want: -1},
		// 非法版本串
		{name: "a非法", a: "abc", b: "1.2.3", wantErr: true},
		{name: "b非法", a: "1.2.3", b: "1.x.3", wantErr: true},
		{name: "a为空", a: "", b: "1.2.3", wantErr: true},
		{name: "仅v前缀", a: "v", b: "1.2.3", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareVersions(tt.a, tt.b)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CompareVersions(%q, %q) 期望报错, 实际 got=%d", tt.a, tt.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompareVersions(%q, %q) 意外报错: %v", tt.a, tt.b, err)
			}
			if got != tt.want {
				t.Errorf("CompareVersions(%q, %q) = %d, 期望 %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestParseVersion 表驱动覆盖私有函数 parseVersion：正常解析、v 前缀、空白、非法输入。
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{name: "三段版本", input: "1.2.3", want: []int{1, 2, 3}},
		{name: "带v前缀", input: "v10.20.30", want: []int{10, 20, 30}},
		{name: "带空白与v前缀", input: "  v2.1 ", want: []int{2, 1}},
		{name: "单段", input: "3", want: []int{3}},
		{name: "空串", input: "", wantErr: true},
		{name: "仅空白", input: "   ", wantErr: true},
		{name: "仅v前缀", input: "v", wantErr: true},
		{name: "含非数字段", input: "1.a.3", wantErr: true},
		{name: "负号段按Atoi可解析", input: "1.-2.3", want: []int{1, -2, 3}},
		{name: "v前有空格仍非法", input: " x.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersion(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseVersion(%q) 期望报错, 实际 got=%v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVersion(%q) 意外报错: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseVersion(%q) = %v, 期望 %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseVersion(%q)[%d] = %d, 期望 %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestResolveURL 表驱动覆盖：绝对 URL 原样返回、相对路径拼接、baseURL 尾部斜杠与 path 头部斜杠的归一化。
func TestResolveURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{name: "http绝对URL原样返回", baseURL: "http://a/bucket", path: "http://cdn/x.exe", want: "http://cdn/x.exe"},
		{name: "https绝对URL原样返回", baseURL: "http://a/bucket", path: "https://cdn/x.exe", want: "https://cdn/x.exe"},
		{name: "相对路径拼接", baseURL: "http://a/bucket", path: "dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "baseURL尾部斜杠归一化", baseURL: "http://a/bucket/", path: "dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "path头部斜杠归一化", baseURL: "http://a/bucket", path: "/dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "两侧斜杠同时归一化", baseURL: "http://a/bucket/", path: "/dist/x.exe", want: "http://a/bucket/dist/x.exe"},
		{name: "嵌套相对路径", baseURL: "http://a:9000/b", path: "app/v1/serialcat.exe", want: "http://a:9000/b/app/v1/serialcat.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveURL(tt.baseURL, tt.path); got != tt.want {
				t.Errorf("ResolveURL(%q, %q) = %q, 期望 %q", tt.baseURL, tt.path, got, tt.want)
			}
		})
	}
}

// TestIsTLSCertError 表驱动覆盖：nil、证书校验错误类型、字符串匹配与不匹配分支。
func TestIsTLSCertError(t *testing.T) {
	// 构造 tls.CertificateVerificationError，模拟证书校验失败错误
	certErr := &tls.CertificateVerificationError{Err: errors.New("x509: certificate signed by unknown authority")}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil错误", err: nil, want: false},
		{name: "证书校验错误类型", err: certErr, want: true},
		{name: "包装后的证书校验错误", err: fmt.Errorf("请求版本信息失败: %w", certErr), want: true},
		{name: "字符串匹配certificate和authority", err: errors.New("Get https://x: x509: certificate signed by unknown authority"), want: true},
		{name: "字符串仅含certificate", err: errors.New("certificate error happened"), want: false},
		{name: "字符串仅含authority", err: errors.New("unknown authority"), want: false},
		{name: "字符串均不含", err: errors.New("connection refused"), want: false},
		{name: "大写字符串匹配（小写归一化）", err: errors.New("Certificate signed by unknown Authority"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTLSCertError(tt.err); got != tt.want {
				t.Errorf("IsTLSCertError(%v) = %v, 期望 %v", tt.err, got, tt.want)
			}
		})
	}
}

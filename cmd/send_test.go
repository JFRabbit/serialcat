package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// escapeCmd 覆盖：字面 \r \n \t 的替换、顺序、无转义时的原样返回。
func TestEscapeCmd(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"无转义原样返回", "AT+OPEN", "AT+OPEN"},
		{"单个回车", `AT\r`, "AT\r"},
		{"单个换行", `AT\n`, "AT\n"},
		{"单个制表符", `a\tb`, "a\tb"},
		{"组合回车换行", `AT+OPEN\r\n`, "AT+OPEN\r\n"},
		{"多组混合", `\r\n\t\r\n`, "\r\n\t\r\n"},
		{"已是真实控制字符不再误伤", "AT\r\n", "AT\r\n"},
		{"夹在中间", "ls\r-al", "ls\r-al"},
		{"未知转义不处理", `a\rb`, "a\rb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeCmd(tc.in); got != tc.want {
				t.Errorf("escapeCmd(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// remoteWriter.Send 成功路径：2xx(200) 返回 nil，表单 body 携带 data 字段。
func TestRemoteWriterSendSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/write" {
			t.Errorf("请求路径 = %q, want /api/write", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("请求方法 = %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取请求体: %v", err)
		}
		if !strings.Contains(string(body), "data="+urlQueryEscape("AT\r\n")) {
			t.Errorf("请求体 = %q, 缺少 data 字段", string(body))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	if err := w.Send(context.Background(), []byte("AT\r\n")); err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}
}

// urlQueryEscape 仅测试内辅助：与 url.Values.Encode 中 + 号编码保持一致。
func urlQueryEscape(s string) string {
	r := strings.NewReplacer(" ", "+", "\r", "%0D", "\n", "%0A")
	return r.Replace(s)
}

// remoteWriter.Send 非 2xx：agent 返回 500 时应报错且错误信息含状态文本。
func TestRemoteWriterSendNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	err := w.Send(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("期望非 nil 错误,实际为 nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误信息 %q 未包含状态码 500", err)
	}
}

// remoteWriter.Send 携带 token：设置 token 时请求头应有 Bearer;为空时则不应有 Authorization 头。
func TestRemoteWriterSendToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := &remoteWriter{base: srv.URL, token: "secret-token", cli: srv.Client()}
	if err := w.Send(context.Background(), []byte("x")); err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
	}

	// 不带 token:不应出现 Authorization 头
	gotAuth = "未设置"
	w2 := &remoteWriter{base: srv.URL, cli: srv.Client()}
	if err := w2.Send(context.Background(), []byte("x")); err != nil {
		t.Fatalf("Send 返回错误: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("未设 token 时 Authorization = %q, want 空", gotAuth)
	}
}

// remoteWriter.Send 网络不可达：客户端 Do 失败时应包装错误返回。
func TestRemoteWriterSendRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 立即关闭,让请求必然失败

	w := &remoteWriter{base: srv.URL, cli: srv.Client()}
	err := w.Send(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("期望非 nil 错误,实际为 nil")
	}
	if !strings.Contains(err.Error(), "发送请求") {
		t.Errorf("错误信息 %q 未包含包装前缀", err)
	}
}

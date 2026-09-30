package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"logdiag/pkg/serial"
)

// capturePort：记录下行写入内容的内存端口。
type capturePort struct {
	mu    sync.Mutex
	wrote []byte
	done  chan struct{}
	once  sync.Once
}

func (p *capturePort) Read(b []byte) (int, error) {
	<-p.done
	return 0, errors.New("closed")
}

func (p *capturePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.wrote = append(p.wrote, b...)
	p.mu.Unlock()
	return len(b), nil
}

func (p *capturePort) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *capturePort) written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.wrote)
}

// captureDriver：始终返回 capturePort，端口名不做限制。
type captureDriver struct {
	port *capturePort
}

func (d captureDriver) ListPorts() ([]string, error) { return []string{"COM1"}, nil }

func (d captureDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	return d.port, nil
}

var _ serial.Driver = captureDriver{}

// 构造测试 Server：fake driver + 内存 SerialSource。
func newTestServer(t *testing.T, opts Options) (*Server, *capturePort) {
	t.Helper()
	port := &capturePort{done: make(chan struct{})}
	srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, opts)
	if err != nil {
		t.Fatalf("NewServer 返回错误: %v", err)
	}
	return srv, port
}

// /health 返回 ok。
func TestHandleHealth(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("状态码 = %d, 期望 200", rec.Code)
	}
	if rec.Body.String() != "ok\n" {
		t.Errorf("响应体 = %q, 期望 %q", rec.Body.String(), "ok\n")
	}
}

// NewServer 打开串口失败时应返回包装错误。
func TestNewServerOpenError(t *testing.T) {
	// fakeDriver 仅在 115200 可打开（见 probe_test.go）
	_, err := NewServer("127.0.0.1:0", "COM1", 9600, fakeDriver{}, Options{})
	if err == nil {
		t.Fatal("打开失败时 NewServer 应返回错误")
	}
	if !strings.Contains(err.Error(), "打开串口") {
		t.Errorf("错误 %v 应包含包装前缀", err)
	}
}

// /api/ports 枚举端口。
func TestHandlePorts(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	srv.handlePorts(rec, httptest.NewRequest(http.MethodGet, "/api/ports", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rec.Code)
	}
	var ports []string
	if err := json.Unmarshal(rec.Body.Bytes(), &ports); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if len(ports) != 1 || ports[0] != "COM1" {
		t.Errorf("端口列表 = %v, 期望 [COM1]", ports)
	}
}

// /api/write 未开启写开关时返回 403。
func TestHandleWriteDisabled(t *testing.T) {
	srv, port := newTestServer(t, Options{WriteEnabled: false})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/write?data=hi", nil)
	srv.handleWrite(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d, 期望 403", rec.Code)
	}
	if got := port.written(); got != "" {
		t.Errorf("写接口关闭时不应写串口, 实际写入 %q", got)
	}
}

// /api/write 开启写开关且设置 token：缺少/错误 Authorization 返回 401，正确 token 放行。
func TestHandleWriteToken(t *testing.T) {
	newReq := func(auth string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/write?data=hello", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return req
	}

	t.Run("缺少令牌返回 401", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq(""))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("状态码 = %d, 期望 401", rec.Code)
		}
		if got := port.written(); got != "" {
			t.Errorf("未授权不应写串口, 实际写入 %q", got)
		}
	})

	t.Run("错误令牌返回 401", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq("Bearer wrong"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("状态码 = %d, 期望 401", rec.Code)
		}
		if got := port.written(); got != "" {
			t.Errorf("未授权不应写串口, 实际写入 %q", got)
		}
	})

	t.Run("正确令牌放行并写串口", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq("Bearer secret"))
		if rec.Code != http.StatusOK {
			t.Errorf("状态码 = %d, 期望 200", rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		if body["ok"] != "sent" {
			t.Errorf("响应体 = %v, 期望 ok=sent", body)
		}
		if got := port.written(); got != "hello" {
			t.Errorf("串口写入 = %q, 期望 %q", got, "hello")
		}
	})
}

// /api/write 开启且无 token 时直接放行；query 参数中的 \r \n \t 字面转义被还原为控制字符。
func TestHandleWriteEscapeAndPostForm(t *testing.T) {
	t.Run("字面转义还原", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, `/api/write?data=AT\r\n\tZ`, nil)
		srv.handleWrite(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200", rec.Code)
		}
		if got, want := port.written(), "AT\r\n\tZ"; got != want {
			t.Errorf("串口写入 = %q, 期望 %q", got, want)
		}
	})

	t.Run("POST form 取 data", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true})
		rec := httptest.NewRecorder()
		body := strings.NewReader("data=reboot\r\n")
		req := httptest.NewRequest(http.MethodPost, "/api/write", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.handleWrite(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200", rec.Code)
		}
		if got, want := port.written(), "reboot\r\n"; got != want {
			t.Errorf("串口写入 = %q, 期望 %q", got, want)
		}
	})

	t.Run("写串口失败返回 500", func(t *testing.T) {
		srv, _ := newTestServer(t, Options{WriteEnabled: true})
		// 先关闭底层端口，让 capturePort.Read 返回错误；Write 本身不报错，
		// 因此改用错误端口驱动验证 500 分支。
		bad, err := NewServer("127.0.0.1:0", "COM1", 115200, errDriver{}, Options{WriteEnabled: true})
		if err != nil {
			t.Fatalf("NewServer 返回错误: %v", err)
		}
		_ = srv
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/write?data=x", nil)
		bad.handleWrite(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("状态码 = %d, 期望 500", rec.Code)
		}
	})
}

// errDriver：Open 返回写即失败的端口。
type errDriver struct{}

func (errDriver) ListPorts() ([]string, error) { return nil, nil }

func (errDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	return errPort{}, nil
}

type errPort struct{}

func (errPort) Read([]byte) (int, error)    { return 0, io.EOF }
func (errPort) Close() error                { return nil }
func (errPort) Write(b []byte) (int, error) { return 0, errors.New("write failed") }

// /api/probe 缺少 port 返回 400；带 port 返回探测结果。
func TestHandleProbe(t *testing.T) {
	t.Run("缺少 port 返回 400", func(t *testing.T) {
		srv, _ := newTestServer(t, Options{})
		rec := httptest.NewRecorder()
		srv.handleProbe(rec, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("状态码 = %d, 期望 400", rec.Code)
		}
	})

	t.Run("正常探测返回 JSON", func(t *testing.T) {
		port := &capturePort{done: make(chan struct{})}
		srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, Options{})
		if err != nil {
			t.Fatalf("NewServer 返回错误: %v", err)
		}
		rec := httptest.NewRecorder()
		srv.handleProbe(rec, httptest.NewRequest(http.MethodGet, "/api/probe?port=COM1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200", rec.Code)
		}
		var results []ProbeResult
		if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
			t.Fatalf("响应不是 JSON: %v", err)
		}
		if len(results) != len(probeBaunds) {
			t.Errorf("结果数 = %d, 期望 %d", len(results), len(probeBaunds))
		}
	})
}

// handleStream 经由 httptest.Server 端到端验证 NDJSON 流式输出。
func TestHandleStreamEndToEnd(t *testing.T) {
	port := &capturePort{done: make(chan struct{})}
	srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, Options{})
	if err != nil {
		t.Fatalf("NewServer 返回错误: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stream", srv.handleStream)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, 期望 application/x-ndjson", ct)
	}

	// 等待 handler 内完成订阅后再广播，避免帧被丢弃导致 Decode 阻塞
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.src.mu.Lock()
		n := len(srv.src.subs)
		srv.src.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待流订阅超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.src.broadcast(Frame{Ts: time.Now(), Text: "hello stream"})

	dec := json.NewDecoder(resp.Body)
	var f Frame
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("解码流帧失败: %v", err)
	}
	if f.Text != "hello stream" {
		t.Errorf("帧文本 = %q, 期望 %q", f.Text, "hello stream")
	}
	if f.Ts.IsZero() {
		t.Error("帧时间戳为零")
	}
}

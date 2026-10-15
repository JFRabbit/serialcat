package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JFRabbit/serialcat/pkg/serial"
)

// capturePort: an in-memory port that records downstream writes.
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

// captureDriver: always returns capturePort; the port name is unrestricted.
type captureDriver struct {
	port *capturePort
}

func (d captureDriver) ListPorts() ([]string, error) { return []string{"COM1"}, nil }

func (d captureDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	return d.port, nil
}

var _ serial.Driver = captureDriver{}

// Builds a test Server: fake driver + in-memory SerialSource.
func newTestServer(t *testing.T, opts Options) (*Server, *capturePort) {
	t.Helper()
	port := &capturePort{done: make(chan struct{})}
	srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, opts)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return srv, port
}

// /health returns ok.
func TestHandleHealth(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok\n" {
		t.Errorf("response body = %q, want %q", rec.Body.String(), "ok\n")
	}
}

// NewServer should return a wrapped error when opening the serial port fails.
func TestNewServerOpenError(t *testing.T) {
	// fakeDriver only opens at 115200 (see probe_test.go)
	_, err := NewServer("127.0.0.1:0", "COM1", 9600, fakeDriver{}, Options{})
	if err == nil {
		t.Fatal("NewServer should return an error when opening fails")
	}
	if !strings.Contains(err.Error(), "open serial port") {
		t.Errorf("error %v should contain the wrap prefix", err)
	}
}

// /api/ports enumerates ports.
func TestHandlePorts(t *testing.T) {
	srv, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	srv.handlePorts(rec, httptest.NewRequest(http.MethodGet, "/api/ports", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var ports []string
	if err := json.Unmarshal(rec.Body.Bytes(), &ports); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(ports) != 1 || ports[0] != "COM1" {
		t.Errorf("port list = %v, want [COM1]", ports)
	}
}

// /api/write returns 403 when the write switch is off.
func TestHandleWriteDisabled(t *testing.T) {
	srv, port := newTestServer(t, Options{WriteEnabled: false})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/write?data=hi", nil)
	srv.handleWrite(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status code = %d, want 403", rec.Code)
	}
	if got := port.written(); got != "" {
		t.Errorf("should not write to the serial port when the write endpoint is disabled, actually wrote %q", got)
	}
}

// /api/write with the write switch on and a token set: missing/wrong Authorization returns 401, correct token passes.
func TestHandleWriteToken(t *testing.T) {
	newReq := func(auth string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/write?data=hello", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return req
	}

	t.Run("missing token returns 401", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq(""))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status code = %d, want 401", rec.Code)
		}
		if got := port.written(); got != "" {
			t.Errorf("unauthorized should not write to the serial port, actually wrote %q", got)
		}
	})

	t.Run("wrong token returns 401", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq("Bearer wrong"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status code = %d, want 401", rec.Code)
		}
		if got := port.written(); got != "" {
			t.Errorf("unauthorized should not write to the serial port, actually wrote %q", got)
		}
	})

	t.Run("correct token passes and writes to the serial port", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true, WriteToken: "secret"})
		rec := httptest.NewRecorder()
		srv.handleWrite(rec, newReq("Bearer secret"))
		if rec.Code != http.StatusOK {
			t.Errorf("status code = %d, want 200", rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if body["ok"] != "sent" {
			t.Errorf("response body = %v, want ok=sent", body)
		}
		if got := port.written(); got != "hello" {
			t.Errorf("serial write = %q, want %q", got, "hello")
		}
	})
}

// /api/write with the write switch on and no token passes directly; literal \r \n \t escapes in query params are restored to control characters.
func TestHandleWriteEscapeAndPostForm(t *testing.T) {
	t.Run("literal escapes restored", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, `/api/write?data=AT\r\n\tZ`, nil)
		srv.handleWrite(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want 200", rec.Code)
		}
		if got, want := port.written(), "AT\r\n\tZ"; got != want {
			t.Errorf("serial write = %q, want %q", got, want)
		}
	})

	t.Run("POST form provides data", func(t *testing.T) {
		srv, port := newTestServer(t, Options{WriteEnabled: true})
		rec := httptest.NewRecorder()
		body := strings.NewReader("data=reboot\r\n")
		req := httptest.NewRequest(http.MethodPost, "/api/write", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.handleWrite(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want 200", rec.Code)
		}
		if got, want := port.written(), "reboot\r\n"; got != want {
			t.Errorf("serial write = %q, want %q", got, want)
		}
	})

	t.Run("write failure returns 500", func(t *testing.T) {
		srv, _ := newTestServer(t, Options{WriteEnabled: true})
		// Closing the underlying port makes capturePort.Read return an error; Write itself never fails,
		// so a failing port driver is used to exercise the 500 branch.
		bad, err := NewServer("127.0.0.1:0", "COM1", 115200, errDriver{}, Options{WriteEnabled: true})
		if err != nil {
			t.Fatalf("NewServer returned error: %v", err)
		}
		_ = srv
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/write?data=x", nil)
		bad.handleWrite(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status code = %d, want 500", rec.Code)
		}
	})
}

// errDriver: Open returns a port whose writes fail.
type errDriver struct{}

func (errDriver) ListPorts() ([]string, error) { return nil, nil }

func (errDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	return errPort{}, nil
}

type errPort struct{}

func (errPort) Read([]byte) (int, error)    { return 0, io.EOF }
func (errPort) Close() error                { return nil }
func (errPort) Write(b []byte) (int, error) { return 0, errors.New("write failed") }

// /api/probe returns 400 when port is missing; returns probe results with a port.
func TestHandleProbe(t *testing.T) {
	t.Run("missing port returns 400", func(t *testing.T) {
		srv, _ := newTestServer(t, Options{})
		rec := httptest.NewRecorder()
		srv.handleProbe(rec, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status code = %d, want 400", rec.Code)
		}
	})

	t.Run("normal probe returns JSON", func(t *testing.T) {
		port := &capturePort{done: make(chan struct{})}
		srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, Options{})
		if err != nil {
			t.Fatalf("NewServer returned error: %v", err)
		}
		rec := httptest.NewRecorder()
		srv.handleProbe(rec, httptest.NewRequest(http.MethodGet, "/api/probe?port=COM1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want 200", rec.Code)
		}
		var results []ProbeResult
		if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if len(results) != len(probeBaunds) {
			t.Errorf("result count = %d, want %d", len(results), len(probeBaunds))
		}
	})
}

// handleStream end-to-end NDJSON streaming via httptest.Server.
func TestHandleStreamEndToEnd(t *testing.T) {
	port := &capturePort{done: make(chan struct{})}
	srv, err := NewServer("127.0.0.1:0", "COM1", 115200, captureDriver{port: port}, Options{})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stream", srv.handleStream)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	// Wait for the handler to complete subscription before broadcasting, so frames are not dropped and Decode does not block.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.src.mu.Lock()
		n := len(srv.src.subs)
		srv.src.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for stream subscription")
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.src.broadcast(Frame{Ts: time.Now(), Text: "hello stream"})

	dec := json.NewDecoder(resp.Body)
	var f Frame
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("failed to decode stream frame: %v", err)
	}
	if f.Text != "hello stream" {
		t.Errorf("frame text = %q, want %q", f.Text, "hello stream")
	}
	if f.Ts.IsZero() {
		t.Error("frame timestamp is zero")
	}
}

// The following are tests for /api/logs history reads.

// logsTestBase is the base time of the test log; timestamps are generated by incrementing step.
const logsTestBase = "2026-10-08 10:00:00.000"

// writeLogFile writes n lines of log-format rows (time from base incremented by step, text=fmt.Sprintf(textFmt,i)).
func writeLogFile(t *testing.T, path string, n int, base time.Time, step time.Duration, textFmt string) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * step)
		b.WriteString(ts.Format(logTimeLayout))
		b.WriteByte(' ')
		b.WriteString(fmt.Sprintf(textFmt, i))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}
}

// newLogsSrv builds a Server with a pre-populated log file (10 rows, text=line-%02d, step 1s) for /api/logs tests.
func newLogsSrv(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServer(t, Options{})
	base, err := time.ParseInLocation(logTimeLayout, logsTestBase, time.Local)
	if err != nil {
		t.Fatalf("failed to parse base time: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test-serial.log")
	writeLogFile(t, path, 10, base, time.Second, "line-%02d")
	srv.logPath = path
	return srv
}

// callLogs calls handleLogs; params are built with qv for proper URL encoding.
func callLogs(srv *Server, params url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?"+params.Encode(), nil)
	srv.handleLogs(rec, req)
	return rec
}

// qv is a convenience helper for url.Values: pass key,value pairs.
func qv(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

// decodeLogs parses a successful /api/logs response.
func decodeLogs(t *testing.T, rec *httptest.ResponseRecorder) logsResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse /api/logs response: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

// Forward pagination: after cursor + limit truncation, correct hasMore/next, paginate to the end.
func TestHandleLogs_ForwardPaginated(t *testing.T) {
	srv := newLogsSrv(t)
	after := time.Date(2026, 10, 8, 10, 0, 2, 0, time.Local) // after row 3 (index 2)

	// First batch: after=index2,limit=3 → index3,4,5, hasMore=true,next=index5 time.
	resp := decodeLogs(t, callLogs(srv, qv("after", after.Format(logTimeLayout), "limit", "3")))
	if resp.Count != 3 {
		t.Fatalf("count = %d, want 3", resp.Count)
	}
	want := []string{"line-03", "line-04", "line-05"}
	for i, row := range resp.Rows {
		if row.Text != want[i] {
			t.Errorf("rows[%d].Text = %q, want %q", i, row.Text, want[i])
		}
	}
	if !resp.HasMore {
		t.Error("hasMore should be true (rows remain)")
	}
	if resp.Next != "2026-10-08 10:00:05.000" {
		t.Errorf("next = %q, want index5 time", resp.Next)
	}

	// Second batch: after=next → index6..9, 4 rows, hasMore=false,next empty.
	resp2 := decodeLogs(t, callLogs(srv, qv("after", resp.Next)))
	if resp2.Count != 4 {
		t.Fatalf("second batch count = %d, want 4", resp2.Count)
	}
	if resp2.Rows[0].Text != "line-06" || resp2.Rows[3].Text != "line-09" {
		t.Errorf("second batch row order wrong: %v", resp2.Rows)
	}
	if resp2.HasMore {
		t.Error("second batch hasMore should be false (reached end of file)")
	}
	if resp2.Next != "" {
		t.Errorf("second batch next should be empty, got %q", resp2.Next)
	}
}

// until bound: only rows with ts<=until are returned.
func TestHandleLogs_UntilBound(t *testing.T) {
	srv := newLogsSrv(t)
	after := time.Date(2026, 10, 8, 10, 0, 0, 0, time.Local) // index0
	until := time.Date(2026, 10, 8, 10, 0, 3, 0, time.Local) // <= index3
	resp := decodeLogs(t, callLogs(srv, qv("after", after.Format(logTimeLayout), "until", until.Format(logTimeLayout))))
	if resp.Count != 3 { // index1,2,3
		t.Fatalf("count = %d, want 3", resp.Count)
	}
	if resp.Rows[2].Text != "line-03" {
		t.Errorf("last row = %q, want line-03", resp.Rows[2].Text)
	}
	if resp.HasMore {
		t.Error("hasMore should be false after until bound is reached")
	}
}

// filter regex: only matching rows are returned.
func TestHandleLogs_Filter(t *testing.T) {
	srv := newLogsSrv(t)
	resp := decodeLogs(t, callLogs(srv, qv("filter", "^line-0[345]$")))
	if resp.Count != 3 {
		t.Fatalf("count = %d, want 3", resp.Count)
	}
	if resp.Rows[0].Text != "line-03" || resp.Rows[2].Text != "line-05" {
		t.Errorf("filter matched rows wrong: %v", resp.Rows)
	}
}

// Tail snapshot: returns the latest N rows, chronological order.
func TestHandleLogs_Tail(t *testing.T) {
	srv := newLogsSrv(t)
	resp := decodeLogs(t, callLogs(srv, qv("tail", "true", "limit", "3")))
	if resp.Count != 3 {
		t.Fatalf("count = %d, want 3", resp.Count)
	}
	if resp.Rows[0].Text != "line-07" || resp.Rows[2].Text != "line-09" {
		t.Errorf("latest 3 tail rows wrong: %v", resp.Rows)
	}
	if resp.HasMore {
		t.Error("tail hasMore should be false")
	}
}

// Tail snapshot + filter: the latest N matching rows, chronological order.
func TestHandleLogs_TailFilter(t *testing.T) {
	srv := newLogsSrv(t)
	resp := decodeLogs(t, callLogs(srv, qv("tail", "true", "limit", "3", "filter", "^line-0[579]$")))
	if resp.Count != 3 {
		t.Fatalf("count = %d, want 3", resp.Count)
	}
	want := []string{"line-05", "line-07", "line-09"}
	for i, row := range resp.Rows {
		if row.Text != want[i] {
			t.Errorf("rows[%d].Text = %q, want %q", i, row.Text, want[i])
		}
	}
}

// Edge cases and errors.
func TestHandleLogs_Errors(t *testing.T) {
	// Local logging disabled (logPath empty) → 404.
	srv, _ := newTestServer(t, Options{})
	if rec := callLogs(srv, qv()); rec.Code != http.StatusNotFound {
		t.Errorf("no log status code = %d, want 404", rec.Code)
	}

	lsrv := newLogsSrv(t)
	// after > until → 400.
	a := "2026-10-08 10:00:05.000"
	u := "2026-10-08 10:00:01.000"
	rec := callLogs(lsrv, qv("after", a, "until", u))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("after>until status code = %d, want 400", rec.Code)
	}
	// tail conflicts with after → 400.
	if rec = callLogs(lsrv, qv("tail", "true", "after", a)); rec.Code != http.StatusBadRequest {
		t.Errorf("tail+after status code = %d, want 400", rec.Code)
	}
	// invalid limit (0) → 400.
	if rec = callLogs(lsrv, qv("limit", "0")); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0 status code = %d, want 400", rec.Code)
	}
	// invalid filter regex → 400.
	if rec = callLogs(lsrv, qv("filter", "[invalid")); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid filter status code = %d, want 400", rec.Code)
	}
	// invalid after time format → 400.
	if rec = callLogs(lsrv, qv("after", "not-a-time")); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid after time status code = %d, want 400", rec.Code)
	}
}

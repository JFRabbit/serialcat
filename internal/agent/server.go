package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JFRabbit/serialcat/internal/logger"
	"github.com/JFRabbit/serialcat/pkg/serial"
)

// Options are the startup options for agent serve, controlling write endpoint exposure and local logging (enabled by default).
type Options struct {
	// WriteEnabled enables the downstream write endpoint /api/write. Off by default (returns 403), the safest.
	WriteEnabled bool
	// WriteToken is the auth token for the write endpoint. When non-empty, /api/write requires Authorization: Bearer <token>.
	WriteToken string
	// NoLocalLog disables the default local logging (enabled by default). When local logging is enabled, the serial frame stream is additionally written to a log file in the current directory.
	NoLocalLog bool
	// OutDir is the default output directory for local logging; defaults to the current directory.
	OutDir string
}

// Server is the agent's HTTP server, exposing serial enumeration, real-time streaming, and write capabilities.
type Server struct {
	addr    string
	port    string
	baud    int
	drv     serial.Driver
	src     *SerialSource
	opts    Options
	ln      net.Listener // listener already bound (for automatic port increment); when nil, ListenAndServe is called internally
	cancel  context.CancelFunc
	httpSrv *http.Server
	logPath string // current default log file path for /api/logs history reads; empty when local logging is off
}

// NewServer builds the HTTP server and opens the serial port; call Start to serve once ready.
func NewServer(addr, serialPort string, baud int, drv serial.Driver, opts Options) (*Server, error) {
	p, err := drv.Open(serialPort, baud)
	if err != nil {
		return nil, fmt.Errorf("open serial port: %w", err)
	}
	return &Server{
		addr: addr,
		port: serialPort,
		baud: baud,
		drv:  drv,
		opts: opts,
		src:  NewSerialSource(p),
	}, nil
}

// SetListener injects an already-bound listener; when not injected, Start calls ListenAndServe internally.
func (s *Server) SetListener(l net.Listener) { s.ln = l }

// SetAddr updates the listen address (used to sync the actual address after port increment).
func (s *Server) SetAddr(a string) { s.addr = a }

// Start launches serial reading and the HTTP service, blocking until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Serial read goroutine: cancels the service on abnormal exit.
	go func() {
		if err := s.src.Run(sctx); err != nil && sctx.Err() == nil {
			logger.Error("serial read terminated: %v", err)
			cancel()
		}
	}()
	// Local default logging: enabled by default, the serial stream is additionally written to a local file (fully decoupled from /api/stream subscriptions).
	if !s.opts.NoLocalLog {
		s.startLocalLog(sctx)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/ports", s.handlePorts)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/write", s.handleWrite)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/logs", s.handleLogs)

	s.httpSrv = &http.Server{Addr: s.addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		if s.ln != nil {
			errCh <- s.httpSrv.Serve(s.ln)
		} else {
			errCh <- s.httpSrv.ListenAndServe()
		}
	}()

	logger.Info("agent serial proxy started  serial=%s baud=%d listen=%s", s.port, s.baud, s.addr)
	select {
	case <-sctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
		_ = s.src.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

// handleHealth is the liveness probe.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok\n"))
}

// handlePorts enumerates COM ports.
func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	ports, err := s.drv.ListPorts()
	if err != nil {
		httpError(w, err)
		return
	}
	if ports == nil {
		ports = []string{}
	}
	writeJSON(w, ports)
	_ = r
}

// handleStream serves the real-time serial stream (NDJSON long connection).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	subCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ch, err := s.src.Subscribe(subCtx)
	if err != nil {
		return
	}
	enc := json.NewEncoder(w)
	for f := range ch {
		if err := enc.Encode(f); err != nil {
			return // client disconnected
		}
		fl.Flush()
	}
}

// handleWrite handles downstream commands. Returns 403 when the write switch is off; when on, validates the Bearer token (if set).
// The literal \r \n \t escapes in data are handled on the sending side; escaping again here provides idempotency.
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	if !s.opts.WriteEnabled {
		http.Error(w, "write endpoint is disabled; start the agent with --enable-write", http.StatusForbidden)
		return
	}
	if s.opts.WriteToken != "" && r.Header.Get("Authorization") != "Bearer "+s.opts.WriteToken {
		http.Error(w, "unauthorized: missing or wrong authorization token", http.StatusUnauthorized)
		return
	}
	data := r.URL.Query().Get("data")
	if data == "" {
		if err := r.ParseForm(); err == nil {
			data = r.PostFormValue("data")
		}
	}
	data = strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t").Replace(data)
	if _, err := s.src.Write([]byte(data)); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]string{"ok": "sent"})
}

// startLocalLog subscribes to the serial frame stream and writes it to a local log file (enabled by default).
// The file name is serialcat-serial-{port}-{ts}.log, where {ts} expands to the startup timestamp (reusing the existing convention).
func (s *Server) startLocalLog(ctx context.Context) {
	name := fmt.Sprintf("serialcat-serial-%s-%s.log", s.port, time.Now().Format("20060102-150405"))
	if dir := s.opts.OutDir; dir != "" {
		name = filepath.Join(dir, name)
	}
	// The log path must be set before it can be read by /api/logs; the assignment must happen before the HTTP routes are registered/called.
	s.logPath = name
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Error("failed to create log directory: %v", err)
			return
		}
	}
	f, err := os.Create(name)
	if err != nil {
		logger.Error("failed to create local log file: %v", err)
		return
	}
	ch, err := s.src.Subscribe(ctx)
	if err != nil {
		f.Close()
		return
	}
	go func() {
		f := f // local mutable handle; updated after reopen, defer closes the latest handle
		w := bufio.NewWriter(f)
		curPath := name
		defer func() { _ = w.Flush(); _ = f.Close() }()
		for fr := range ch {
			// When the log file is deleted/moved externally, recreate the same-named (or incremented suffix) file and continue appending without interruption.
			if _, err := os.Stat(curPath); err != nil {
				if nf, npath, e := recreateLogFile(curPath); e == nil {
					_ = w.Flush() // flush the old buffer as much as possible
					_ = f.Close() // close the old handle; defer will close the new handle
					f, w, curPath = nf, bufio.NewWriter(nf), npath
					fmt.Printf("log file was deleted, recreated and continuing: %s\n", npath)
				} else {
					logger.Error("failed to recreate log file: %v", e)
				}
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", fr.Ts.Format("2006-01-02 15:04:05.000"), fr.Text)
			// Flush per line: the log must hit disk in real time to avoid sitting in the 4KB buffer and appearing late.
			_ = w.Flush()
		}
	}()
	// Print the file name to the console for locating the log (standalone stdout, isolated from logger).
	if abs, err := filepath.Abs(name); err == nil {
		fmt.Printf("local log file: %s\n", abs)
	} else {
		fmt.Printf("local log file: %s\n", name)
	}
	logger.Info("local logging enabled: %s", name)
}

// recreateLogFile recreates the log file after it was deleted: same name first; if that fails (e.g., taken by a directory)
// it automatically increments the suffix (e.g., xxx-1.log). Returns the new handle and path.
func recreateLogFile(name string) (*os.File, string, error) {
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", fmt.Errorf("recreate directory %s: %w", dir, err)
		}
	}
	if f, err := os.Create(name); err == nil {
		return f, name, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i <= 1000; i++ {
		alt := fmt.Sprintf("%s-%d%s", base, i, ext)
		if f, err := os.Create(alt); err == nil {
			return f, alt, nil
		}
	}
	return nil, "", fmt.Errorf("cannot reopen default log (same name and 1000 suffixed names all failed): %s", name)
}

// handleProbe probes baud rates for the given port and returns recommended parameters.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Query().Get("port")
	if port == "" {
		http.Error(w, "missing port parameter", http.StatusBadRequest)
		return
	}
	res, err := Probe(s.drv, port)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, res)
}

// Fixed prefix of the log line: 2006-01-02 15:04:05.000 (23 chars) + one space, then the raw text.
const logTimeLayout = "2006-01-02 15:04:05.000"

const logTimePrefixLen = len("2006-01-02 15:04:05.000")

// /api/logs response body: paginated history logs.
type logsResponse struct {
	Count   int       `json:"count"`   // number of rows in this batch
	HasMore bool      `json:"hasMore"` // whether more rows remain (true for forward pagination before EOF; always false for tail)
	Next    string    `json:"next"`    // continuation cursor: the last row's time when more rows remain (use as next after); empty otherwise
	Rows    []logsRow `json:"rows"`    // rows in this batch, chronological order
}

type logsRow struct {
	Ts   string `json:"ts"`   // timestamp in the log format
	Text string `json:"text"` // raw line text
}

// handleLogs reads history logs from the default log file.
// Forward pagination: GET /api/logs?after=<ts>&until=<ts>&limit=<N>&filter=<regex>
// Tail snapshot: GET /api/logs?tail=true&limit=<N>&filter=<regex> (latest N rows)
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.opts.NoLocalLog || s.logPath == "" {
		http.Error(w, "agent has local logging disabled (--no-local-log); no history logs", http.StatusNotFound)
		return
	}
	q := r.URL.Query()

	limit := 1000
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 10000 {
			http.Error(w, "invalid limit parameter (1-10000)", http.StatusBadRequest)
			return
		}
		limit = n
	}

	var re *regexp.Regexp
	if f := q.Get("filter"); f != "" {
		var err error
		if re, err = regexp.Compile(f); err != nil {
			http.Error(w, "invalid filter regex: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	match := func(text string) bool { return re == nil || re.MatchString(text) }

	// Tail snapshot mode: returns the latest limit rows at once, chronological order.
	if q.Get("tail") == "true" || q.Get("tail") == "1" {
		if q.Get("after") != "" || q.Get("until") != "" {
			http.Error(w, "tail mode cannot be combined with after/until", http.StatusBadRequest)
			return
		}
		rows, err := readTailLog(s.logPath, limit, match)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, logsResponse{Count: len(rows), Rows: rows})
		return
	}

	// Forward pagination mode.
	var after, until *time.Time
	if v := q.Get("after"); v != "" {
		t, err := time.ParseInLocation(logTimeLayout, v, time.Local)
		if err != nil {
			http.Error(w, "invalid after time format (expected "+logTimeLayout+")", http.StatusBadRequest)
			return
		}
		after = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.ParseInLocation(logTimeLayout, v, time.Local)
		if err != nil {
			http.Error(w, "invalid until time format (expected "+logTimeLayout+")", http.StatusBadRequest)
			return
		}
		until = &t
	}
	if after != nil && until != nil && after.After(*until) {
		http.Error(w, "after cannot be later than until", http.StatusBadRequest)
		return
	}

	rows, hasMore, next, err := readForwardLog(s.logPath, after, until, limit, match)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, logsResponse{Count: len(rows), HasMore: hasMore, Next: next, Rows: rows})
}

// parseLogLine parses a line into (time, raw text, valid). Invalid lines (not in the log format) are skipped.
func parseLogLine(raw []byte) (time.Time, string, bool) {
	if len(raw) < logTimePrefixLen+2 || raw[logTimePrefixLen] != ' ' {
		return time.Time{}, "", false
	}
	t, err := time.ParseInLocation(logTimeLayout, string(raw[:logTimePrefixLen]), time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	return t, string(raw[logTimePrefixLen+1:]), true
}

// readForwardLog linearly scans the history log forward: returns rows matching (after<ts<=until and filter),
// plus the (hasMore, next) continuation cursor.
func readForwardLog(logPath string, after, until *time.Time, limit int, match func(string) bool) ([]logsRow, bool, string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, false, "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var rows []logsRow
	var last time.Time
	for sc.Scan() {
		t, text, ok := parseLogLine(sc.Bytes())
		if !ok {
			continue
		}
		if after != nil && !t.After(*after) {
			continue
		}
		if until != nil && t.After(*until) {
			break
		}
		if !match(text) {
			continue
		}
		last = t
		rows = append(rows, logsRow{Ts: t.Format(logTimeLayout), Text: text})
		if len(rows) >= limit {
			// Batch limit reached: scan a bit further to decide hasMore/next.
			hasMore := scanHasMoreLog(logPath, last, until, match)
			var next string
			if hasMore {
				next = last.Format(logTimeLayout)
			}
			return rows, hasMore, next, nil
		}
	}
	return rows, false, "", sc.Err()
}

// scanHasMoreLog reports whether more rows exist after the given time (and <=until, matching filter).
func scanHasMoreLog(logPath string, after time.Time, until *time.Time, match func(string) bool) bool {
	f, err := os.Open(logPath)
	if err != nil {
		return true // on open failure, conservatively report more to avoid the client dropping the tail
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		t, text, ok := parseLogLine(sc.Bytes())
		if !ok {
			continue
		}
		if !t.After(after) {
			continue
		}
		if until != nil && t.After(*until) {
			return false
		}
		if match(text) {
			return true
		}
	}
	return false
}

// readTailLog reads the latest limit rows (matching filter) backward from the end of the file, returning them in chronological order.
// It reads back in chunks and stops once full, without reading the whole file.
func readTailLog(logPath string, limit int, match func(string) bool) ([]logsRow, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size == 0 {
		return []logsRow{}, nil
	}

	const chunk = 8192
	var rows []logsRow
	var partial []byte
	off := size

	extract := func() {
		for len(rows) < limit {
			idx := bytes.LastIndexByte(partial, '\n')
			if idx < 0 {
				return
			}
			line := partial[idx+1:]
			partial = partial[:idx]
			if len(line) == 0 {
				continue
			}
			if t, text, ok := parseLogLine(line); ok && match(text) {
				rows = append(rows, logsRow{Ts: t.Format(logTimeLayout), Text: text})
			}
		}
	}

	for off > 0 && len(rows) < limit {
		n := int64(chunk)
		if off < int64(chunk) {
			n = off
		}
		block := make([]byte, n)
		if _, err := f.ReadAt(block, off-n); err != nil {
			return rows, err
		}
		off -= n
		partial = append(block, partial...)
		extract()
	}
	// Wrap-up: after off==0, partial may still hold a complete line (extract already split it if rows are not full); the remaining newline-less segment is a fragment of the file's first line.
	extract()
	if len(rows) < limit && len(partial) > 0 {
		if t, text, ok := parseLogLine(partial); ok && match(text) {
			rows = append(rows, logsRow{Ts: t.Format(logTimeLayout), Text: text})
		}
	}
	// rows were collected backward from the file end; reverse into chronological order.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("failed to write JSON response: %v", err)
	}
}

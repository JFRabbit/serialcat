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

	"logdiag/internal/logger"
	"logdiag/pkg/serial"
)

// Options 是 agent serve 的启动选项，控制写接口暴露与本地落盘（默认开启）。
type Options struct {
	// WriteEnabled 是否暴露下行写接口 /api/write。默认关闭（返回 403），最安全。
	WriteEnabled bool
	// WriteToken 写接口鉴权令牌。非空时 /api/write 要求 Authorization: Bearer <token>。
	WriteToken string
	// NoLocalLog 关闭本地默认落盘（默认开启）。开启时串口帧流额外写当前路径日志文件。
	NoLocalLog bool
	// OutDir 默认落盘输出目录，默认当前路径。
	OutDir string
}

// Server 是 agent 的 HTTP 服务端，暴露串口枚举/实时流/下行能力。
type Server struct {
	addr    string
	port    string
	baud    int
	drv     serial.Driver
	src     *SerialSource
	opts    Options
	ln      net.Listener // 已绑定的监听器（用于端口自动递增），nil 时内部自行 ListenAndServe
	cancel  context.CancelFunc
	httpSrv *http.Server
	logPath string // 当前默认落盘日志文件路径，供 /api/logs 历史读取；未落盘时为空
}

// NewServer 构造 HTTP 服务端并打开串口，准备就绪后调用 Start 对外服务。
func NewServer(addr, serialPort string, baud int, drv serial.Driver, opts Options) (*Server, error) {
	p, err := drv.Open(serialPort, baud)
	if err != nil {
		return nil, fmt.Errorf("打开串口: %w", err)
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

// SetListener 注入已绑定的监听器；不注入时 Start 内部自行 ListenAndServe。
func (s *Server) SetListener(l net.Listener) { s.ln = l }

// SetAddr 更新监听地址（端口递增后用于同步实际地址）。
func (s *Server) SetAddr(a string) { s.addr = a }

// Start 启动串口读取与 HTTP 服务，阻塞直至 ctx 取消。
func (s *Server) Start(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// 串口读取协程：异常退出即取消服务。
	go func() {
		if err := s.src.Run(sctx); err != nil && sctx.Err() == nil {
			logger.Error("串口读取终止: %v", err)
			cancel()
		}
	}()
	// 本地默认落盘：默认开启，串口流额外写本地文件（与 /api/stream 订阅完全解耦）。
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

	logger.Info("agent 串口代理已启动 串口=%s 波特率=%d 监听=%s", s.port, s.baud, s.addr)
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

// handleHealth 探活。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok\n"))
}

// handlePorts 枚举 COM 端口。
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

// handleStream 实时串口流（NDJSON 长连接）。
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
			return // 客户端断开
		}
		fl.Flush()
	}
}

// handleWrite 下行命令。未开启写开关时返回 403；开启后校验 Bearer token（若设置）。
// data 中的 \r　\n \t 字面转义统一在发送侧处理，此处再转义一次做幂等保障。
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	if !s.opts.WriteEnabled {
		http.Error(w, "写接口未开启，agent 启动时需加 --enable-write", http.StatusForbidden)
		return
	}
	if s.opts.WriteToken != "" && r.Header.Get("Authorization") != "Bearer "+s.opts.WriteToken {
		http.Error(w, "未授权：缺少或错误的 authorization 令牌", http.StatusUnauthorized)
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

// startLocalLog 订阅串口帧流并写入本地落盘日志文件（默认开启）。
// 文件名 logdiag-serial-{port}-{ts}.log，{ts} 展开为启动时间戳（复用既有约定）。
func (s *Server) startLocalLog(ctx context.Context) {
	name := fmt.Sprintf("logdiag-serial-%s-%s.log", s.port, time.Now().Format("20060102-150405"))
	if dir := s.opts.OutDir; dir != "" {
		name = filepath.Join(dir, name)
	}
	// 当前落盘文件路径就绪后,才能被 /api/logs 读取;赋值须早于 http 路由注册/被调用。
	s.logPath = name
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Error("创建落盘日志目录失败: %v", err)
			return
		}
	}
	f, err := os.Create(name)
	if err != nil {
		logger.Error("创建本地落盘日志失败: %v", err)
		return
	}
	ch, err := s.src.Subscribe(ctx)
	if err != nil {
		f.Close()
		return
	}
	go func() {
		f := f // 局部可变句柄,重开后会更新,defer 关闭最新句柄
		w := bufio.NewWriter(f)
		curPath := name
		defer func() { _ = w.Flush(); _ = f.Close() }()
		for fr := range ch {
			// 落盘文件被外部删除/移走时,重建同名(或递增后缀)文件继续追加,保证不中断。
			if _, err := os.Stat(curPath); err != nil {
				if nf, npath, e := recreateLogFile(curPath); e == nil {
					_ = w.Flush() // 旧缓冲尽量落盘
					_ = f.Close() // 关闭旧句柄,defer 将关闭新句柄
					f, w, curPath = nf, bufio.NewWriter(nf), npath
					fmt.Printf("日志文件被删除,已重建续写: %s\n", npath)
				} else {
					logger.Error("重建落盘日志失败: %v", e)
				}
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", fr.Ts.Format("2006-01-02 15:04:05.000"), fr.Text)
			// 逐行 flush：落盘日志必须实时写盘，避免攒在 4KB 缓冲里"过一会才出现"。
			_ = w.Flush()
		}
	}()
	// 控制台打印输出文件名,方便定位落盘日志（standalone stdout,与 logger 隔离）。
	if abs, err := filepath.Abs(name); err == nil {
		fmt.Printf("本地落盘日志: %s\n", abs)
	} else {
		fmt.Printf("本地落盘日志: %s\n", name)
	}
	logger.Info("本地落盘日志已开启: %s", name)
}

// recreateLogFile 在落盘日志文件被删除后重建:优先同名;同名创建失败(如被目录占用)
// 则自动递增后缀(如 xxx-1.log)。返回新句柄与新路径。
func recreateLogFile(name string) (*os.File, string, error) {
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", fmt.Errorf("重建目录 %s: %w", dir, err)
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
	return nil, "", fmt.Errorf("无法重开默认日志(同名及 1000 个后缀均失败): %s", name)
}

// handleProbe 对指定端口做波特率探测并返回推荐参数。
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	port := r.URL.Query().Get("port")
	if port == "" {
		http.Error(w, "缺少 port 参数", http.StatusBadRequest)
		return
	}
	res, err := Probe(s.drv, port)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, res)
}

// 落盘日志固定前缀：2006-01-02 15:04:05.000(23字符) + 一个空格，然后才是原文。
const logTimeLayout = "2006-01-02 15:04:05.000"

const logTimePrefixLen = len("2006-01-02 15:04:05.000")

// /api/logs 返回体：分页式历史日志。
type logsResponse struct {
	Count   int       `json:"count"`   // 本批返回行数
	HasMore bool      `json:"hasMore"` // 是否还有更多（正向续读且未到末尾时为 true；尾部恒 false）
	Next    string    `json:"next"`    // 续读游标：有更多时填最后一条时间，调用方下次作为 after；否则空
	Rows    []logsRow `json:"rows"`    // 本批行，时间正序
}

type logsRow struct {
	Ts   string `json:"ts"`   // 落盘格式时间戳
	Text string `json:"text"` // 原始行原文
}

// handleLogs 历史日志读取：从默认落盘文件读取。
// 正向续读：GET /api/logs?after=<ts>&until=<ts>&limit=<N>&filter=<正则>
// 尾部快照：GET /api/logs?tail=true&limit=<N>&filter=<正则>（最近 N 条）
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.opts.NoLocalLog || s.logPath == "" {
		http.Error(w, "agent 未开启本地落盘(--no-local-log)，无历史日志", http.StatusNotFound)
		return
	}
	q := r.URL.Query()

	limit := 1000
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 10000 {
			http.Error(w, "limit 参数非法（1-10000）", http.StatusBadRequest)
			return
		}
		limit = n
	}

	var re *regexp.Regexp
	if f := q.Get("filter"); f != "" {
		var err error
		if re, err = regexp.Compile(f); err != nil {
			http.Error(w, "filter 正则无效: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	match := func(text string) bool { return re == nil || re.MatchString(text) }

	// 尾部快照模式：一次性返回最近 limit 行，时间正序。
	if q.Get("tail") == "true" || q.Get("tail") == "1" {
		if q.Get("after") != "" || q.Get("until") != "" {
			http.Error(w, "tail 模式不能同时使用 after/until", http.StatusBadRequest)
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

	// 正向续读模式。
	var after, until *time.Time
	if v := q.Get("after"); v != "" {
		t, err := time.ParseInLocation(logTimeLayout, v, time.Local)
		if err != nil {
			http.Error(w, "after 时间格式无效（应为 "+logTimeLayout+"）", http.StatusBadRequest)
			return
		}
		after = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.ParseInLocation(logTimeLayout, v, time.Local)
		if err != nil {
			http.Error(w, "until 时间格式无效（应为 "+logTimeLayout+"）", http.StatusBadRequest)
			return
		}
		until = &t
	}
	if after != nil && until != nil && after.After(*until) {
		http.Error(w, "after 不能晚于 until", http.StatusBadRequest)
		return
	}

	rows, hasMore, next, err := readForwardLog(s.logPath, after, until, limit, match)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, logsResponse{Count: len(rows), HasMore: hasMore, Next: next, Rows: rows})
}

// parseLogLine 解析一行为 (时间, 原文, 是否合法)。非法行(非本落盘格式)直接跳过。
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

// readForwardLog 正向线性扫描历史落盘文件：返回满足条件(after<ts<=until 且命中 filter)的行，
// 以及(hasMore, next) 续读游标。
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
			// 已达到本批上限：再向后扫一段判断还有没有匹配行，决定 hasMore/next。
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

// scanHasMoreLog 判断在 after 之后(且 <=until、命中 filter)是否还有行。
func scanHasMoreLog(logPath string, after time.Time, until *time.Time, match func(string) bool) bool {
	f, err := os.Open(logPath)
	if err != nil {
		return true // 打开失败保守返回有更多，避免客户端丢掉尾部
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

// readTailLog 从文件末尾反向读取最近的 limit 行（命中 filter），返回时间正序。
// 采用分块往回读，读满即停，不整读全文件。
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
	// 收尾：off==0 后 partial 可能仍含完整行(rows 未满时 extract 已拆)；剩余无换行段是文件首行残段。
	extract()
	if len(rows) < limit && len(partial) > 0 {
		if t, text, ok := parseLogLine(partial); ok && match(text) {
			rows = append(rows, logsRow{Ts: t.Format(logTimeLayout), Text: text})
		}
	}
	// rows 为从文件尾向前收集，反转成时间正序。
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
		logger.Error("写 JSON 响应失败: %v", err)
	}
}

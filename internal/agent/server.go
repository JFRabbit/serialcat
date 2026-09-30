package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"logdiag/internal/logger"
	"logdiag/pkg/serial"
)

// Options 是 agent serve 的启动选项，控制写接口暴露与本地兜底落盘。
type Options struct {
	// WriteEnabled 是否暴露下行写接口 /api/write。默认关闭（返回 403），最安全。
	WriteEnabled bool
	// WriteToken 写接口鉴权令牌。非空时 /api/write 要求 Authorization: Bearer <token>。
	WriteToken string
	// NoLocalLog 关闭本地兜底落盘（默认开启）。开启时串口帧流额外写当前路径日志文件作为兜底。
	NoLocalLog bool
	// OutDir 兜底落盘输出目录，默认当前路径。
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
	// 本地兜底落盘：默认开启，串口流额外写本地文件（与 /api/stream 订阅完全解耦）。
	if !s.opts.NoLocalLog {
		s.startLocalLog(sctx)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/ports", s.handlePorts)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/write", s.handleWrite)
	mux.HandleFunc("/api/probe", s.handleProbe)

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
// data 中的 \r　\n \t 字面转义统一在发送侧处理，此处兜底再转义一次（幂等）。
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

// startLocalLog 订阅串口帧流并写入本地兜底日志文件（默认开启）。
// 文件名 logdiag-serial-{port}-{ts}.log，{ts} 展开为启动时间戳（复用既有约定）。
func (s *Server) startLocalLog(ctx context.Context) {
	name := fmt.Sprintf("logdiag-serial-%s-%s.log", s.port, time.Now().Format("20060102-150405"))
	if dir := s.opts.OutDir; dir != "" {
		name = filepath.Join(dir, name)
	}
	if dir := filepath.Dir(name); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Error("创建兜底日志目录失败: %v", err)
			return
		}
	}
	f, err := os.Create(name)
	if err != nil {
		logger.Error("创建本地兜底日志失败: %v", err)
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
			// 兜底文件被外部删除/移走时,重建同名(或递增后缀)文件继续追加,保证兜底不断。
			if _, err := os.Stat(curPath); err != nil {
				if nf, npath, e := recreateLogFile(curPath); e == nil {
					_ = w.Flush() // 旧缓冲尽量落盘
					_ = f.Close() // 关闭旧句柄,defer 将关闭新句柄
					f, w, curPath = nf, bufio.NewWriter(nf), npath
					fmt.Printf("兜底日志文件被删除,已重建续写: %s\n", npath)
				} else {
					logger.Error("重建兜底日志失败: %v", e)
				}
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", fr.Ts.Format("2006-01-02 15:04:05.000"), fr.Text)
			// 逐行 flush：兜底日志必须实时落盘，避免攒在 4KB 缓冲里"过一会才出现"。
			_ = w.Flush()
		}
	}()
	// 控制台打印输出文件名,方便定位兜底日志（standalone stdout,与 logger 隔离）。
	if abs, err := filepath.Abs(name); err == nil {
		fmt.Printf("本地兜底日志: %s\n", abs)
	} else {
		fmt.Printf("本地兜底日志: %s\n", name)
	}
	logger.Info("本地兜底日志已开启: %s", name)
}

// recreateLogFile 在兜底日志文件被删除后重建:优先同名;同名创建失败(如被目录占用)
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
	return nil, "", fmt.Errorf("无法重开兜底日志(同名及 1000 个后缀均失败): %s", name)
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

func httpError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("写 JSON 响应失败: %v", err)
	}
}

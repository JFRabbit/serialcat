package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"logdiag/internal/logger"
)

// Server 是 agent 的 HTTP 服务端，暴露串口枚举/实时流/下行能力。
type Server struct {
	addr    string
	port    string
	baud    int
	drv     Driver
	src     *SerialSource
	ln      net.Listener // 已绑定的监听器（用于端口自动递增），nil 时内部自行 ListenAndServe
	cancel  context.CancelFunc
	httpSrv *http.Server
}

// NewServer 构造 HTTP 服务端并打开串口，准备就绪后调用 Start 对外服务。
func NewServer(addr, serialPort string, baud int, drv Driver) (*Server, error) {
	p, err := drv.Open(serialPort, baud)
	if err != nil {
		return nil, fmt.Errorf("打开串口: %w", err)
	}
	return &Server{
		addr: addr,
		port: serialPort,
		baud: baud,
		drv:  drv,
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

// handleWrite 下行命令（阶段② TX 预留）。
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	data := strings.ReplaceAll(r.URL.Query().Get("data"), "\\r", "\r")
	if data == "" {
		if err := r.ParseForm(); err == nil {
			data = r.PostFormValue("data")
		}
	}
	if _, err := s.src.Write([]byte(data)); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]string{"ok": "sent"})
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

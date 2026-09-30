// Package app 是 CLI 的业务编排层：加载配置、校验、运行 DAG。
// 依赖 pkg/ 下的引擎与节点库，不包含 cobra 相关代码。
package app

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"logdiag/internal/logger"
	"logdiag/pkg/config"
	"logdiag/pkg/nodes"
)

// RunOptions 汇聚 run 的局部选项。
type RunOptions struct {
	// LocalWrite 是否开启本地写通道：source:serial 打开串口后，在回环端口暴露 POST /api/write，
	// 供 send --url http://127.0.0.1:<Port> 复用同一串口句柄下发命令（避免重复 Open 冲突）。默认 7100，同 agent。
	LocalWrite     bool
	LocalWritePort int
}

// Run 加载并执行一个 DAG 配置文件，阻塞至执行结束或收到中断信号。
func Run(ctx context.Context, cfgPath string, opts RunOptions) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置 %s: %w", cfgPath, err)
	}
	g, err := nodes.Build(cfg)
	if err != nil {
		return err
	}

	if opts.LocalWrite {
		// 把写能力注入到 source:serial 节点：串口打开后起回环写 server，复用其句柄。
		for _, n := range g.Nodes {
			if s, ok := n.(*nodes.LocalSerial); ok {
				port := opts.LocalWritePort
				s.SetOnOpen(func(ctx context.Context, p io.ReadWriteCloser) {
					startLoopbackWrite(ctx, p, port)
				})
			}
		}
	}

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Info("收到中断信号，停止")
		cancel()
	}()

	return g.Run(rctx)
}

// startLoopbackWrite 在 127.0.0.1:port 暴露 POST /api/write，把内容写入串口句柄 p。
// 仅绑回环、不做鉴权（本机即身份）；端口被占用时仅提示、不阻塞 run。随 ctx 取消而关停。
func startLoopbackWrite(ctx context.Context, p io.ReadWriteCloser, port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Info("本地写端口 %d 开启失败（可能被占用），未启用，%v", port, err)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/write", func(w http.ResponseWriter, r *http.Request) {
		data := r.URL.Query().Get("data")
		if data == "" {
			if err := r.ParseForm(); err == nil {
				data = r.PostFormValue("data")
			}
		}
		if _, err := p.Write([]byte(data)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"sent"}`))
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	logger.Info("本地写通道已开启 127.0.0.1:%d", port)
}

// Validate 只校验配置拓扑与节点构建，不执行。
func Validate(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置 %s: %w", cfgPath, err)
	}
	if _, err := nodes.Build(cfg); err != nil {
		return err
	}
	return nil
}

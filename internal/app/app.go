// Package app is the CLI orchestration layer: loads config, validates, and runs DAGs.
// It depends on the engine and node libraries under pkg/ and contains no cobra code.
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

	"github.com/JFRabbit/serialcat/internal/logger"
	"github.com/JFRabbit/serialcat/pkg/config"
	"github.com/JFRabbit/serialcat/pkg/nodes"
)

// RunOptions collects the run command's local options.
type RunOptions struct {
	// LocalWrite enables the local write channel: after source:serial opens the port, expose POST /api/write
	// on a loopback port, so send --url http://127.0.0.1:<Port> can reuse the same serial handle to send
	// commands (avoiding repeated Open conflicts). Default 7100, same as the agent.
	LocalWrite     bool
	LocalWritePort int
}

// Run loads and executes a DAG config file, blocking until execution finishes or an interrupt signal arrives.
func Run(ctx context.Context, cfgPath string, opts RunOptions) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", cfgPath, err)
	}
	g, err := nodes.Build(cfg)
	if err != nil {
		return err
	}

	if opts.LocalWrite {
		// Inject the write capability into the source:serial node: after the port opens, start a loopback write server reusing its handle.
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
		logger.Info("received interrupt signal, stopping")
		cancel()
	}()

	return g.Run(rctx)
}

// startLoopbackWrite exposes POST /api/write on 127.0.0.1:port, writing payloads to the serial handle p.
// It binds loopback only, without auth (localhost is the identity); when the port is taken it only logs
// and does not block run. It stops when ctx is canceled.
func startLoopbackWrite(ctx context.Context, p io.ReadWriteCloser, port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Info("local write port %d failed to open (maybe in use), not enabled, %v", port, err)
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

	logger.Info("local write channel enabled 127.0.0.1:%d", port)
}

// Validate only checks the config topology and node building, without running.
func Validate(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", cfgPath, err)
	}
	if _, err := nodes.Build(cfg); err != nil {
		return err
	}
	return nil
}

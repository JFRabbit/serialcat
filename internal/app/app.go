// Package app 是 CLI 的业务编排层：加载配置、校验、运行 DAG。
// 依赖 pkg/ 下的引擎与节点库，不包含 cobra 相关代码。
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"logdiag/internal/logger"
	"logdiag/pkg/config"
	"logdiag/pkg/nodes"
)

// Run 加载并执行一个 DAG 配置文件，阻塞至执行结束或收到中断信号。
func Run(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置 %s: %w", cfgPath, err)
	}
	g, err := nodes.Build(cfg)
	if err != nil {
		return err
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
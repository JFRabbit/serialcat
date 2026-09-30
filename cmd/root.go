package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"logdiag/internal/console"
	"logdiag/internal/constant"
	"logdiag/internal/logger"
)

// 版本注入变量：由 ldflags 覆盖。
var (
	Version   = "unset"
	Commit    = "unset"
	BuildTime = "unset"
)

// debug 是全局调试 flag，包级变量便于测试 save/restore。
var debug bool

// insecure 跳过 TLS 证书校验（自签名证书更新源场景），供 update 使用。
var insecure bool

var rootCmd = &cobra.Command{
	Use:     constant.AppName,
	Version: fmt.Sprintf("%s commit %s at %s", Version, Commit, BuildTime),
	// flag 解析完成后只执行一次：初始化 logger。
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return logger.Init(debug)
	},
	// 退出前 flush 日志。
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		logger.Sync()
	},
}

// Execute 是程序唯一入口，只在此处允许 os.Exit。
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if logger.Sugar != nil {
			logger.Error("%s", err)
		} else {
			os.Stderr.WriteString(err.Error() + "\n") // logger 未初始化时降级
		}
		os.Exit(1)
	}
}

func init() {
	// Windows 下启动即关闭控制台快速编辑模式,避免选中文本时挂起高频输出。
	console.DisableQuickEdit()

	rootCmd.PersistentFlags().StringVarP(&configFile, constant.FlagConfig, "f", constant.DefaultConfigFile, "DAG 配置文件路径")
	rootCmd.PersistentFlags().BoolVar(&debug, constant.FlagDebug, false, "启用调试日志")
	rootCmd.PersistentFlags().BoolVar(&insecure, "insecure", false, "跳过 TLS 证书校验（自签名更新源）")
	rootCmd.SilenceUsage = true  // 失败不打印 usage
	rootCmd.SilenceErrors = true // 错误只输出一次
}

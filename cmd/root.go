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

var rootCmd = &cobra.Command{
	Use:     constant.AppName,
	Short:   "串口日志读取、过滤、下发命令的轻量工具",
	Long:    "logdiag 串口日志抓取工具",
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
		}
		// 错误始终打到 stderr，便于 send 等一次性命令立刻看到失败原因；
		// 同时（logger 已初始化时）记录到 logdiag.log。
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func init() {
	// Windows 下启动即关闭控制台快速编辑模式,避免选中文本时挂起高频输出。
	console.DisableQuickEdit()
	rootCmd.PersistentFlags().BoolVar(&debug, constant.FlagDebug, false, "启用调试日志")
	rootCmd.SilenceUsage = true  // 失败不打印 usage
	rootCmd.SilenceErrors = true // 错误只输出一次
}

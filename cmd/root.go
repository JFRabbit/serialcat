package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/console"
	"github.com/JFRabbit/serialcat/internal/constant"
	"github.com/JFRabbit/serialcat/internal/logger"
)

// 版本注入变量：由 ldflags 覆盖。
var (
	Version   = "unset"
	Commit    = "unset"
	BuildTime = "unset"
)

// copyrightNote 是版权 / 许可声明，用于 --version 与 --help 的尾部。
const copyrightNote = "Copyright 2026 JFRabbit. 依 Apache-2.0 开源,详见 LICENSE 与 THIRD_PARTY_NOTICES."

// debug 是全局调试 flag，包级变量便于测试 save/restore。
var debug bool

var rootCmd = &cobra.Command{
	Use:     constant.AppName,
	Short:   "串口日志读取、过滤、下发命令的轻量工具",
	Long:    "serialcat 串口日志抓取工具",
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
		// 同时（logger 已初始化时）记录到 serialcat.log。
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

	// --version：在默认"name version <ver>"后再打印版权 / 许可声明。
	rootCmd.SetVersionTemplate("{{with .Name}}{{printf \"%s \" .}}{{end}}{{printf \"version %s\\n\" .Version}}" + copyrightNote + "\n")
	// --help：保留默认 help 结构，仅在文末追加一行版权 / 许可声明。
	rootCmd.SetHelpTemplate("{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}\n{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}\n" + copyrightNote + "\n")
}

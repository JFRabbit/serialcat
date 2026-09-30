package cmd

import (
	"github.com/spf13/cobra"

	"logdiag/internal/app"
	"logdiag/internal/constant"
)

// configFile 由 run/val 的局部 -f/--config flag 填充。
var configFile string

var (
	runEnableWrite     bool
	runEnableWritePort int
)

// runCmd 执行一个 DAG：加载配置 → 构建图 → 运行至结束。
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "按配置读取并处理日志",
	Long: "按配置文件里定义的流程，把日志来源（本机串口、远端串口代理、或日志文件）的内容" +
		"过滤后打印到终端或保存到文件。\n" +
		"参数：-f, --config 配置文件路径(默认 dag.yaml)。\n" +
		"如需在运行的同时下发命令：加 --enable-write 开启写通道，再用 send --url 指名到本机即可；" +
		"--enable-write-port 可改写通道端口(默认 7100)。",
	RunE: func(cmd *cobra.Command, args []string) error {
		return app.Run(cmd.Context(), configFile, app.RunOptions{
			LocalWrite:     runEnableWrite,
			LocalWritePort: runEnableWritePort,
		})
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	fs := runCmd.Flags()
	fs.StringVarP(&configFile, constant.FlagConfig, "f", constant.DefaultConfigFile, "配置文件路径")
	fs.BoolVar(&runEnableWrite, "enable-write", false, "开启写通道，便于用 send 通过本机串口下发命令")
	fs.IntVar(&runEnableWritePort, "enable-write-port", 7100, "写通道端口（默认与 agent 一致）")
}

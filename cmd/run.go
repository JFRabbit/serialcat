package cmd

import (
	"github.com/spf13/cobra"

	"logdiag/internal/app"
)

// configFile 由 root 注册的全局 flag 填充。
var configFile string

// runCmd 执行一个 DAG：加载配置 → 构建图 → 运行至结束。
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "运行一个 DAG 配置",
	RunE: func(cmd *cobra.Command, args []string) error {
		return app.Run(cmd.Context(), configFile)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
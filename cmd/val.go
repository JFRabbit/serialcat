package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"logdiag/internal/app"
)

// valCmd 校验配置拓扑，不执行 DAG。成功/失败结果直接输出到控制台。
var valCmd = &cobra.Command{
	Use:   "val",
	Short: "校验 DAG 配置（不执行）",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := app.Validate(configFile); err != nil {
			fmt.Printf("校验失败: %v\n", err)
			return err
		}
		fmt.Println("校验通过")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(valCmd)
}
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"logdiag/internal/app"
	"logdiag/internal/constant"
)

// valCmd 校验配置拓扑，不执行 DAG。成功/失败结果直接输出到控制台。
var valCmd = &cobra.Command{
	Use:   "verify",
	Short: "检查配置文件是否合法",
	Long: "检查配置文件能否被正确读取和执行，不会真正运行日志流程。\n" +
		"用于修改配置后先自检一遍。\n" +
		"参数：-f, --config 配置文件路径(默认 dag.yaml)。",
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
	valCmd.Flags().StringVarP(&configFile, constant.FlagConfig, "f", constant.DefaultConfigFile, "配置文件路径")
}

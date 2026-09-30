package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"logdiag/internal/constant"
)

// short 是 version 命令的短输出 flag。
var short bool

// versionCmd 打印版本信息；--short 只出版本号，供脚本取用。
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "打印版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		if short {
			fmt.Println(Version)
			return
		}
		fmt.Printf("%s version %s (commit %s, built %s)\n", constant.AppName, Version, Commit, BuildTime)
	},
}

func init() {
	versionCmd.Flags().BoolVar(&short, constant.FlagShort, false, "仅返回版本号")
	rootCmd.AddCommand(versionCmd)
}
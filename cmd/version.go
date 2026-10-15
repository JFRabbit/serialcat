package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/constant"
)

// short is the short-output flag of the version command.
var short bool

// versionCmd prints version info; --short prints only the version number for scripts.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		if short {
			fmt.Println(Version)
			return
		}
		fmt.Printf("%s version %s (commit %s, built %s)\n", constant.AppName, Version, Commit, BuildTime)
		fmt.Println(copyrightNote)
	},
}

func init() {
	versionCmd.Flags().BoolVar(&short, constant.FlagShort, false, "print the version number only")
	rootCmd.AddCommand(versionCmd)
}
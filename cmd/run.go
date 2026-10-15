package cmd

import (
	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/app"
	"github.com/JFRabbit/serialcat/internal/constant"
)

// configFile is populated by the local -f/--config flags of run/val.
var configFile string

var (
	runEnableWrite     bool
	runEnableWritePort int
)

// runCmd executes a DAG: load config, build the graph, run to completion.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Read and process logs per config",
	Long: "Follow the flow defined in the config file to filter log sources (local serial port, remote serial agent, or log files) " +
		"and print the result to the terminal or save it to a file.\n" +
		"Arguments: -f, --config config file path (default dag.yaml).\n" +
		"To send commands while running: add --enable-write to open the write channel, then use send --url to point to this machine; " +
		"--enable-write-port changes the write channel port (default 7100).",
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
	fs.StringVarP(&configFile, constant.FlagConfig, "f", constant.DefaultConfigFile, "config file path")
	fs.BoolVar(&runEnableWrite, "enable-write", false, "enable the write channel so send can push commands through the local serial port")
	fs.IntVar(&runEnableWritePort, "enable-write-port", 7100, "write channel port (defaults to the same as agent)")
}

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/app"
	"github.com/JFRabbit/serialcat/internal/constant"
)

// valCmd validates the config topology without executing the DAG; the result is printed to the console.
var valCmd = &cobra.Command{
	Use:   "verify",
	Short: "Check whether the config file is valid",
	Long: "Check whether the config file can be read and executed correctly, without actually running the log flow.\n" +
		"Use it to self-check a config after editing.\n" +
		"Arguments: -f, --config config file path (default dag.yaml).",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := app.Validate(configFile); err != nil {
			fmt.Printf("validation failed: %v\n", err)
			return err
		}
		fmt.Println("validation passed")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(valCmd)
	valCmd.Flags().StringVarP(&configFile, constant.FlagConfig, "f", constant.DefaultConfigFile, "config file path")
}

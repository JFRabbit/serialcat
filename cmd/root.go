package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/JFRabbit/serialcat/internal/console"
	"github.com/JFRabbit/serialcat/internal/constant"
	"github.com/JFRabbit/serialcat/internal/logger"
)

// Version injection variables: overridden by ldflags.
var (
	Version   = "unset"
	Commit    = "unset"
	BuildTime = "unset"
)

// copyrightNote is the copyright/license note, appended at the end of --version and --help output.
const copyrightNote = "Copyright 2026 JFRabbit. Licensed under Apache-2.0; see LICENSE and THIRD_PARTY_NOTICES."

// debug is the global debug flag; package-level so tests can save/restore it.
var debug bool

var rootCmd = &cobra.Command{
	Use:     constant.AppName,
	Short:   "Lightweight tool for reading, filtering and sending commands over serial logs",
	Long:    "serialcat serial log capture tool",
	Version: fmt.Sprintf("%s commit %s at %s", Version, Commit, BuildTime),
	// Run once after flags are parsed: initialize the logger.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		return logger.Init(debug)
	},
	// Flush logs before exit.
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		logger.Sync()
	},
}

// Execute is the single entry point; os.Exit is only allowed here.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if logger.Sugar != nil {
			logger.Error("%s", err)
		}
		// Errors always go to stderr so one-shot commands like send can see the failure
		// reason immediately; they are also recorded to serialcat.log (when the logger is initialized).
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func init() {
	// On Windows, disable console quick-edit mode at startup to avoid hanging on
	// high-frequency output when text is selected.
	console.DisableQuickEdit()
	rootCmd.PersistentFlags().BoolVar(&debug, constant.FlagDebug, false, "enable debug logging")
	rootCmd.SilenceUsage = true  // do not print usage on failure
	rootCmd.SilenceErrors = true // errors are printed only once

	// --version: print the copyright/license note after the default "name version <ver>" line.
	rootCmd.SetVersionTemplate("{{with .Name}}{{printf \"%s \" .}}{{end}}{{printf \"version %s\\n\" .Version}}" + copyrightNote + "\n")
	// --help: keep the default help layout, only append the copyright/license note at the end.
	rootCmd.SetHelpTemplate("{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}\n{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}\n" + copyrightNote + "\n")
}

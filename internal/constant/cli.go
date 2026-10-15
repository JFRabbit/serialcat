// Package constant centralizes CLI constants: app name, flag names, default paths, to avoid magic strings.
package constant

// AppName is the application name.
const AppName = "serialcat"

// Defaults
const (
	// DefaultConfigFile is the default DAG config file path.
	DefaultConfigFile = "dag.yaml"
)

// Flag names
const (
	// FlagConfig is the config file path flag.
	FlagConfig = "config"
	// FlagShort is the short-output flag of the version command.
	FlagShort = "short"
	// FlagDebug is the debug logging flag.
	FlagDebug = "debug"
)

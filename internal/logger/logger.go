// Package logger wraps zap, providing a printf-style facade (sugar) plus a singleton.
// Console-colored console encoder, local timezone time format yyyy-MM-dd HH:mm:ss.
// Global state is allowed only for the logger singleton and cmd package-level flag variables.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Fixed log file under logs/ in the run directory. Tool logs are written to both stdout and this file,
// so they stay traceable when flooded by console node output. The file uses no color encoding.
const (
	logDir      = "logs"
	logFileName = "serialcat.log"
)

// Sugar is the global facade, nil until initialized. Call Init before use.
var Sugar *zap.SugaredLogger

// Holds the file handle; Sync/Close on exit.
var logFile *os.File

// Init initializes the global logger. Debug level is enabled when verbose is true.
// Output: only logs/serialcat.log under the run directory (no color).
// Tool logs are fully isolated from console node output: the console only shows the console node's data stream.
func Init(verbose bool) error {
	level := zapcore.InfoLevel
	if verbose {
		level = zapcore.DebugLevel
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "name",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder, // no ANSI color codes
		EncodeTime:     localTimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	f, err := openLogFile()
	if err != nil {
		return err
	}
	logFile = f
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(f),
		level,
	)
	Sugar = zap.New(core, zap.AddCallerSkip(1)).Sugar()
	return nil
}

// openLogFile opens (appends to) logs/serialcat.log under the run directory (creating directories automatically).
func openLogFile() (*os.File, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create log directory %s: %w", logDir, err)
	}
	name := filepath.Join(logDir, logFileName)
	// filepath.Abs only normalizes the relative path; it does not change the "run directory" semantics.
	if abs, err := filepath.Abs(name); err == nil {
		name = abs
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cannot open log file %s: %w", name, err)
	}
	return f, nil
}

// Sync flushes the logs; call before exit.
func Sync() {
	if Sugar != nil {
		_ = Sugar.Sync()
	}
	if logFile != nil {
		_ = logFile.Sync()
		_ = logFile.Close()
		logFile = nil
	}
}

// printf-style facade. Silently drops logs when uninitialized.
func Debug(f string, a ...any) { sugar().Debugf(f, a...) }
func Info(f string, a ...any)  { sugar().Infof(f, a...) }
func Warn(f string, a ...any)  { sugar().Warnf(f, a...) }
func Error(f string, a ...any) { sugar().Errorf(f, a...) }

func sugar() *zap.SugaredLogger {
	if Sugar == nil {
		// Return a no-op sugar to avoid a nil panic.
		return zap.NewNop().Sugar()
	}
	return Sugar
}

// localTimeEncoder outputs local timezone time.
func localTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Local().Format("2006-01-02 15:04:05"))
}

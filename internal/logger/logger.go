// Package logger 封装 zap，提供 printf 风格门面（sugar）+ 单例。
// 控制台彩色 console encoder，本地时区时间格式 yyyy-MM-dd HH:mm:ss。
// 全局状态仅允许 logger 单例与 cmd 包级 flag 变量。
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// 运行目录下 logs/ 里的固定日志文件。工具自身日志同时写 stdout 与该文件，
// 避免被 console 节点的输出淹没后无法追溯。文件用无颜色编码。
const (
	logDir      = "logs"
	logFileName = "logdiag.log"
)

// Sugar 是全局门面，未初始化时为 nil。使用前先调用 Init。
var Sugar *zap.SugaredLogger

// 持有一份文件句柄，退出时 Sync/Close。
var logFile *os.File

// Init 初始化全局 logger。verbose 为 true 时开启 Debug 级。
// 输出：仅运行目录下 logs/logdiag.log（无颜色）。
// 工具自身日志与 console 节点输出完全隔离：控制台只显示 console 节点的数据流。
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
		EncodeLevel:    zapcore.CapitalLevelEncoder, // 无 ANSI 色码
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

// openLogFile 打开（追加）运行目录下 logs/logdiag.log（自动建目录）。
func openLogFile() (*os.File, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建日志目录 %s: %w", logDir, err)
	}
	name := filepath.Join(logDir, logFileName)
	// filepath.Abs 仅用于把相对路径规范化，不改变"运行目录"语义。
	if abs, err := filepath.Abs(name); err == nil {
		name = abs
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("无法打开日志文件 %s: %w", name, err)
	}
	return f, nil
}

// Sync flush 日志，退出前调用。
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

// printf 风格门面。未初始化时静默丢弃。
func Debug(f string, a ...any) { sugar().Debugf(f, a...) }
func Info(f string, a ...any)  { sugar().Infof(f, a...) }
func Warn(f string, a ...any)  { sugar().Warnf(f, a...) }
func Error(f string, a ...any) { sugar().Errorf(f, a...) }

func sugar() *zap.SugaredLogger {
	if Sugar == nil {
		// 返回一个丢弃式 sugar，避免 nil panic。
		return zap.NewNop().Sugar()
	}
	return Sugar
}

// localTimeEncoder 输出本地时区时间。
func localTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Local().Format("2006-01-02 15:04:05"))
}

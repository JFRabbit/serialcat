// Package constant 集中管理 CLI 的常量：应用名、flag 名、默认路径，杜绝魔法字符串。
package constant

// AppName 应用名。
const AppName = "logdiag"

// 默认值
const (
	// DefaultConfigFile 默认 DAG 配置文件路径。
	DefaultConfigFile = "dag.yaml"
)

// Flag 名
const (
	// FlagConfig 配置文件路径 flag。
	FlagConfig = "config"
	// FlagShort 版本命令的短输出 flag。
	FlagShort = "short"
	// FlagDebug 调试日志 flag。
	FlagDebug = "debug"
)
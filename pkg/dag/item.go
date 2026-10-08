package dag

import "time"

// Item 是在边上传送的一条记录。它是一条不可再分的日志单元。
type Item struct {
	Ts   time.Time         // 记录时间（未采样到则以到达时间回退）
	Text string            // 原始行文本
	Src  string            // 来源节点 id
	Meta map[string]string // 可选键值，由加工节点填充
}

// NewItem 构造一个带当前时间的 Item。
func NewItem(text string) Item {
	return Item{Ts: time.Now(), Text: text}
}

// 批量切分阈值：以"行数"或"累计字节数"任一楼限触发切批，防止单批过大撑爆内存。
const (
	BatchMaxItems = 1024 * 4  // 单批最多行数
	BatchMaxBytes = 64 * 1024 // 单批最多累计字节数（约 64KB）
)

// Batch 是 `[]Item` 的类型别名，标注"一批日志"语义，便于为批量补充方法。
// 传输单元固定为 Batch（即 []Item）。
type Batch = []Item

// LazyTime 一次取样，供同批内的多个 Item 共享时间戳，避免逐行调用 time.Now()。
func LazyTime() time.Time { return time.Now() }

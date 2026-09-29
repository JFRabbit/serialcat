package dag

import "context"

// Node 是一个处理单元。MVP 支持两类：
//   - source：无入边，Run 里生产 Item 并发射到 Out
//   - node（process/sink）：从 in 读，处理，可选写 Out
//
// 传输单元固定为 []Item（批量）。批量是"内部搬运手段"，节点的业务逻辑
// 仍按 Item 逐个处理；批量可减少 channel 往返、降低分配，显著提升吞吐。
type Node interface {
	// Name 返回节点 id（config 中唯一）。
	Name() string
	// Run 运行主逻辑，直到 in 关闭（源结束）或 ctx 结束/出错。
	// in 对 source 型节点为 nil。
	Run(ctx context.Context, in <-chan []Item) error
}

// SourceNode 是无入边的节点（文件/串口/stdin）。
type SourceNode interface {
	Node
	// Out 返回节点用于发射 Item 批次的输出 channel，由引擎消费。
	Out() <-chan []Item
}

// ProcessNode 是普通节点（过滤/加工/输出）。sink 型无输出，Out 返回 nil,false。
type ProcessNode interface {
	Node
	// Out 返回节点的输出 channel（每个值为一批 []Item）；sink 型返回 nil,false。
	Out() (chan []Item, bool)
}

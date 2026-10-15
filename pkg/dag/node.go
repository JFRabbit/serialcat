package dag

import "context"

// Node is a processing unit. The MVP supports two kinds:
//   - source: no incoming edges, produces Items in Run and emits them to Out
//   - node (process/sink): reads from in, processes, optionally writes to Out
//
// The transport unit is fixed to []Item (batch). Batching is an "internal
// moving mechanism": node business logic still processes Items one by one;
// batching reduces channel round-trips and allocations, significantly boosting throughput.
type Node interface {
	// Name returns the node id (unique in config).
	Name() string
	// Run runs the main logic until in closes (source ends) or ctx ends/errors.
	// in is nil for source-type nodes.
	Run(ctx context.Context, in <-chan []Item) error
}

// SourceNode is a node without incoming edges (file/serial/stdin).
type SourceNode interface {
	Node
	// Out returns the output channel the node emits Item batches to, consumed by the engine.
	Out() <-chan []Item
}

// ProcessNode is a regular node (filter/process/output). Sink-type nodes have no output and Out returns nil,false.
type ProcessNode interface {
	Node
	// Out returns the node's output channel (each value is a batch of []Item); sink-type nodes return nil,false.
	Out() (chan []Item, bool)
}

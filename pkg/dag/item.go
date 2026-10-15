package dag

import "time"

// Item is a record transmitted over an edge. It is an indivisible log unit.
type Item struct {
	Ts   time.Time         // record time (falls back to arrival time when not sampled)
	Text string            // raw line text
	Src  string            // source node id
	Meta map[string]string // optional key-values filled by processing nodes
}

// NewItem creates an Item with the current time.
func NewItem(text string) Item {
	return Item{Ts: time.Now(), Text: text}
}

// Batch split thresholds: splitting is triggered by either "line count" or
// "accumulated byte count", preventing a single batch from blowing up memory.
const (
	BatchMaxItems = 1024 * 4  // max lines per batch
	BatchMaxBytes = 64 * 1024 // max accumulated bytes per batch (≈64KB)
)

// Batch is a type alias for `[]Item`, marking "a batch of logs" semantics
// so methods can be added to the batch later.
// The transport unit is fixed to Batch (i.e. []Item).
type Batch = []Item

// LazyTime samples the time once so multiple Items in the same batch share a
// timestamp, avoiding a time.Now() call per line.
func LazyTime() time.Time { return time.Now() }

package nodes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JFRabbit/serialcat/pkg/dag"
	"github.com/JFRabbit/serialcat/pkg/serial"
)

// LocalSerial is a source node: reads lines from the local serial port → splits
// into batches and emits them, plugging into the same batched DAG as the upstream.
// The physical serial capability is provided by pkg/serial (Windows only);
// opening a port on other platforms returns a clear "Windows only" error.
type LocalSerial struct {
	id   string
	port string
	baud int
	out  chan []dag.Item
	// onOpen is optional: a callback after the port opens successfully, letting the upper layer (run)
	// inject a downstream write capability that reuses the same handle to avoid repeated Open conflicts.
	onOpen func(ctx context.Context, p io.ReadWriteCloser)
}

var _ dag.SourceNode = (*LocalSerial)(nil)

// SetOnOpen registers the callback invoked after the serial port opens (usually for a local write HTTP channel).
func (n *LocalSerial) SetOnOpen(f func(ctx context.Context, p io.ReadWriteCloser)) { n.onOpen = f }

// NewLocalSerial builds a local serial source. baud<=0 defaults to 115200; buf<=0 defaults to a 128-batch buffer.
func NewLocalSerial(id, port string, baud, buf int) *LocalSerial {
	if buf <= 0 {
		buf = 128
	}
	if baud <= 0 {
		baud = 115200
	}
	return &LocalSerial{id: id, port: port, baud: baud, out: make(chan []dag.Item, buf)}
}

// Name implements dag.Node.
func (n *LocalSerial) Name() string { return n.id }

// Out implements dag.SourceNode.
func (n *LocalSerial) Out() <-chan []dag.Item { return n.out }

// Run opens the local serial port and emits in batches. The serial port is a
// continuous source: a read error other than EOF is returned as an exception (not a normal EOS).
// in is nil for sources.
func (n *LocalSerial) Run(ctx context.Context, _ <-chan []dag.Item) error {
	drv, err := serial.NewDriver()
	if err != nil {
		return err
	}
	p, err := drv.Open(n.port, n.baud)
	if err != nil {
		return fmt.Errorf("open serial port %s@%d: %w", n.port, n.baud, err)
	}
	defer p.Close()
	defer close(n.out) // on exit, close Out (announcing EOS or an exception to the engine)

	// After the port opens, notify the upper layer (e.g. the run command's local write channel),
	// reusing the same handle to send commands.
	if n.onOpen != nil {
		n.onOpen(ctx, p)
	}

	br := bufio.NewReader(p)

	var batch []dag.Item
	bytes := 0

	// flush emits the current partial batch (if non-empty).
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		select {
		case n.out <- batch:
			batch = nil
			bytes = 0
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Reading runs in a separate goroutine: avoids being unable to flush partial batches
	// or listen for cancellation while blocked on a read.
	lines := make(chan string, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				lines <- strings.TrimRight(line, "\r\n")
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	tick := time.NewTicker(flushIdle)
	defer tick.Stop()
	for {
		select {
		case line := <-lines:
			batch = append(batch, dag.Item{Ts: time.Now(), Text: line})
			bytes += len(line)
			if len(batch) >= dag.BatchMaxItems || bytes >= dag.BatchMaxBytes {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-tick.C:
			// Low-rate/quiet stream: periodically flush the partial batch to reduce end-to-end latency.
			if err := flush(); err != nil {
				return err
			}
		case err := <-readErr:
			if ferr := flush(); ferr != nil {
				return ferr
			}
			if err == io.EOF {
				return nil // serial port closed
			}
			return fmt.Errorf("serial read interrupted: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

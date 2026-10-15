package nodes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/JFRabbit/serialcat/internal/logger"
	"github.com/JFRabbit/serialcat/pkg/dag"
)

// Process is a process node: hands upstream batches ([]Item) to an external
// subprocess, and translates the result back into []Item batches for the downstream.
// It acts as a "subprocess communication shell": it makes no business decisions;
// the concrete logic is carried by the external script (command+args).
//
// The communication protocol is batched NDJSON:
//   - Send (Go→script, stdin): one JSON array per batch (elements are {ts,text,src,meta}), one array per line.
//   - Receive (script→Go, stdout): the script outputs one JSON array per line, each decoded back into a batch of []Item.
//   - The script may add/delete/modify Items; returning an empty array drops the batch.
//   - stderr is passed through verbatim to the log (for debugging), without affecting the data flow.
type Process struct {
	id      string
	command string
	args    []string
	out     chan []dag.Item
}

var _ dag.ProcessNode = (*Process)(nil)

// NewProcess builds a subprocess processing node. buf<=0 falls back to the default buffer (128 batches).
func NewProcess(id, command string, args []string, buf int) *Process {
	if buf <= 0 {
		buf = 128
	}
	return &Process{
		id:      id,
		command: command,
		args:    args,
		out:     make(chan []dag.Item, buf),
	}
}

// Name implements dag.Node.
func (n *Process) Name() string { return n.id }

// Out implements dag.ProcessNode.
func (n *Process) Out() (chan []dag.Item, bool) { return n.out, true }

// Run starts the subprocess and loops "write stdin batches / read stdout batches"
// until in closes (EOS).
//
// Concurrency model: writing and reading each use a goroutine, joined via out, avoiding batch read/write interlock.
//   - in closes → close(stdin) → writer goroutine ends; the reader goroutine ends on stdout EOF and closes out.
//   - Any error (start failure / mid-run exit / decode failure / ctx cancel) → report the error and close out.
func (n *Process) Run(ctx context.Context, in <-chan []dag.Item) error {
	cmd := exec.CommandContext(ctx, n.command, n.args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("node %s: failed to get stdin: %w", n.id, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("node %s: failed to get stdout: %w", n.id, err)
	}
	// stderr passthrough: spawn a goroutine to read it in a loop, preventing the script from blocking itself by filling the stderr buffer.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("node %s: failed to get stderr: %w", n.id, err)
	}
	var stderrWg sync.WaitGroup
	stderrWg.Add(1)
	go func() {
		defer stderrWg.Done()
		_ = copyToLog(stderr, n.id)
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("node %s: failed to start subprocess %q: %w", n.id, n.command, err)
	}

	// Mutex on whether out has been closed, preventing double close at compile time.
	var closeOnce sync.Once
	closeOut := func() { closeOnce.Do(func() { close(n.out) }) }

	// ---- writer goroutine: in batches → stdin ----
	writeErr := make(chan error, 1)
	go func() {
		defer func() {
			// After in closes (EOS), close the subprocess stdin to signal "input ended".
			_ = stdin.Close()
		}()
		enc := json.NewEncoder(stdin)
		for batch := range in {
			if err := enc.Encode(batchToFrameSlice(batch)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	// ---- reader goroutine: stdout batches → out ----
	readErr := make(chan error, 1)
	go func() {
		defer closeOut()
		dec := json.NewDecoder(bufio.NewReader(stdout))
		for {
			var frames []frame
			if err := dec.Decode(&frames); err != nil {
				if err == io.EOF {
					readErr <- nil
					return
				}
				readErr <- fmt.Errorf("failed to parse script output: %w", err)
				return
			}
			if len(frames) == 0 {
				continue // empty batch = drop, not sent downstream
			}
			batch := make([]dag.Item, 0, len(frames))
			for _, f := range frames {
				batch = append(batch, f.item())
			}
			select {
			case n.out <- batch:
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
	}()

	waitC := make(chan error, 1)
	go func() { waitC <- cmd.Wait() }()

	// Join: wait for the writer to finish (in fully consumed), then read until EOF, then wait.
	// Order: wait for writeErr → close stdin (done by the writer goroutine's defer) → wait for stdout EOF → wait.
	if werr := <-writeErr; werr != nil {
		_ = cmd.Process.Kill()
		closeOut()
		return fmt.Errorf("node %s: failed to write to subprocess: %w", n.id, werr)
	}

	// After the writer ends normally, wait for the reader to finish; then reap the process.
	var rerr error
	select {
	case rerr = <-readErr:
	case <-ctx.Done():
		rerr = ctx.Err()
	}
	stderrWg.Wait()
	_ = <-waitC // ignore the 0 exit code (normal EOS end); abnormal exits are reported by cmd.Wait's error below
	if rerr != nil {
		return fmt.Errorf("node %s: %w", n.id, rerr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// batchToFrameSlice converts a batch of Items into a serializable frame slice (one NDJSON line per batch).
func batchToFrameSlice(batch []dag.Item) []frame {
	frames := make([]frame, 0, len(batch))
	for _, it := range batch {
		frames = append(frames, frame{Ts: it.Ts, Text: it.Text})
	}
	return frames
}

// copyToLog copies r line by line to the log (stderr passthrough), for debugging.
func copyToLog(r io.Reader, node string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		logger.Warn("node %s script stderr: %s", node, sc.Text())
	}
	return sc.Err()
}

// frame is the record structure exchanged with the subprocess; its fields align
// with the fields of dag.Item that need to be passed out.
// It is reused for both serialization and deserialization.
type frame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

func (f frame) item() dag.Item {
	return dag.Item{Ts: f.Ts, Text: f.Text}
}
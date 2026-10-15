package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/JFRabbit/serialcat/pkg/dag"
)

// remoteFrame corresponds to a single serial record streamed by the remote agent
// (NDJSON). Its fields align with the internal agent.
type remoteFrame struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// SerialRemote is a source node: pulls serial lines from the remote
// "serialcat agent" service's /api/stream, wraps them into dag.Items and emits
// them in batches, plugging into the same batched DAG as the upstream.
// This node is pure HTTP and cross-platform; the physical serial capability is
// provided by the agent running on Windows.
type SerialRemote struct {
	id     string
	base   string
	out    chan []dag.Item
	client *http.Client
}

var _ dag.SourceNode = (*SerialRemote)(nil)

// NewSerialRemote builds a remote serial source node. baseURL looks like
// http://192.168.1.10:7100 or with a trailing slash;
// a missing scheme (e.g. 192.168.1.10:7100) is auto-prefixed with http://.
func NewSerialRemote(id, baseURL string, buf int) *SerialRemote {
	if buf <= 0 {
		buf = 128
	}
	base := strings.TrimSpace(baseURL)
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &SerialRemote{
		id:     id,
		base:   base,
		out:    make(chan []dag.Item, buf),
		client: &http.Client{Timeout: 0}, // streaming long connection, no overall timeout
	}
}

// Name implements dag.Node.
func (n *SerialRemote) Name() string { return n.id }

// Out implements dag.SourceNode.
func (n *SerialRemote) Out() <-chan []dag.Item { return n.out }

// Run connects to the remote stream and emits in batches. Disconnections are
// auto-reconnected: each connection consumes one round of reads; a broken
// stream is not a DAG failure, but marks a "gap" and backs off before reconnecting.
// It only ends on ctx cancel or a permanent error.
// in is nil for sources.
func (n *SerialRemote) Run(ctx context.Context, _ <-chan []dag.Item) error {
	defer close(n.out)

	backoff := backoffBase
	for {
		body, perm, derr := n.dialStream(ctx)
		if derr != nil {
			if perm {
				return derr // permanent error (e.g. 4xx/URL misconfigured): no retry, let the user notice quickly
			}
			if !sleepWithBackoff(ctx, &backoff) {
				return ctx.Err()
			}
			continue
		}

		backoff = backoffBase // reset backoff after a successful connection
		_ = n.readLoop(ctx, body)
		_ = body.Close()
		if ctx.Err() != nil {
			return ctx.Err() // user-initiated cancel/exit, normal end
		}
		// Stream broken: first insert a "gap" marker into the output, then back off
		// and reconnect, staying continuous rather than silent.
		if gerr := n.emitGap(ctx); gerr != nil {
			return gerr
		}
		if !sleepWithBackoff(ctx, &backoff) {
			return ctx.Err()
		}
	}
}

// readLoop consumes one established remote stream connection: decode → split into
// batches → emit, flushing partial batches every 100ms.
// Returns nil for a normal ctx-cancel end; returns err for a broken stream (reconnected by the outer layer).
func (n *SerialRemote) readLoop(ctx context.Context, body io.Reader) error {
	dec := json.NewDecoder(body)
	frames := make(chan remoteFrame, 64)
	decErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		var f remoteFrame
		for {
			if err := dec.Decode(&f); err != nil {
				select {
				case decErr <- err:
				case <-done:
				}
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()

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

	tick := time.NewTicker(flushIdle)
	defer tick.Stop()
	for {
		select {
		case f := <-frames:
			// The timestamp is the value sent by the agent.
			batch = append(batch, dag.Item{Ts: f.Ts, Text: f.Text})
			bytes += len(f.Text)
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
		case err := <-decErr:
			// Flush the remaining partial batch as best effort, then report the break
			// (a "disconnect" of a continuous source is an exception, but not the end; the outer layer reconnects).
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// emitGap inserts a gap-marker item into the output, letting downstream
// (console/disk) explicitly see the gap.
func (n *SerialRemote) emitGap(ctx context.Context) error {
	gap := dag.Item{
		Ts:   time.Now(),
		Text: "...... serial stream interrupted, gap here ......",
		Meta: map[string]string{"gap": "break"},
	}
	select {
	case n.out <- []dag.Item{gap}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flushIdle is the periodic partial-batch flush interval for low-rate serial streams.
const flushIdle = 100 * time.Millisecond

// Reconnect backoff: starts at 500ms, grows exponentially capped at 30s,
// with random jitter each time to avoid multi-endpoint thundering herd.
const (
	backoffBase = 500 * time.Millisecond
	backoffMax  = 30 * time.Second
)

// sleepWithBackoff waits one backoff interval (with jitter) and doubles backoff.
// Returns false if ctx has been cancelled.
func sleepWithBackoff(ctx context.Context, backoff *time.Duration) bool {
	jitter := time.Duration(rand.Int63n(int64(*backoff) / 2))
	delay := *backoff + jitter
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
	}
	*backoff *= 2
	if *backoff > backoffMax {
		*backoff = backoffMax
	}
	return true
}

// dialStream makes one request to /api/stream and returns the response body.
// perm is true for permanent errors (4xx: URL misconfigured/auth failure) which
// must not be retried; anything else is treated as a transient reconnectable error.
func (n *SerialRemote) dialStream(ctx context.Context) (io.ReadCloser, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/api/stream", nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, false, err // network error: retryable
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, resp.StatusCode < 500, fmt.Errorf("remote serial agent /api/stream returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, false, nil
}

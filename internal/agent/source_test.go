package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipePort is an in-memory port with preset line data: after reading all data it blocks until Close triggers an error exit.
// With eofOnEmpty it returns io.EOF once data is exhausted (used for the Run normal-exit scenario).
type pipePort struct {
	mu         sync.Mutex
	data       []byte
	eofOnEmpty bool
	done       chan struct{}
	once       sync.Once
}

func (p *pipePort) Read(b []byte) (int, error) {
	p.mu.Lock()
	if len(p.data) > 0 {
		n := copy(b, p.data)
		p.data = p.data[n:]
		p.mu.Unlock()
		return n, nil
	}
	p.mu.Unlock()
	if p.eofOnEmpty {
		return 0, io.EOF
	}
	<-p.done
	return 0, errors.New("closed")
}

func (p *pipePort) Write(b []byte) (int, error) { return len(b), nil }

func (p *pipePort) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// Broadcasting by line: the same frame should reach multiple subscribers simultaneously, and trailing \r\n is trimmed.
func TestSerialSourceBroadcastToSubscribers(t *testing.T) {
	data := "line1\nline2\r\nline3\n"
	src := NewSerialSource(&pipePort{data: []byte(data), done: make(chan struct{})})
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	ch1, err := src.Subscribe(ctx1)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	ch2, err := src.Subscribe(ctx2)
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	// After the frames are read, the port still blocks; use ctx cancel to make Run exit (covers the ctx.Err() branch).
	rctx, rcancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(rctx) }()

	want := []string{"line1", "line2", "line3"}
	for i, wantText := range want {
		for name, ch := range map[string]<-chan Frame{"ch1": ch1, "ch2": ch2} {
			select {
			case f, ok := <-ch:
				if !ok {
					t.Fatalf("frame %d %s channel closed", i, name)
				}
				if f.Text != wantText {
					t.Errorf("frame %d %s = %q, want %q", i, name, f.Text, wantText)
				}
				if f.Ts.IsZero() {
					t.Errorf("frame %d %s timestamp is zero", i, name)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("frame %d %s timed out waiting", i, name)
			}
		}
	}

	rcancel()
	// After the frames are read, Read blocks on the empty port; Close unblocks it. Since ctx is canceled, Run should return ctx.Err().
	_ = src.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

// Backpressure isolation: when a slow subscriber does not read, frames are dropped without slowing down broadcasting or other subscribers.
func TestSerialSourceSlowSubscriberDropsFrames(t *testing.T) {
	n := 600 // exceeds the subscriber buffer of 512
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("x\n")
	}
	src := NewSerialSource(&pipePort{data: []byte(sb.String()), done: make(chan struct{}), eofOnEmpty: true})

	fastCtx, fastCancel := context.WithCancel(context.Background())
	defer fastCancel()
	slowCtx, slowCancel := context.WithCancel(context.Background())
	defer slowCancel()

	fast, _ := src.Subscribe(fastCtx)
	slow, _ := src.Subscribe(slowCtx)

	// The fast subscriber reads while receiving (concurrently draining) and should get all frames;
	// the slow subscriber does not read, so once the buffer of 512 fills up the remaining frames are dropped.
	fastCountCh := make(chan int, 1)
	go func() {
		c := 0
		for range fast {
			c++
		}
		fastCountCh <- c
	}()

	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background()) }()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// After Run ends (EOF), the channel is not closed by itself (only ctx cancel closes it); count frames after unsubscribing.
	fastCancel()
	slowCancel()

	if fastCount := <-fastCountCh; fastCount != n {
		t.Errorf("fast subscriber received %d frames, want %d", fastCount, n)
	}

	// Frames are dropped once the slow subscriber's buffer is full: fewer than the total (drop semantics active).
	slowCount := 0
	for range slow {
		slowCount++
	}
	if slowCount != 512 {
		t.Errorf("slow subscriber received %d frames, want buffer limit 512 (rest dropped)", slowCount)
	}
}

// ctx cancel: the subscriber is automatically unsubscribed and its channel is closed.
func TestSerialSourceUnsubscribeOnCtxCancel(t *testing.T) {
	src := NewSerialSource(&pipePort{data: []byte("hello\n"), done: make(chan struct{})})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := src.Subscribe(ctx)

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel still readable after ctx cancel, want closed")
		}
	case <-time.After(2 * time.Second):
		t.Error("channel not closed within the expected time after ctx cancel")
	}

	// After unsubscribe, broadcasting should neither block nor panic.
	src.mu.Lock()
	if len(src.subs) != 0 {
		t.Errorf("subscription table still has %d subscribers after unsubscribe", len(src.subs))
	}
	src.mu.Unlock()
	src.broadcast(Frame{Text: "orphan"})
}

// EOF semantics: Run returns nil when the serial port returns io.EOF after reading all data.
func TestSerialSourceRunEOFReturnsNil(t *testing.T) {
	// strings.Reader returns io.EOF once fully read
	src := NewSerialSource(&struct {
		io.Reader
		io.Writer
		io.Closer
	}{Reader: strings.NewReader("only\n"), Writer: &strings.Builder{}, Closer: io.NopCloser(strings.NewReader(""))})
	if err := src.Run(context.Background()); err != nil {
		t.Fatalf("Run should return nil, got: %v", err)
	}
}

// Close semantics: after closing the underlying port, Read errors and Run returns the underlying error (not EOF).
func TestSerialSourceRunCloseError(t *testing.T) {
	p := &pipePort{data: []byte("partial"), done: make(chan struct{})} // no newline; blocks after reading
	src := NewSerialSource(p)
	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background()) }()

	if err := src.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run should return an error after the port is closed")
		}
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want the underlying read error rather than EOF/ctx cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after the port was closed")
	}
}

// Run returns ctx.Err() on ctx cancel: cancel then close the port to unblock Read;
// error handling checks ctx first (in source.go the ctx.Err() branch takes priority over the underlying error).
func TestSerialSourceRunCtxCancel(t *testing.T) {
	p := &pipePort{data: []byte("stuck"), done: make(chan struct{})}
	defer p.Close()
	src := NewSerialSource(p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()

	cancel()
	time.Sleep(50 * time.Millisecond) // ensure cancel arrives before the port error
	_ = src.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after ctx cancel")
	}
}

// Write/Close pass-through semantics.
func TestSerialSourceWriteAndClose(t *testing.T) {
	p := &pipePort{data: []byte("x\n"), done: make(chan struct{})}
	src := NewSerialSource(p)
	if n, err := src.Write([]byte("cmd")); err != nil || n != 3 {
		t.Errorf("Write returned (%d, %v), want (3, nil)", n, err)
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Errorf("repeated Close should be idempotent, got: %v", err)
	}
}

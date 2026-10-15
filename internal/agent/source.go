package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/JFRabbit/serialcat/internal/logger"
)

// SerialSource is a serial line source: a single background goroutine reads the serial port and broadcasts lines to subscribers.
// Frames are dropped when subscribers have no buffer (backpressure isolation: serial reading is never slowed down by slow subscribers).
type SerialSource struct {
	port io.ReadWriteCloser
	mu   sync.Mutex
	subs map[chan Frame]struct{}
}

// NewSerialSource builds a line source from an already-opened serial port.
func NewSerialSource(p io.ReadWriteCloser) *SerialSource {
	return &SerialSource{port: p, subs: map[chan Frame]struct{}{}}
}

// Subscribe registers a subscriber; when ctx is canceled the subscription is removed and the returned channel is closed.
func (s *SerialSource) Subscribe(ctx context.Context) (<-chan Frame, error) {
	ch := make(chan Frame, 512)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subs, ch)
		close(ch)
		s.mu.Unlock()
	}()
	return ch, nil
}

// Run blocks reading the serial port and broadcasting by line, until ctx is canceled or the port errors/closes.
func (s *SerialSource) Run(ctx context.Context) error {
	br := bufio.NewReader(s.port)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			s.broadcast(Frame{Ts: time.Now(), Text: strings.TrimRight(line, "\r\n")})
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				logger.Info("serial port reached EOF, port closed")
				return nil
			}
			return err
		}
	}
}

// Write passes downstream commands through to the serial port (TX phase reserved).
func (s *SerialSource) Write(b []byte) (int, error) { return s.port.Write(b) }

// Close closes the underlying serial port.
func (s *SerialSource) Close() error { return s.port.Close() }

func (s *SerialSource) broadcast(f Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- f:
		default: // drop frames when a subscriber is too slow to avoid slowing down serial reading (backpressure)
		}
	}
}

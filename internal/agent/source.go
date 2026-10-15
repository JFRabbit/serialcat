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

// SerialSource 串口行源：后台单协程读串口，按行广播给订阅者。
// 订阅者无缓冲时丢弃该帧（背压隔离：串口读取永不因慢订阅者被拖缓）。
type SerialSource struct {
	port io.ReadWriteCloser
	mu   sync.Mutex
	subs map[chan Frame]struct{}
}

// NewSerialSource 用已打开的串口构造行源。
func NewSerialSource(p io.ReadWriteCloser) *SerialSource {
	return &SerialSource{port: p, subs: map[chan Frame]struct{}{}}
}

// Subscribe 注册一个订阅者；ctx 取消时自动退订并关闭返回的 channel。
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

// Run 阻塞读取串口并按行广播，直到 ctx 取消或串口出错/关闭。
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
				logger.Info("串口达到 EOF，串口已关闭")
				return nil
			}
			return err
		}
	}
}

// Write 透传下行命令到串口（阶段② TX 预留）。
func (s *SerialSource) Write(b []byte) (int, error) { return s.port.Write(b) }

// Close 关闭底层串口。
func (s *SerialSource) Close() error { return s.port.Close() }

func (s *SerialSource) broadcast(f Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- f:
		default: // 订阅者太慢则丢弃，避免拖慢串口读取（背压）
		}
	}
}

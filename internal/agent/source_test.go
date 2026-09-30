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

// pipePort 预置行数据的内存端口：读完后持续阻塞，直到 Close 触发错误退出。
// eofOnEmpty 时读尽数据直接返回 io.EOF（用于 Run 正常收尾的场景）。
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

// 按行广播：同一帧应同时送达多个订阅者，且行尾 \r\n 被裁剪。
func TestSerialSourceBroadcastToSubscribers(t *testing.T) {
	data := "line1\nline2\r\nline3\n"
	src := NewSerialSource(&pipePort{data: []byte(data), done: make(chan struct{})})
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	ch1, err := src.Subscribe(ctx1)
	if err != nil {
		t.Fatalf("Subscribe 返回错误: %v", err)
	}
	ch2, err := src.Subscribe(ctx2)
	if err != nil {
		t.Fatalf("Subscribe 返回错误: %v", err)
	}

	// 帧读完后串口仍阻塞，用 ctx 取消让 Run 退出（覆盖 ctx.Err() 分支）
	rctx, rcancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(rctx) }()

	want := []string{"line1", "line2", "line3"}
	for i, wantText := range want {
		for name, ch := range map[string]<-chan Frame{"ch1": ch1, "ch2": ch2} {
			select {
			case f, ok := <-ch:
				if !ok {
					t.Fatalf("帧 %d %s 通道已关闭", i, name)
				}
				if f.Text != wantText {
					t.Errorf("帧 %d %s = %q, 期望 %q", i, name, f.Text, wantText)
				}
				if f.Ts.IsZero() {
					t.Errorf("帧 %d %s 时间戳为零", i, name)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("帧 %d %s 超时未收到", i, name)
			}
		}
	}

	rcancel()
	// 帧读完后 Read 阻塞在空端口上，Close 解除阻塞；此时 ctx 已取消，Run 应返回 ctx.Err()
	_ = src.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run 返回 %v, 期望 context.Canceled", err)
	}
}

// 背压隔离：慢订阅者不读取时，帧被丢弃但不拖慢广播与其他订阅者。
func TestSerialSourceSlowSubscriberDropsFrames(t *testing.T) {
	n := 600 // 超过订阅者缓冲 512
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

	// 快订阅者边收边读（并发排空），应拿到全部帧；
	// 慢订阅者不读，缓冲满 512 后其余帧被丢弃。
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
		t.Fatalf("Run 返回错误: %v", err)
	}

	// Run 结束（EOF）后通道不会自行关闭（只有 ctx 取消才关闭），退订后统计帧数。
	fastCancel()
	slowCancel()

	if fastCount := <-fastCountCh; fastCount != n {
		t.Errorf("快订阅者收到 %d 帧, 期望 %d", fastCount, n)
	}

	// 慢订阅者缓冲满后丢帧：应少于总数（丢帧语义生效）
	slowCount := 0
	for range slow {
		slowCount++
	}
	if slowCount != 512 {
		t.Errorf("慢订阅者收到 %d 帧, 期望缓冲上限 512（其余被丢弃）", slowCount)
	}
}

// ctx 取消：订阅者自动退订且通道被关闭。
func TestSerialSourceUnsubscribeOnCtxCancel(t *testing.T) {
	src := NewSerialSource(&pipePort{data: []byte("hello\n"), done: make(chan struct{})})
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := src.Subscribe(ctx)

	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("ctx 取消后通道仍可读，期望已关闭")
		}
	case <-time.After(2 * time.Second):
		t.Error("ctx 取消后通道未在预期时间内关闭")
	}

	// 退订后广播不应阻塞也不应 panic
	src.mu.Lock()
	if len(src.subs) != 0 {
		t.Errorf("退订后订阅表残留 %d 个订阅者", len(src.subs))
	}
	src.mu.Unlock()
	src.broadcast(Frame{Text: "orphan"})
}

// EOF 语义：串口读尽返回 io.EOF 时 Run 正常返回 nil。
func TestSerialSourceRunEOFReturnsNil(t *testing.T) {
	// strings.Reader 读完即返回 io.EOF
	src := NewSerialSource(&struct {
		io.Reader
		io.Writer
		io.Closer
	}{Reader: strings.NewReader("only\n"), Writer: &strings.Builder{}, Closer: io.NopCloser(strings.NewReader(""))})
	if err := src.Run(context.Background()); err != nil {
		t.Fatalf("Run 应返回 nil, 得到: %v", err)
	}
}

// Close 语义：Close 底层端口后 Read 出错，Run 返回底层错误（非 EOF）。
func TestSerialSourceRunCloseError(t *testing.T) {
	p := &pipePort{data: []byte("partial"), done: make(chan struct{})} // 无换行,读完即阻塞
	src := NewSerialSource(p)
	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background()) }()

	if err := src.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("串口关闭后 Run 应返回错误")
		}
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			t.Errorf("Run 返回 %v, 期望底层读错误而非 EOF/ctx 取消", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run 在端口关闭后未退出")
	}
}

// ctx 取消时 Run 返回 ctx.Err()：取消后关闭端口解除 Read 阻塞，
// 错误处理优先检查 ctx（source.go 中 ctx.Err() 分支优先于底层错误）。
func TestSerialSourceRunCtxCancel(t *testing.T) {
	p := &pipePort{data: []byte("stuck"), done: make(chan struct{})}
	defer p.Close()
	src := NewSerialSource(p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()

	cancel()
	time.Sleep(50 * time.Millisecond) // 确保取消先于端口错误到达
	_ = src.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run 返回 %v, 期望 context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("ctx 取消后 Run 未退出")
	}
}

// Write/Close 透传语义。
func TestSerialSourceWriteAndClose(t *testing.T) {
	p := &pipePort{data: []byte("x\n"), done: make(chan struct{})}
	src := NewSerialSource(p)
	if n, err := src.Write([]byte("cmd")); err != nil || n != 3 {
		t.Errorf("Write 返回 (%d, %v), 期望 (3, nil)", n, err)
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close 返回错误: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Errorf("重复 Close 应幂等, 得到: %v", err)
	}
}

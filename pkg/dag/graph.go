package dag

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// defaultBufSize 是每条边默认缓冲的批次数（背压）。
// 传输单元为一批 []Item（单批可达 BatchMaxBytes≈64KB），因此缓冲按批计数而非按行，
// 值不宜过大，否则会在内存中积压大量日志。
const defaultBufSize = 128

// Edge 描述一条有向连接：from 的输出送往 to。
type Edge struct {
	From string
	To   string
	Buf  int
}

// Graph 是一个由 Node 组成的 DAG。
type Graph struct {
	Nodes map[string]Node
	Edges []Edge
}

// NewGraph 构造空图。
func NewGraph() *Graph { return &Graph{Nodes: make(map[string]Node)} }

// AddNode 添加节点。
func (g *Graph) AddNode(n Node) { g.Nodes[n.Name()] = n }

// AddEdge 添加 from→to 连边；Buf<=0 时用默认值。
func (g *Graph) AddEdge(from, to string, buf int) {
	g.Edges = append(g.Edges, Edge{From: from, To: to, Buf: buf})
}

// NodeFactory 由 config 构建指定节点。
type NodeFactory func(name, kind string, cfg map[string]any) (Node, error)

// Run 执行整个 DAG：
//  1. 为每个下游(to)建缓冲 channel（扇入合并的写入端）。
//  2. 启动每个节点自身：source 无入边，接收到 nil；其余读各自缓冲。
//  3. 每个上游(from)一个扇出 goroutine：读取其 Out，广播复制到所有下游。
//  4. EOS 传播：上游 Out 关闭 → 该 writer 结束 → 下游计数 "-1" → 归零关闭下游缓冲。
//     下游节点读到缓冲关闭即结束，完成自身的 Flush。
func (g *Graph) Run(actx context.Context) error {
	var (
		errMu  sync.Mutex
		runErr error
	)
	fail := func(e error) {
		errMu.Lock()
		if runErr == nil {
			runErr = e
		}
		errMu.Unlock()
	}
	ctx, cancel := context.WithCancel(actx)
	defer cancel()

	// 1. 建下游缓冲 + 记录有效上游输出。
	outs := map[string]<-chan []Item{} // from -> out（仅有效输出节点）
	for id, n := range g.Nodes {
		if s, ok := n.(SourceNode); ok {
			outs[id] = s.Out()
		} else if p, ok := n.(ProcessNode); ok {
			if c, has := p.Out(); has {
				outs[id] = c
			}
		}
	}

	buffers := map[string]chan []Item{} // to -> 缓冲（元素为一批 []Item）
	writeCount := map[string]*int32{}   // to -> 剩余 writer 数（扇入合并）
	closeOnce := map[string]*sync.Once{}

	for _, e := range g.Edges {
		if _, has := outs[e.From]; !has {
			continue // 来源不是有效输出节点，跳过
		}
		if ch, ok := buffers[e.To]; ok {
			// 已建过
			_ = ch
		} else {
			buf := e.Buf
			if buf <= 0 {
				buf = defaultBufSize
			}
			buffers[e.To] = make(chan []Item, buf)
		}
		if _, ok := writeCount[e.To]; !ok {
			n := int32(0)
			writeCount[e.To] = &n
			closeOnce[e.To] = &sync.Once{}
		}
		*writeCount[e.To]++ // fan_in writer 计数
	}

	// 2. 启动节点自身。
	var wg sync.WaitGroup
	for id, n := range g.Nodes {
		nn := n
		var in <-chan []Item
		if c, ok := buffers[id]; ok {
			in = c
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := nn.Run(ctx, in); e != nil {
				fail(fmt.Errorf("node %s: %w", id, e))
				cancel()
			}
		}()
	}

	// 3. 扇出 + EOS 传播。
	// 下游 group：from -> []to
	deps := map[string][]string{}
	for _, e := range g.Edges {
		if _, has := outs[e.From]; !has {
			continue
		}
		deps[e.From] = append(deps[e.From], e.To)
	}
	for from, out := range outs {
		if len(deps[from]) == 0 {
			continue
		}
		down := make([]chan<- []Item, 0, len(deps[from]))
		for _, to := range deps[from] {
			down = append(down, buffers[to])
		}
		f, o := from, out
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				// 该上游结束：每个下游计数 -1
				for _, to := range deps[f] {
					if cnt, ok := writeCount[to]; ok {
						if atomic.AddInt32(cnt, -1) == 0 {
							closeOnce[to].Do(func() { close(buffers[to]) })
						}
					}
				}
			}()
			for {
				select {
				case batch, ok := <-o:
					if !ok {
						return // 上游 Out 关闭 = EOS
					}
					for _, dch := range down {
						select {
						case dch <- batch:
						case <-ctx.Done():
							return
						}
					}
				case <-ctx.Done():
					// 上游因错误/取消而终止（未关闭 Out）：同样结束，避免 wg.Wait 悬挂。
					return
				}
			}
		}()
	}

	wg.Wait()

	if runErr != nil {
		return runErr
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return nil
}

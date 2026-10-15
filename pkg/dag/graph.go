package dag

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// defaultBufSize is the default number of batches buffered per edge (backpressure).
// The transport unit is a batch of []Item (a single batch can reach BatchMaxBytes≈64KB),
// so the buffer is counted in batches rather than lines; keep the value small,
// otherwise a large amount of logs would pile up in memory.
const defaultBufSize = 128

// Edge describes a directed connection: the output of From is sent to To.
type Edge struct {
	From string
	To   string
	Buf  int
}

// Graph is a DAG composed of Nodes.
type Graph struct {
	Nodes map[string]Node
	Edges []Edge
}

// NewGraph creates an empty graph.
func NewGraph() *Graph { return &Graph{Nodes: make(map[string]Node)} }

// AddNode adds a node.
func (g *Graph) AddNode(n Node) { g.Nodes[n.Name()] = n }

// AddEdge adds a from→to edge; Buf<=0 falls back to the default value.
func (g *Graph) AddEdge(from, to string, buf int) {
	g.Edges = append(g.Edges, Edge{From: from, To: to, Buf: buf})
}

// NodeFactory builds a node of the given kind from config.
type NodeFactory func(name, kind string, cfg map[string]any) (Node, error)

// Run executes the whole DAG:
//  1. Create a buffered channel for each downstream (to) — the write side of fan-in merging.
//  2. Start each node itself: sources have no incoming edges and receive nil; the rest read their own buffers.
//  3. One fan-out goroutine per upstream (from): reads its Out and broadcasts copies to all downstreams.
//  4. EOS propagation: upstream Out closes → that writer ends → downstream counter "-1" → close the downstream buffer at zero.
//     A downstream node reading a closed buffer ends and performs its own Flush.
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

	// 1. Build downstream buffers + record valid upstream outputs.
	outs := map[string]<-chan []Item{} // from -> out (valid output nodes only)
	for id, n := range g.Nodes {
		if s, ok := n.(SourceNode); ok {
			outs[id] = s.Out()
		} else if p, ok := n.(ProcessNode); ok {
			if c, has := p.Out(); has {
				outs[id] = c
			}
		}
	}

	buffers := map[string]chan []Item{} // to -> buffer (elements are batches of []Item)
	writeCount := map[string]*int32{}   // to -> remaining writer count (fan-in merging)
	closeOnce := map[string]*sync.Once{}

	for _, e := range g.Edges {
		if _, has := outs[e.From]; !has {
			continue // the source is not a valid output node, skip
		}
		if ch, ok := buffers[e.To]; ok {
			// already created
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
		*writeCount[e.To]++ // fan-in writer count
	}

	// 2. Start the nodes themselves.
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

	// 3. Fan-out + EOS propagation.
	// downstream group: from -> []to
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
				// The upstream ends: decrement each downstream counter by 1
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
							return // upstream Out closed = EOS
						}
					for _, dch := range down {
						select {
						case dch <- batch:
						case <-ctx.Done():
							return
						}
					}
				case <-ctx.Done():
					// The upstream terminated due to an error/cancel (Out not closed): also end, to avoid wg.Wait hanging.
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

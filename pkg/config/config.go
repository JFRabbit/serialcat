package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// NodeConfig 描述 DAG 中的一个节点。
type NodeConfig struct {
	ID   string   `yaml:"id"`
	Kind string   `yaml:"kind"` // source | filter | process | sink
	Type string   `yaml:"type"` // file | grep | console | ...
	From []string `yaml:"from"` // 上游节点 id（source 留空）
	Conf map[string]any
}

// UnmarshalYAML 自定义：把剩余字段装入 Conf。
func (n *NodeConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain NodeConfig
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*n = NodeConfig(p)
	// 收集 inline 字段（id/kind/type/from 之外）
	n.Conf = map[string]any{}
	keys := map[string]bool{"id": true, "kind": true, "type": true, "from": true}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			k := node.Content[i].Value
			if keys[k] || k == "" {
				continue
			}
			var v any
			if err := node.Content[i+1].Decode(&v); err != nil {
				return err
			}
			n.Conf[k] = v
		}
	}
	return nil
}

// Config 是 DAG 的声明式描述（唯一权威配置模型）。
type Config struct {
	Graph []NodeConfig `yaml:"graph"`
}

// Load 从文件读取并解码 Config。
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse 解析 YAML 字节为 Config。
func Parse(b []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// nodeKind 描述节点的"种类能力"，决定它在 DAG 上能否作为上游/下游。
// Config 层面用 kind 字符串即可区分，不依赖具体 type，避免 import nodes 造成循环依赖。
type nodeKind uint8

const (
	kindUnknown nodeKind = iota
	kindSource           // 无 in、有 out：DAG 起点
	kindMiddle           // 有 in、有 out：filter / process
	kindSink             // 有 in、无 out：DAG 终点
)

// classify 把 config 中的 kind 归为能力类别。未识别或空按 unknown 处理（由后续校验报出）。
func classify(kind string) nodeKind {
	switch kind {
	case "source":
		return kindSource
	case "filter", "process":
		return kindMiddle
	case "sink":
		return kindSink
	default:
		return kindUnknown
	}
}

// Validate 校验配置拓扑，分三阶段：
//  1. 结构级：id 非空唯一、type 非空、kind 合法；
//  2. 边级：source 无 from / 非 source 必须有 from 且引用存在、上游必须有输出（sink 不能作上游）；
//  3. 图级：环检测（成环会死锁/崩溃，即使非法也须拦下）。
func (c *Config) Validate() error {
	kindOf := map[string]string{}  // id -> kind
	capOf := map[string]nodeKind{} // id -> 能力类别
	for _, n := range c.Graph {
		if n.ID == "" {
			return errors.New("节点 id 不能为空")
		}
		if _, dup := kindOf[n.ID]; dup {
			return fmt.Errorf("重复节点 id: %s", n.ID)
		}
		if n.Type == "" {
			return fmt.Errorf("节点 %s 缺少 type", n.ID)
		}
		if classify(n.Kind) == kindUnknown {
			return fmt.Errorf("节点 %s 的 kind 非法: %q（应为 source/filter/process/sink）", n.ID, n.Kind)
		}
		kindOf[n.ID] = n.Kind
		capOf[n.ID] = classify(n.Kind)
	}

	// 阶段2：边级约束。
	adj := map[string][]string{} // from -> []to（环检测用）
	for _, n := range c.Graph {
		if n.Kind == "source" {
			if len(n.From) > 0 {
				return fmt.Errorf("source 节点 %s 不应有 from", n.ID)
			}
			continue
		}
		if len(n.From) == 0 {
			return fmt.Errorf("节点 %s 是 %s 类型但没有 from", n.ID, n.Kind)
		}
		for _, f := range n.From {
			if k, ok := kindOf[f]; !ok {
				return fmt.Errorf("节点 %s 引用不存在的上游 %s", n.ID, f)
			} else if capOf[f] == kindSink {
				return fmt.Errorf("节点 %s 引用了无输出的上游 %s（sink 型 %s 不能作为上游）", n.ID, f, k)
			}
			adj[f] = append(adj[f], n.ID)
		}
	}

	// 阶段3：环检测（Kahn 拓扑）。入度 = 被引用次数；出度用邻接表。
	indeg := map[string]int{}
	for _, n := range c.Graph {
		indeg[n.ID] = len(n.From)
	}
	queue := []string{}
	for id, d := range indeg {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		for _, to := range adj[id] {
			indeg[to]--
			if indeg[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	if len(queue) != len(c.Graph) {
		// 有环：找出仍未被消化的节点（入度>0），还原它们的环。
		var cycle []string
		var start string
		for id, d := range indeg {
			if d > 0 {
				start = id
				break
			}
		}
		for cur := start; ; {
			cycle = append(cycle, cur)
			hit := false
			for _, to := range adj[cur] {
				if indeg[to] > 0 {
					cur, hit = to, true
					break
				}
			}
			if !hit {
				break
			}
			if len(cycle) > len(c.Graph)+1 { // 兜底防死循环
				break
			}
		}
		return fmt.Errorf("配置成环: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

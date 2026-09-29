package config

import (
	"errors"
	"fmt"
	"os"

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

// Validate 校验配置拓扑：id 唯一、source 无 from、from 引用存在。
func (c *Config) Validate() error {
	ids := map[string]string{} // id -> kind
	for _, n := range c.Graph {
		if n.ID == "" {
			return errors.New("节点 id 不能为空")
		}
		if _, dup := ids[n.ID]; dup {
			return fmt.Errorf("重复节点 id: %s", n.ID)
		}
		if n.Type == "" {
			return fmt.Errorf("节点 %s 缺少 type", n.ID)
		}
		ids[n.ID] = n.Kind
	}
	for _, n := range c.Graph {
		if n.Kind == "source" {
			if len(n.From) > 0 {
				return fmt.Errorf("source 节点 %s 不应有 from", n.ID)
			}
			continue
		}
		for _, f := range n.From {
			if _, ok := ids[f]; !ok {
				return fmt.Errorf("节点 %s 引用不存在的上游 %s", n.ID, f)
			}
		}
		if len(n.From) == 0 {
			return fmt.Errorf("节点 %s 是 %s 类型但没有 from", n.ID, n.Kind)
		}
	}
	return nil
}
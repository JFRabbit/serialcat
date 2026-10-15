package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// NodeConfig describes one node in the DAG.
type NodeConfig struct {
	ID   string   `yaml:"id"`
	Kind string   `yaml:"kind"` // source | filter | process | sink
	Type string   `yaml:"type"` // file | grep | console | ...
	From []string `yaml:"from"` // upstream node ids (empty for source)
	Conf map[string]any
}

// UnmarshalYAML is custom: it packs the remaining fields into Conf.
func (n *NodeConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain NodeConfig
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*n = NodeConfig(p)
	// Collect inline fields (anything other than id/kind/type/from)
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

// Config is the declarative description of a DAG (the single authoritative config model).
type Config struct {
	Graph []NodeConfig `yaml:"graph"`
}

// Load reads and decodes a Config from a file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse parses YAML bytes into a Config.
func Parse(b []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// nodeKind describes the "capability kind" of a node, deciding whether it can
// act as an upstream/downstream in the DAG.
// At the Config level the kind string alone distinguishes them, without relying
// on concrete types, avoiding an import cycle with the nodes package.
type nodeKind uint8

const (
	kindUnknown nodeKind = iota
	kindSource           // no in, has out: DAG start
	kindMiddle           // has in, has out: filter / process
	kindSink             // has in, no out: DAG end
)

// classify maps the kind in config to a capability category. Unknown or empty
// kinds are treated as unknown (reported by later validation).
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

// Validate checks the config topology in three stages:
//  1. Structure level: id non-empty and unique, type non-empty, kind valid;
//  2. Edge level: source has no from / non-source must have from referencing an existing node, upstream must have output (sink cannot be an upstream);
//  3. Graph level: cycle detection (a cycle would deadlock/crash, so it must be blocked even if invalid).
func (c *Config) Validate() error {
	kindOf := map[string]string{}  // id -> kind
	capOf := map[string]nodeKind{} // id -> capability category
	for _, n := range c.Graph {
		if n.ID == "" {
			return errors.New("node id must not be empty")
		}
		if _, dup := kindOf[n.ID]; dup {
			return fmt.Errorf("duplicate node id: %s", n.ID)
		}
		if n.Type == "" {
			return fmt.Errorf("node %s missing type", n.ID)
		}
		if classify(n.Kind) == kindUnknown {
			return fmt.Errorf("node %s has invalid kind: %q (expected source/filter/process/sink)", n.ID, n.Kind)
		}
		kindOf[n.ID] = n.Kind
		capOf[n.ID] = classify(n.Kind)
	}

	// Stage 2: edge-level constraints.
	adj := map[string][]string{} // from -> []to (used for cycle detection)
	for _, n := range c.Graph {
		if n.Kind == "source" {
			if len(n.From) > 0 {
				return fmt.Errorf("source node %s must not have from", n.ID)
			}
			continue
		}
		if len(n.From) == 0 {
			return fmt.Errorf("node %s of type %s has no from", n.ID, n.Kind)
		}
		for _, f := range n.From {
			if k, ok := kindOf[f]; !ok {
				return fmt.Errorf("node %s references nonexistent upstream %s", n.ID, f)
			} else if capOf[f] == kindSink {
				return fmt.Errorf("node %s references an upstream %s with no output (sink-type %s cannot be used as upstream)", n.ID, f, k)
			}
			adj[f] = append(adj[f], n.ID)
		}
	}

	// Stage 3: cycle detection (Kahn's topological sort). Indegree = number of references; adjacency list for outdegree.
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
		// There is a cycle: find the nodes not yet consumed (indegree>0) and reconstruct their cycle.
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
			if len(cycle) > len(c.Graph)+1 { // fuse to prevent an infinite loop
				break
			}
		}
		return fmt.Errorf("config forms a cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

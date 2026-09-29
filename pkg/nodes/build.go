package nodes

import (
	"fmt"

	"logdiag/pkg/config"
	"logdiag/pkg/dag"
)

// Build 根据 Config 构造 dag.Graph。
// 目前支持：source:file · filter:grep · sink:console/file。
func Build(cfg *config.Config) (*dag.Graph, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	g := dag.NewGraph()
	for _, nc := range cfg.Graph {
		n, err := buildNode(&nc)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", nc.ID, err)
		}
		g.AddNode(n)
	}
	for _, nc := range cfg.Graph {
		for _, f := range nc.From {
			g.AddEdge(f, nc.ID, 0)
		}
	}
	return g, nil
}

func buildNode(nc *config.NodeConfig) (dag.Node, error) {
	key := nc.Kind + ":" + nc.Type
	switch key {
	case "source:file":
		return fileSource(&nc.Conf, nc.ID)
	case "source:serial-remote":
		return serialRemote(&nc.Conf, nc.ID)
	case "filter:grep":
		return grepNode(&nc.Conf, nc.ID)
	case "process:script":
		return processNode(&nc.Conf, nc.ID)
	case "sink:console":
		return NewConsole(nc.ID), nil
	case "sink:file":
		return fileSink(&nc.Conf, nc.ID)
	default:
		return nil, fmt.Errorf("不支持的节点类型 %s/%s", nc.Kind, nc.Type)
	}
}

func strConf(c map[string]any, k string) (string, error) {
	v, ok := c[k]
	if !ok {
		return "", fmt.Errorf("缺少配置项 %s", k)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("配置项 %s 应为字符串", k)
	}
	return s, nil
}

func fileSource(c *map[string]any, id string) (dag.Node, error) {
	p, err := strConf(*c, "path")
	if err != nil {
		return nil, err
	}
	return NewFileSource(id, p, 0), nil
}

func grepNode(c *map[string]any, id string) (dag.Node, error) {
	p, err := strConf(*c, "match")
	if err != nil {
		return nil, err
	}
	g, err := NewGrep(id, p, 0)
	if err != nil {
		return nil, fmt.Errorf("match 编译失败: %w", err)
	}
	return g, nil
}

func fileSink(c *map[string]any, id string) (dag.Node, error) {
	p, err := strConf(*c, "path")
	if err != nil {
		return nil, err
	}
	return NewFileSink(id, p), nil
}

func serialRemote(c *map[string]any, id string) (dag.Node, error) {
	u, err := strConf(*c, "url")
	if err != nil {
		return nil, err
	}
	return NewSerialRemote(id, u, 0), nil
}

// processNode 构造 process:script 节点。command 必填，args 可省略。
func processNode(c *map[string]any, id string) (dag.Node, error) {
	cmd, err := strConf(*c, "command")
	if err != nil {
		return nil, err
	}
	var args []string
	if v, ok := (*c)["args"]; ok {
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("配置项 args 应为字符串数组")
		}
		for _, a := range list {
			s, ok := a.(string)
			if !ok {
				return nil, fmt.Errorf("配置项 args 的每个元素应为字符串")
			}
			args = append(args, s)
		}
	}
	return NewProcess(id, cmd, args, 0), nil
}
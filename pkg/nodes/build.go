package nodes

import (
	"fmt"
	"path/filepath"

	"github.com/JFRabbit/serialcat/pkg/config"
	"github.com/JFRabbit/serialcat/pkg/dag"
	"github.com/JFRabbit/serialcat/pkg/serial"
)

// Build constructs a dag.Graph from a Config.
// Currently supported: source:file · filter:grep · sink:console/file.
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
	case "source:serial":
		return localSerial(&nc.Conf, nc.ID)
	case "filter:grep":
		return grepNode(&nc.Conf, nc.ID)
	case "process:script":
		return processNode(&nc.Conf, nc.ID)
	case "sink:console":
		return NewConsole(nc.ID), nil
	case "sink:file":
		return fileSink(&nc.Conf, nc.ID)
	default:
		return nil, fmt.Errorf("unsupported node type %s/%s", nc.Kind, nc.Type)
	}
}

func strConf(c map[string]any, k string) (string, error) {
	v, ok := c[k]
	if !ok {
		return "", fmt.Errorf("missing config key %s", k)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("config key %s must be a string", k)
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
		return nil, fmt.Errorf("failed to compile match: %w", err)
	}
	return g, nil
}

func fileSink(c *map[string]any, id string) (dag.Node, error) {
	// path is optional: defaults to logs/serialcat-sink-{ts}.log under the run directory, no config needed.
	p := filepath.Join("logs", "serialcat-sink-{ts}.log")
	if v, ok := (*c)["path"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("config key path must be a string")
		}
		if s != "" {
			p = s
		}
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

func localSerial(c *map[string]any, id string) (dag.Node, error) {
	var baud int = 115200
	if v, ok := (*c)["baud"]; ok {
		b, ok := v.(int)
		if !ok {
			return nil, fmt.Errorf("config key baud must be an integer")
		}
		baud = b
	}

	// port is optional: when omitted, auto-detect and select the serial port.
	// With multiple candidate ports there is no way to decide, so report a clear error to avoid connecting to the wrong one.
	port := ""
	if v, ok := (*c)["port"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("config key port must be a string")
		}
		port = s
	}
	if port == "" {
		drv, err := serial.NewDriver()
		if err != nil {
			return nil, err
		}
		ports, err := drv.ListPorts()
		if err != nil {
			return nil, fmt.Errorf("failed to auto-detect serial port: %w", err)
		}
		switch len(ports) {
		case 0:
			return nil, fmt.Errorf("no serial port detected, specify port explicitly in config")
		case 1:
			port = ports[0]
		default:
			return nil, fmt.Errorf("multiple serial ports detected %v, cannot auto-select, specify port explicitly in config", ports)
		}
	}

	return NewLocalSerial(id, port, baud, 0), nil
}

// processNode builds a process:script node. command is required, args is optional.
func processNode(c *map[string]any, id string) (dag.Node, error) {
	cmd, err := strConf(*c, "command")
	if err != nil {
		return nil, err
	}
	var args []string
	if v, ok := (*c)["args"]; ok {
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("config key args must be a string array")
		}
		for _, a := range list {
			s, ok := a.(string)
			if !ok {
				return nil, fmt.Errorf("each element of config key args must be a string")
			}
			args = append(args, s)
		}
	}
	return NewProcess(id, cmd, args, 0), nil
}

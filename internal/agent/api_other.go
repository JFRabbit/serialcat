//go:build !windows

package agent

import "io"

type otherDriver struct{}

func newDriver() (Driver, error) { return &otherDriver{}, nil }

func (d *otherDriver) ListPorts() ([]string, error) { return nil, errUnsupported }

func (d *otherDriver) Open(string, int) (io.ReadWriteCloser, error) { return nil, errUnsupported }

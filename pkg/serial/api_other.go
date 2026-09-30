//go:build !windows

package serial

import "io"

type otherDriver struct{}

func newDriver() (Driver, error) { return &otherDriver{}, nil }

func (d *otherDriver) ListPorts() ([]string, error) { return nil, ErrUnsupported }

func (d *otherDriver) Open(string, int) (io.ReadWriteCloser, error) { return nil, ErrUnsupported }

//go:build windows

package agent

import (
	"fmt"
	"io"

	"go.bug.st/serial"
)

type winDriver struct{}

func newDriver() (Driver, error) { return &winDriver{}, nil }

func (d *winDriver) ListPorts() ([]string, error) { return serial.GetPortsList() }

func (d *winDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	m := &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	p, err := serial.Open(name, m)
	if err != nil {
		return nil, fmt.Errorf("open %s @%d: %w", name, baud, err)
	}
	return p, nil
}

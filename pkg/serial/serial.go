// Package serial provides a platform abstraction for the serial driver, shared by
// local serial direct connection (source:serial), send-local writes, and the Windows agent.
//
// Cross-platform convention: Windows uses go.bug.st/serial for the real
// implementation; other platforms are placeholder stubs whose Open/ListPorts
// return a clear "Windows only" error.
package serial

import (
	"errors"
	"io"
)

// errUnsupported is the non-Windows platform placeholder: the serial driver provides no physical capability.
var ErrUnsupported = errors.New("serial capability is supported on Windows only, please run on Windows")

// Driver is the platform abstraction of the serial driver. Windows is implemented
// by go.bug.st/serial; other platforms are placeholder stubs.
type Driver interface {
	// ListPorts enumerates the available serial port names (e.g. COM5).
	ListPorts() ([]string, error)
	// Open opens the given serial port (8 data bits / no parity / 1 stop bit).
	Open(name string, baud int) (io.ReadWriteCloser, error)
}

// NewDriver constructs the platform serial driver. On non-Windows platforms the
// returned driver only reports unsupported.
func NewDriver() (Driver, error) { return newDriver() }

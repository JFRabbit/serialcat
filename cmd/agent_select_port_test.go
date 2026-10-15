package cmd

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeSelectDriver is a fake serial.Driver returning a fixed port list / fixed error, used only for selectPort tests.
type fakeSelectDriver struct {
	ports []string
	err   error
}

func (f *fakeSelectDriver) ListPorts() ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.ports, nil
}

func (f *fakeSelectDriver) Open(string, int) (io.ReadWriteCloser, error) {
	return nil, errors.New("Open is out of scope for this test")
}

// withFakeStdin temporarily replaces os.Stdin with the given content for the interactive multi-port branch.
func withFakeStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe failed: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("writing to pipe failed: %v", err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

// TestSelectPortListError covers port enumeration failure returning the error as-is.
func TestSelectPortListError(t *testing.T) {
	want := errors.New("enumeration failed")
	_, err := selectPort(&fakeSelectDriver{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("expected enumeration error %v, got %v", want, err)
	}
}

// TestSelectPortNoPort covers the no-port case: an error should mention --port.
func TestSelectPortNoPort(t *testing.T) {
	_, err := selectPort(&fakeSelectDriver{ports: nil})
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("no-port case should error with a --port hint, got %v", err)
	}
}

// TestSelectPortSingle covers the single-port case being auto-selected without reading stdin.
func TestSelectPortSingle(t *testing.T) {
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM5"}})
	if err != nil {
		t.Fatalf("single port should not error: %v", err)
	}
	if got != "COM5" {
		t.Fatalf("expected COM5 to be auto-selected, got %q", got)
	}
}

// TestSelectPortMultiDefault covers Enter (empty input) selecting the default first port.
func TestSelectPortMultiDefault(t *testing.T) {
	withFakeStdin(t, "\n")
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5"}})
	if err != nil {
		t.Fatalf("Enter default selection should not error: %v", err)
	}
	if got != "COM3" {
		t.Fatalf("Enter should select the default first port COM3, got %q", got)
	}
}

// TestSelectPortMultiPick covers entering an index selecting the matching port.
func TestSelectPortMultiPick(t *testing.T) {
	withFakeStdin(t, "2\n")
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5", "COM7"}})
	if err != nil {
		t.Fatalf("a valid index should not error: %v", err)
	}
	if got != "COM5" {
		t.Fatalf("index 2 should select COM5, got %q", got)
	}
}

// TestSelectPortMultiInvalid covers invalid input (non-numeric / out of range) erroring out.
func TestSelectPortMultiInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"non-numeric", "abc\n"},
		{"out of range high", "9\n"},
		{"out of range low", "0\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withFakeStdin(t, c.input)
			_, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5"}})
			if err == nil || !strings.Contains(err.Error(), "invalid selection") {
				t.Fatalf("input %q should report an invalid selection error, got %v", strings.TrimSpace(c.input), err)
			}
		})
	}
}

package agent

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// sampleText: table-driven coverage of control-character placeholders, bytes above 0x7e, truncation, and empty input.
func TestSampleText(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty input", nil, ""},
		{"plain printable", []byte("hello world"), "hello world"},
		{"newline/carriage return/tab to spaces", []byte("a\nb\rc\td"), "a b c d"},
		{"control chars to placeholders", []byte{0x00, 0x1f, 'a'}, "··a"},
		{"beyond ASCII to placeholders", []byte{0x7f, 0x80, 0xff, 'b'}, "···b"},
		{"boundaries 0x20 and 0x7e preserved", []byte{0x20, 0x7e}, " ~"},
	}

	// Long input truncated to 256 runes
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	tests = append(tests, struct {
		name string
		in   []byte
		want string
	}{"truncated to 256", long, strings.Repeat("x", 256)})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sampleText(tt.in); got != tt.want {
				t.Errorf("sampleText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// score: weighted scoring of printable ratio and newline count.
func TestScore(t *testing.T) {
	tests := []struct {
		name string
		r    ProbeResult
		want float64
	}{
		{"all zeros", ProbeResult{}, 0},
		{"printable without newlines", ProbeResult{Printable: 1}, 1000},
		{"all newlines", ProbeResult{Printable: 1, Newlines: 5}, 1050},
		{"half printable, two newlines", ProbeResult{Printable: 0.5, Newlines: 2}, 520},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := score(tt.r); got != tt.want {
				t.Errorf("score(%+v) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

// score ordering semantics: Printable has weight 1000, so a few newlines cannot reverse a printable-ratio gap.
func TestScoreOrdering(t *testing.T) {
	high := ProbeResult{Printable: 0.9, Newlines: 1} // 910
	low := ProbeResult{Printable: 0.5, Newlines: 30} // 800
	if score(high) <= score(low) {
		t.Errorf("want higher score first: score(high)=%v <= score(low)=%v", score(high), score(low))
	}
}

// fakePort: after preset data is read, it blocks until Close triggers an error exit of the read loop.
type fakePort struct {
	data []byte
	done chan struct{}
	once sync.Once
}

func (p *fakePort) Read(b []byte) (int, error) {
	if len(p.data) > 0 {
		n := copy(b, p.data)
		p.data = p.data[n:]
		return n, nil
	}
	<-p.done
	return 0, errors.New("closed")
}

func (p *fakePort) Write(b []byte) (int, error) { return len(b), nil }
func (p *fakePort) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// fakeDriver: the printable-data port opens at 115200 returning a "good" log stream; other baud rates fail to open.
type fakeDriver struct{}

func (fakeDriver) ListPorts() ([]string, error) { return []string{"COM1"}, nil }

func (fakeDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	if baud != 115200 {
		return nil, errors.New("open failed")
	}
	// Lots of printable characters + newlines, ensuring this baud rate scores highest.
	return &fakePort{data: []byte(strings.Repeat("boot log ok\n", 100)), done: make(chan struct{})}, nil
}

// Probe: under the fake driver, 115200 should be recommended and the rest kept with score 0.
func TestProbeWithFakeDriver(t *testing.T) {
	results, err := Probe(fakeDriver{}, "COM1")
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if len(results) != len(probeBaunds) {
		t.Fatalf("result count = %d, want %d", len(results), len(probeBaunds))
	}
	if results[0].Baud != 115200 || !results[0].Recommended {
		t.Errorf("first = %+v, want 115200 and Recommended", results[0])
	}
	// Baud rates that failed to open: kept, 0 bytes, not recommended.
	for _, r := range results[1:] {
		if r.Bytes != 0 || r.Recommended {
			t.Errorf("failed baud %+v should be kept but with 0 bytes and not recommended", r)
		}
	}
	// Sample text should not be empty.
	if results[0].Sample == "" {
		t.Error("sample text should not be empty")
	}
}

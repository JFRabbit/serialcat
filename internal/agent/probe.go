package agent

import (
	"context"
	"sort"
	"time"

	"github.com/JFRabbit/serialcat/pkg/serial"
)

// Candidate baud rates to probe: covers common firmware default rates.
var probeBaunds = []int{9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600}

// Sample window and max read size for a single baud rate.
const (
	sampleWindow = 300 * time.Millisecond
	maxRead      = 16 * 1024
)

// ProbeResult is the probe result for a single baud rate.
type ProbeResult struct {
	Baud        int     `json:"baud"`
	Bytes       int     `json:"bytes"`
	Printable   float64 `json:"printable_ratio"`
	Newlines    int     `json:"newlines"`
	Sample      string  `json:"sample"`
	Recommended bool    `json:"recommended"`
}

// Probe scans common baud rates for the given port and returns results sorted by recommendation.
// Heuristic: firmware logs usually contain printable ASCII; the higher the printable ratio and the
// more newlines, the more likely the baud rate is correct.
func Probe(drv serial.Driver, port string) ([]ProbeResult, error) {
	results := make([]ProbeResult, 0, len(probeBaunds))
	for _, baud := range probeBaunds {
		results = append(results, probeOne(drv, port, baud))
	}
	// Sort: higher printable ratio and more newlines rank first.
	sort.SliceStable(results, func(i, j int) bool {
		return score(results[i]) > score(results[j])
	})
	if len(results) > 0 {
		results[0].Recommended = true
	}
	return results, nil
}

func score(r ProbeResult) float64 {
	return r.Printable*1000 + float64(r.Newlines)*10
}

// probeOne samples a short window at the given baud rate and scores it.
func probeOne(drv serial.Driver, port string, baud int) ProbeResult {
	res := ProbeResult{Baud: baud}
	p, err := drv.Open(port, baud)
	if err != nil {
		return res // on open failure, keep the result with score 0 for the caller to judge
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), sampleWindow)
	defer cancel()

	// Reader goroutine: keeps reading and hands data back via the channel to avoid blocking select.
	ch := make(chan []byte, 4)
	go readLoop(p, ch)

	var out []byte
loop:
	for len(out) < maxRead {
		select {
		case <-ctx.Done():
			break loop
		case data := <-ch:
			out = append(out, data...)
		case <-time.After(sampleWindow):
			break loop
		}
	}

	printable, newlines := 0, 0
	for _, b := range out {
		switch {
		case b == '\n':
			newlines++
			printable++
		case b >= 0x20 && b <= 0x7e:
			printable++
		}
	}
	total := len(out)
	res.Bytes = total
	res.Newlines = newlines
	if total > 0 {
		res.Printable = float64(printable) / float64(total)
	}
	res.Sample = sampleText(out)
	return res
}

// readLoop keeps reading the serial port and puts data into ch, exiting naturally after the port closes (sends to a closed channel are ignored? — see below).
func readLoop(p interface {
	Read([]byte) (int, error)
	Close() error
}, ch chan []byte) {
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			copied := make([]byte, n)
			copy(copied, buf[:n])
			select {
			case ch <- copied:
			default: // drop when the main loop has stopped (timeout) to avoid leaking this goroutine
			}
		}
		if err != nil {
			return // after the port is closed, Read returns an error and we exit
		}
	}
}

// sampleText converts bytes into a displayable text snippet (truncated, control characters turned into placeholders).
func sampleText(b []byte) string {
	const max = 256
	runes := make([]rune, 0, max)
	for _, c := range b {
		r := rune(c)
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			r = ' '
		case c < 0x20 || c > 0x7e:
			r = '·'
		}
		runes = append(runes, r)
		if len(runes) >= max {
			break
		}
	}
	return string(runes)
}

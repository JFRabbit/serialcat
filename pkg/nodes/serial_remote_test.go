package nodes

import (
	"context"
	"testing"
	"time"
)

func TestNewSerialRemote_Normalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"missing protocol auto-prefixes http", "192.168.1.10:7100", "http://192.168.1.10:7100"},
		{"trims trailing slash", "http://192.168.1.10:7100/", "http://192.168.1.10:7100"},
		{"trims multiple trailing slashes", "http://host:7100///", "http://host:7100"},
		{"trims leading/trailing whitespace", "  http://host:7100  ", "http://host:7100"},
		{"whitespace plus trailing slash", "  host:7100/  ", "http://host:7100"},
		{"https preserved", "https://host:7100", "https://host:7100"},
		{"https trims trailing slash", "https://host:7100/", "https://host:7100"},
		{"path preserved", "http://host:7100/sub/", "http://host:7100/sub"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := NewSerialRemote("s", c.in, 0)
			if n.base != c.want {
				t.Fatalf("base = %q, want %q", n.base, c.want)
			}
			if n.Name() != "s" {
				t.Fatalf("Name() = %q, want %q", n.Name(), "s")
			}
		})
	}
}

// A tiny backoff (nanosecond-level) makes sleepWithBackoff return immediately, verifying the doubling logic.
func TestSleepWithBackoff_Double(t *testing.T) {
	ctx := context.Background()
	backoff := time.Duration(2) // 2ns, jitter range [0,1), actual sleep at nanosecond level
	for i, want := range []time.Duration{4, 8, 16} {
		if !sleepWithBackoff(ctx, &backoff) {
			t.Fatalf("call %d returned false, want true", i)
		}
		if backoff != want {
			t.Fatalf("call %d backoff = %v, want %v", i, backoff, want)
		}
	}
}

// With ctx already cancelled it should return false immediately and leave backoff unchanged.
func TestSleepWithBackoff_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backoff := time.Second
	if sleepWithBackoff(ctx, &backoff) {
		t.Fatal("ctx cancelled but still returned true")
	}
	if backoff != time.Second {
		t.Fatalf("backoff was changed on cancel: %v", backoff)
	}
}

// Cancel mid-run: the backoff wait should be woken up and return false.
func TestSleepWithBackoff_CancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backoff := time.Hour // large backoff; without cancel it would really sleep a long time
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if sleepWithBackoff(ctx, &backoff) {
		t.Fatal("cancelled while waiting still returned true")
	}
	if time.Since(start) > time.Second {
		t.Fatal("not woken up promptly after cancel")
	}
	if backoff != time.Hour {
		t.Fatalf("backoff was changed on cancel: %v", backoff)
	}
}

package agent_test

import (
	"testing"

	"github.com/JFRabbit/serialcat/internal/agent"
)

// RandomToken: a normal input should return a hex string of length 2n.
func TestRandomTokenLength(t *testing.T) {
	for _, n := range []int{1, 8, 16, 32} {
		tok := agent.RandomToken(n)
		if len(tok) != 2*n {
			t.Errorf("RandomToken(%d) length = %d, want %d", n, len(tok), 2*n)
		}
		// Validate the hex charset
		for _, c := range tok {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				t.Errorf("RandomToken(%d) contains non-hex char %q", n, c)
			}
		}
	}
}

// RandomToken: two calls should produce different tokens (randomness).
func TestRandomTokenUniqueness(t *testing.T) {
	a := agent.RandomToken(16)
	b := agent.RandomToken(16)
	if a == b {
		t.Errorf("two calls returned the same token: %q", a)
	}
}

// RandomToken: n=0 returns an empty string; n<0 makes make([]byte, n) panic.
func TestRandomTokenNonPositive(t *testing.T) {
	if tok := agent.RandomToken(0); tok != "" {
		t.Errorf("RandomToken(0) = %q, want empty string", tok)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("RandomToken(-1) did not panic")
		}
	}()
	agent.RandomToken(-1)
}

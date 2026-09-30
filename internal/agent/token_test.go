package agent_test

import (
	"testing"

	"logdiag/internal/agent"
)

// RandomToken：正常入参应返回 2n 长度的十六进制串。
func TestRandomTokenLength(t *testing.T) {
	for _, n := range []int{1, 8, 16, 32} {
		tok := agent.RandomToken(n)
		if len(tok) != 2*n {
			t.Errorf("RandomToken(%d) 长度 = %d, 期望 %d", n, len(tok), 2*n)
		}
		// 校验 hex 字符集
		for _, c := range tok {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				t.Errorf("RandomToken(%d) 含非 hex 字符 %q", n, c)
			}
		}
	}
}

// RandomToken：两次调用应产生不同令牌（随机性）。
func TestRandomTokenUniqueness(t *testing.T) {
	a := agent.RandomToken(16)
	b := agent.RandomToken(16)
	if a == b {
		t.Errorf("两次调用返回相同令牌: %q", a)
	}
}

// RandomToken：n=0 返回空串；n<0 时 make([]byte, n) 触发 panic。
func TestRandomTokenNonPositive(t *testing.T) {
	if tok := agent.RandomToken(0); tok != "" {
		t.Errorf("RandomToken(0) = %q, 期望空串", tok)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("RandomToken(-1) 未 panic")
		}
	}()
	agent.RandomToken(-1)
}

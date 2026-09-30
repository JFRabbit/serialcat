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
		{"缺协议自动补 http", "192.168.1.10:7100", "http://192.168.1.10:7100"},
		{"去尾斜杠", "http://192.168.1.10:7100/", "http://192.168.1.10:7100"},
		{"去多个尾斜杠", "http://host:7100///", "http://host:7100"},
		{"去首尾空白", "  http://host:7100  ", "http://host:7100"},
		{"空白加尾斜杠", "  host:7100/  ", "http://host:7100"},
		{"https 保留", "https://host:7100", "https://host:7100"},
		{"https 去尾斜杠", "https://host:7100/", "https://host:7100"},
		{"带路径保留", "http://host:7100/sub/", "http://host:7100/sub"},
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

// 小 backoff（纳秒级）让 sleepWithBackoff 立即返回，验证翻倍逻辑。
func TestSleepWithBackoff_Double(t *testing.T) {
	ctx := context.Background()
	backoff := time.Duration(2) // 2ns，jitter 区间为 [0,1)，实际 sleep 纳秒级
	for i, want := range []time.Duration{4, 8, 16} {
		if !sleepWithBackoff(ctx, &backoff) {
			t.Fatalf("第 %d 次返回 false，want true", i)
		}
		if backoff != want {
			t.Fatalf("第 %d 次 backoff = %v, want %v", i, backoff, want)
		}
	}
}

// ctx 已取消时应立即返回 false 且 backoff 不变。
func TestSleepWithBackoff_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backoff := time.Second
	if sleepWithBackoff(ctx, &backoff) {
		t.Fatal("ctx 已取消仍返回 true")
	}
	if backoff != time.Second {
		t.Fatalf("取消时 backoff 被改动: %v", backoff)
	}
}

// 运行期中途取消：退避等待应被唤醒并返回 false。
func TestSleepWithBackoff_CancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backoff := time.Hour // 大 backoff，不取消会真的睡很久
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if sleepWithBackoff(ctx, &backoff) {
		t.Fatal("等待中取消仍返回 true")
	}
	if time.Since(start) > time.Second {
		t.Fatal("取消后未立即唤醒")
	}
	if backoff != time.Hour {
		t.Fatalf("取消时 backoff 被改动: %v", backoff)
	}
}

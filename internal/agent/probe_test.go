package agent

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// sampleText：表驱动覆盖控制字符占位、超 0x7e 字节、截断与空输入。
func TestSampleText(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"空输入", nil, ""},
		{"纯可打印", []byte("hello world"), "hello world"},
		{"换行回车制表转空格", []byte("a\nb\rc\td"), "a b c d"},
		{"控制字符变占位", []byte{0x00, 0x1f, 'a'}, "··a"},
		{"超 ASCII 范围变占位", []byte{0x7f, 0x80, 0xff, 'b'}, "···b"},
		{"边界 0x20 与 0x7e 保留", []byte{0x20, 0x7e}, " ~"},
	}

	// 长输入截断到 256 rune
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	tests = append(tests, struct {
		name string
		in   []byte
		want string
	}{"截断到 256", long, strings.Repeat("x", 256)})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sampleText(tt.in); got != tt.want {
				t.Errorf("sampleText(%q) = %q, 期望 %q", tt.in, got, tt.want)
			}
		})
	}
}

// score：可打印占比与换行数加权打分。
func TestScore(t *testing.T) {
	tests := []struct {
		name string
		r    ProbeResult
		want float64
	}{
		{"全零", ProbeResult{}, 0},
		{"纯可打印无换行", ProbeResult{Printable: 1}, 1000},
		{"全换行", ProbeResult{Printable: 1, Newlines: 5}, 1050},
		{"半可打印两换行", ProbeResult{Printable: 0.5, Newlines: 2}, 520},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := score(tt.r); got != tt.want {
				t.Errorf("score(%+v) = %v, 期望 %v", tt.r, got, tt.want)
			}
		})
	}
}

// score 排序语义：Printable 权重 1000，少量换行不足以逆转可打印占比差距。
func TestScoreOrdering(t *testing.T) {
	high := ProbeResult{Printable: 0.9, Newlines: 1} // 910
	low := ProbeResult{Printable: 0.5, Newlines: 30} // 800
	if score(high) <= score(low) {
		t.Errorf("期望高分在前: score(high)=%v <= score(low)=%v", score(high), score(low))
	}
}

// fakePort：预置数据读完后持续阻塞，直到 Close 触发错误退出读循环。
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

// fakeDriver：可打印数据端口在 115200 返回"优质"日志流，其余端口打开失败。
type fakeDriver struct{}

func (fakeDriver) ListPorts() ([]string, error) { return []string{"COM1"}, nil }

func (fakeDriver) Open(name string, baud int) (io.ReadWriteCloser, error) {
	if baud != 115200 {
		return nil, errors.New("open failed")
	}
	// 大量可打印字符 + 换行，保证该档得分最高
	return &fakePort{data: []byte(strings.Repeat("boot log ok\n", 100)), done: make(chan struct{})}, nil
}

// Probe：fake driver 下 115200 应被推荐，其余档记 0 分并保留。
func TestProbeWithFakeDriver(t *testing.T) {
	results, err := Probe(fakeDriver{}, "COM1")
	if err != nil {
		t.Fatalf("Probe 返回错误: %v", err)
	}
	if len(results) != len(probeBaunds) {
		t.Fatalf("结果数 = %d, 期望 %d", len(results), len(probeBaunds))
	}
	if results[0].Baud != 115200 || !results[0].Recommended {
		t.Errorf("首位 = %+v, 期望 115200 且 Recommended", results[0])
	}
	// 打开失败的档位：保留、0 字节、不推荐
	for _, r := range results[1:] {
		if r.Bytes != 0 || r.Recommended {
			t.Errorf("失败档 %+v 应保留但 0 字节且不推荐", r)
		}
	}
	// 采样文本不应为空
	if results[0].Sample == "" {
		t.Error("采样文本不应为空")
	}
}

package agent

import (
	"context"
	"sort"
	"time"

	"github.com/JFRabbit/serialcat/pkg/serial"
)

// 探测候选波特率：覆盖常见固件默认档位。
var probeBaunds = []int{9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600}

// 单档波特率采样时长与最大读取量。
const (
	sampleWindow = 300 * time.Millisecond
	maxRead      = 16 * 1024
)

// ProbeResult 单档波特率的探测结果。
type ProbeResult struct {
	Baud        int     `json:"baud"`
	Bytes       int     `json:"bytes"`
	Printable   float64 `json:"printable_ratio"`
	Newlines    int     `json:"newlines"`
	Sample      string  `json:"sample"`
	Recommended bool    `json:"recommended"`
}

// Probe 对指定端口扫常见波特率，返回按推荐度排序的结果。
// 启发式：固件日志多含可打印 ASCII，可打印字符占比越高、出现换行越多，越可能是正确波特率。
func Probe(drv serial.Driver, port string) ([]ProbeResult, error) {
	results := make([]ProbeResult, 0, len(probeBaunds))
	for _, baud := range probeBaunds {
		results = append(results, probeOne(drv, port, baud))
	}
	// 排序：可打印占比高且换行多者优先。
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

// probeOne 在指定波特率下采样一小段窗口并打分。
func probeOne(drv serial.Driver, port string, baud int) ProbeResult {
	res := ProbeResult{Baud: baud}
	p, err := drv.Open(port, baud)
	if err != nil {
		return res // 打开失败记 0 分，仍保留以便上层判断
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), sampleWindow)
	defer cancel()

	// 读线程：反复读取并以 channel 交还主循环，避免阻塞 select。
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

// readLoop 持续读取串口并把数据放入 ch，串口关闭后自然退出（向关闭 channel 发送会被忽略？——见下）。
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
			default: // 主循环已停止（超时）则丢弃，避免本协程阻塞泄漏
			}
		}
		if err != nil {
			return // 串口关闭(Close)后 Read 返回错误而退出
		}
	}
}

// sampleText 转成可展示的文本片段（截断，控制字符变占位）。
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

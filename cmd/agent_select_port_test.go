package cmd

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeSelectDriver 返回固定端口列表 / 固定错误的 fake serial.Driver，仅用于 selectPort 测试。
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
	return nil, errors.New("Open 不在本测试范围内")
}

// withFakeStdin 把 os.Stdin 临时替换为给定内容，供多端口交互分支读取。
func withFakeStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("写入管道失败: %v", err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

// TestSelectPortListError 枚举端口失败时应原样返回错误。
func TestSelectPortListError(t *testing.T) {
	want := errors.New("枚举失败")
	_, err := selectPort(&fakeSelectDriver{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("应返回枚举错误 %v，实际 %v", want, err)
	}
}

// TestSelectPortNoPort 无可用串口时应报错提示用 --port 指定。
func TestSelectPortNoPort(t *testing.T) {
	_, err := selectPort(&fakeSelectDriver{ports: nil})
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("无串口时应报 --port 相关错误，实际 %v", err)
	}
}

// TestSelectPortSingle 唯一串口应直接选中，不读 stdin。
func TestSelectPortSingle(t *testing.T) {
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM5"}})
	if err != nil {
		t.Fatalf("单端口不应报错: %v", err)
	}
	if got != "COM5" {
		t.Fatalf("应自动选中 COM5，实际 %q", got)
	}
}

// TestSelectPortMultiDefault 多串口时回车（空输入）应选中默认第 1 个。
func TestSelectPortMultiDefault(t *testing.T) {
	withFakeStdin(t, "\n")
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5"}})
	if err != nil {
		t.Fatalf("回车默认选择不应报错: %v", err)
	}
	if got != "COM3" {
		t.Fatalf("回车应选默认第 1 个 COM3，实际 %q", got)
	}
}

// TestSelectPortMultiPick 多串口时输入序号应选中对应端口。
func TestSelectPortMultiPick(t *testing.T) {
	withFakeStdin(t, "2\n")
	got, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5", "COM7"}})
	if err != nil {
		t.Fatalf("合法序号不应报错: %v", err)
	}
	if got != "COM5" {
		t.Fatalf("序号 2 应选 COM5，实际 %q", got)
	}
}

// TestSelectPortMultiInvalid 多串口时非法输入（非数字 / 越界）应报错。
func TestSelectPortMultiInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"非数字", "abc\n"},
		{"越界上", "9\n"},
		{"越界下", "0\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withFakeStdin(t, c.input)
			_, err := selectPort(&fakeSelectDriver{ports: []string{"COM3", "COM5"}})
			if err == nil || !strings.Contains(err.Error(), "无效选择") {
				t.Fatalf("输入 %q 应报无效选择错误，实际 %v", strings.TrimSpace(c.input), err)
			}
		})
	}
}

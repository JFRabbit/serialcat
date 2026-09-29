package cmd

import (
	"net"
	"testing"
)

// TestListenAutoIncrementBaseFree 基口空闲时应直接绑定基口。
func TestListenAutoIncrementBaseFree(t *testing.T) {
	base := "127.0.0.1:0"
	ln, addr, err := listenAutoIncrement(base, 5)
	if err != nil {
		t.Fatalf("listenAutoIncrement: %v", err)
	}
	defer ln.Close()
	_ = addr // 端口为 0 时地址为实际分配端口，这里仅验证成功
	if ln == nil {
		t.Fatal("listener 不应为 nil")
	}
}

// TestListenAutoIncrementOccupied 基口被占用时，应自动选到 +1 端口。
func TestListenAutoIncrementOccupied(t *testing.T) {
	occ, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer occ.Close()
	base := occ.Addr().String() // 已被占用的地址

	ln, addr, err := listenAutoIncrement(base, 5)
	if err != nil {
		t.Fatalf("listenAutoIncrement: %v", err)
	}
	defer ln.Close()

	if addr == base {
		t.Fatalf("端口被占用却仍选了基口 %s", addr)
	}
	t.Logf("基口 %s 占用，自动递增到 %s", base, addr)
}

// TestListenAutoIncrementInvalid 非法地址应报错而非 panic。
func TestListenAutoIncrementInvalid(t *testing.T) {
	if _, _, err := listenAutoIncrement("not-an-addr", 5); err == nil {
		t.Fatal("非法地址应报错")
	}
}
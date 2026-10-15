package cmd

import (
	"net"
	"testing"
)

// TestListenAutoIncrementBaseFree covers binding the base port directly when it is free.
func TestListenAutoIncrementBaseFree(t *testing.T) {
	base := "127.0.0.1:0"
	ln, addr, err := listenAutoIncrement(base, 5)
	if err != nil {
		t.Fatalf("listenAutoIncrement: %v", err)
	}
	defer ln.Close()
	_ = addr // with port 0 the address is the actually assigned port; here we only verify success
	if ln == nil {
		t.Fatal("listener should not be nil")
	}
}

// TestListenAutoIncrementOccupied covers auto-selecting the +1 port when the base port is occupied.
func TestListenAutoIncrementOccupied(t *testing.T) {
	occ, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupied listen failed: %v", err)
	}
	defer occ.Close()
	base := occ.Addr().String() // the occupied address

	ln, addr, err := listenAutoIncrement(base, 5)
	if err != nil {
		t.Fatalf("listenAutoIncrement: %v", err)
	}
	defer ln.Close()

	if addr == base {
		t.Fatalf("base port %s was occupied but still selected", addr)
	}
	t.Logf("base port %s occupied, auto-incremented to %s", base, addr)
}

// TestListenAutoIncrementInvalid covers invalid addresses erroring out instead of panicking.
func TestListenAutoIncrementInvalid(t *testing.T) {
	if _, _, err := listenAutoIncrement("not-an-addr", 5); err == nil {
		t.Fatal("invalid address should error")
	}
}
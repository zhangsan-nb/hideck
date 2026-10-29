package ipsec

import (
	"net"
	"testing"
	"time"
)

func TestSocks5TransportHandshakeTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn // accept, then never answer
		}
	}()
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := NewSocks5Transport(Socks5Config{}, listener.Addr().String(), "192.0.2.1:4500", 200*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handshake with a silent proxy succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handshake with a silent proxy did not time out")
	}
}

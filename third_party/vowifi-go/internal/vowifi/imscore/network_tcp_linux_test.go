package imscore

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMPTCPMSSNeedsTCP(t *testing.T) {
	for _, tc := range []struct {
		name         string
		protocol     int
		setErr       error
		wantFallback bool
		wantErr      error
	}{
		{name: "plain TCP", protocol: syscall.IPPROTO_TCP},
		{name: "compatible MPTCP", protocol: unix.IPPROTO_MPTCP},
		{name: "MPTCP missing MSS", protocol: unix.IPPROTO_MPTCP, setErr: syscall.EOPNOTSUPP, wantFallback: true},
		{name: "permission error", protocol: unix.IPPROTO_MPTCP, setErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "invalid option", protocol: unix.IPPROTO_MPTCP, setErr: syscall.EINVAL, wantErr: syscall.EINVAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			fallback, err := mptcpMSSNeedsTCP(tc.protocol, func() error {
				called = true
				return tc.setErr
			})
			if fallback != tc.wantFallback || !errors.Is(err, tc.wantErr) {
				t.Fatalf("fallback = %t, err = %v; want %t, %v", fallback, err, tc.wantFallback, tc.wantErr)
			}
			if called != (tc.protocol == unix.IPPROTO_MPTCP) {
				t.Fatalf("MSS probe called = %t for protocol %d", called, tc.protocol)
			}
		})
	}
}

func TestSystemIMSListenerPreservesCompatibleMPTCP(t *testing.T) {
	for _, setting := range []string{"0", "1", "2"} {
		t.Run("multipathtcp="+setting, func(t *testing.T) {
			t.Setenv("GODEBUG", "multipathtcp="+setting)
			addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
			reference, err := net.ListenTCP("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer reference.Close()
			protocol := tcpListenerProtocol(t, reference)
			mssErr := setRawTCPMSS(reference)
			wantProtocol := protocol
			if protocol == unix.IPPROTO_MPTCP && errors.Is(mssErr, syscall.EOPNOTSUPP) {
				wantProtocol = syscall.IPPROTO_TCP
			} else if mssErr != nil {
				t.Fatalf("reference MSS: %v", mssErr)
			}
			listener, err := NewSystemIMSNetwork(nil).ListenTCP(addr)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if got := tcpListenerProtocol(t, listener); got != wantProtocol {
				t.Fatalf("listener protocol = %d, want %d", got, wantProtocol)
			}
			if err := setRawTCPMSS(listener); err != nil {
				t.Fatalf("selected listener MSS: %v", err)
			}
			t.Logf("OS protocol=%d, MSS error=%v, selected protocol=%d", protocol, mssErr, wantProtocol)
		})
	}
}

func TestSystemIMSListenerCanDisableMPTCPMSSFallback(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=2")
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	reference, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	network := NewSystemIMSNetwork(nil)
	network.DisableMPTCPMSSFallback = true
	listener, err := network.ListenTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got, want := tcpListenerProtocol(t, listener), tcpListenerProtocol(t, reference); got != want {
		t.Fatalf("listener protocol = %d, want OS protocol %d", got, want)
	}
	if got, want := setRawTCPMSS(listener), setRawTCPMSS(reference); !errors.Is(got, want) {
		t.Fatalf("listener MSS error = %v, want original error %v", got, want)
	}
}

func tcpListenerProtocol(t *testing.T, listener net.Listener) int {
	t.Helper()
	raw, err := listener.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var protocol int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		protocol, socketErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PROTOCOL)
	}); err != nil {
		t.Fatal(err)
	}
	if socketErr != nil {
		t.Fatal(socketErr)
	}
	return protocol
}

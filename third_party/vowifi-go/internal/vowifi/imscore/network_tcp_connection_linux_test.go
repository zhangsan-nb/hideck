package imscore

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSystemIMSListenerNegotiatesMPTCPWhenMSSIsSupported(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=1")
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			addr := &net.TCPAddr{IP: net.ParseIP(host)}
			wantMPTCP := referenceSupportsMPTCPMSS(t, addr)
			base, err := NewSystemIMSNetwork(nil).ListenTCP(addr)
			if err != nil {
				t.Fatal(err)
			}
			defer base.Close()
			listener, err := applyIPSec3GPPPortSListenerTCPMSSWithError(base)
			if err != nil {
				t.Fatal(err)
			}
			if err := base.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			dialer := net.Dialer{Timeout: 3 * time.Second}
			dialer.SetMultipathTCP(true)
			client, err := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			for _, conn := range []net.Conn{client, server} {
				assertNegotiatedMPTCP(t, conn.(*net.TCPConn), wantMPTCP)
			}
			if mss := socketTCPMSS(t, server); mss <= 0 || mss > ipsec3gppTCPMSS {
				t.Fatalf("accepted MSS = %d, want 0 < MSS <= %d", mss, ipsec3gppTCPMSS)
			}
			assertTCPPayload(t, client, server)
			assertTCPPayload(t, server, client)
			t.Logf("host=%s negotiated_mptcp=%t accepted_mss=%d round_trip=passed", host, wantMPTCP, socketTCPMSS(t, server))
		})
	}
}

func referenceSupportsMPTCPMSS(t *testing.T, addr *net.TCPAddr) bool {
	t.Helper()
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if tcpListenerProtocol(t, listener) != unix.IPPROTO_MPTCP {
		return false
	}
	err = setRawTCPMSS(listener)
	if errors.Is(err, syscall.EOPNOTSUPP) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func assertNegotiatedMPTCP(t *testing.T, conn *net.TCPConn, want bool) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := conn.MultipathTCP()
	if err != nil || got != want {
		t.Fatalf("negotiated MPTCP = %t, err = %v; want %t", got, err, want)
	}
}

func assertTCPPayload(t *testing.T, sender, receiver net.Conn) {
	t.Helper()
	const payload = "IMS MPTCP MSS round trip"
	if _, err := io.WriteString(sender, payload); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(receiver, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != payload {
		t.Fatalf("payload = %q, want %q", buffer, payload)
	}
}

package swu

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"github.com/iniwex5/vowifi-go/engine/ipsec"
)

type natTSOCKSServer struct {
	listener   net.Listener
	relay      *net.UDPConn
	associates atomic.Int32
}

// newNATTSOCKSServer accepts up to maxAssociations SOCKS5 UDP ASSOCIATE
// requests, then refuses further control connections.
func newNATTSOCKSServer(t *testing.T, maxAssociations int) *natTSOCKSServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server := &natTSOCKSServer{listener: listener, relay: relay}
	t.Cleanup(func() { _ = listener.Close(); _ = relay.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if int(server.associates.Add(1)) > maxAssociations {
				_ = conn.Close()
				continue
			}
			go func() {
				_ = serveCandidateSOCKSControl(conn, relay.LocalAddr().(*net.UDPAddr))
				_ = conn.Close()
			}()
		}
	}()
	return server
}

// readDatagram reads the one-byte IKE message sent with SendIKE([]byte{0}) and
// returns the client source port and SOCKS5 destination port. On 4500 the
// message must carry four octets of zeros (RFC 7296 §2.23).
func (s *natTSOCKSServer) readDatagram(t *testing.T) (int, uint16) {
	t.Helper()
	_ = s.relay.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 128)
	length, from, err := s.relay.ReadFromUDP(buffer)
	if err != nil || length < 10 || buffer[3] != 1 {
		t.Fatalf("relay read length=%d err=%v", length, err)
	}
	if payload := buffer[10:length]; !bytes.Equal(payload, []byte{0, 0, 0, 0, 0}) {
		t.Fatalf("IKE payload=%x want non-ESP marker + message", payload)
	}
	return from.Port, binary.BigEndian.Uint16(buffer[8:10])
}

func newNATTSOCKSSession(t *testing.T, server *natTSOCKSServer) (*Session, *ipsec.Socks5Transport) {
	t.Helper()
	session := NewSession(&Config{EPDGAddr: "192.0.2.1:500", ProxyAddr: server.listener.Addr().String()})
	if err := session.buildTransport(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.stopTransport)
	original, ok := session.transport().(*ipsec.Socks5Transport)
	if !ok {
		t.Fatalf("transport=%T want SOCKS5", session.transport())
	}
	return session, original
}

func TestNATTFloatOpensNewSOCKS5Association(t *testing.T) {
	server := newNATTSOCKSServer(t, 2)
	session, original := newNATTSOCKSSession(t, server)

	if err := session.applyNATTraversal([]byte{1}, []byte{2}); err != nil {
		t.Fatal(err)
	}

	replacement, ok := session.transport().(*ipsec.Socks5Transport)
	if !ok || replacement == original {
		t.Fatalf("transport not replaced: %T", session.transport())
	}
	if got := server.associates.Load(); got != 2 {
		t.Fatalf("UDP ASSOCIATE count=%d want 2", got)
	}
	if replacement.LocalPort() == original.LocalPort() {
		t.Fatalf("new association reused local port %d", original.LocalPort())
	}
	if _, open := <-original.IKEPackets(); open {
		t.Fatal("original association still open")
	}
	if session.remotePort != natTPort || replacement.RemotePort() != natTPort {
		t.Fatalf("remote port session=%d transport=%d want 4500", session.remotePort, replacement.RemotePort())
	}
	if err := replacement.SendIKE([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if source, destination := server.readDatagram(t); source != int(replacement.LocalPort()) || destination != natTPort {
		t.Fatalf("datagram from port %d to %d, want from %d to 4500", source, destination, replacement.LocalPort())
	}

	if err := session.applyNATTraversal([]byte{1}, []byte{2}); err != nil {
		t.Fatal(err)
	}
	if got := server.associates.Load(); got != 2 || session.transport() != replacement {
		t.Fatalf("repeated float reassociated: count=%d", got)
	}
}

// After a declined session resumption the resumption's IKE control loop is
// still reading the transport when the full IKE_SA_INIT floats to 4500.
func TestNATTFloatMovesRunningIKEControlToNewAssociation(t *testing.T) {
	server := newNATTSOCKSServer(t, 2)
	session, original := newNATTSOCKSSession(t, server)
	if err := session.startIKEControl(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.stopIKEControl() })

	if err := session.applyNATTraversal([]byte{1}, []byte{2}); err != nil {
		t.Fatal(err)
	}

	replacement := session.transport()
	if replacement == original {
		t.Fatal("transport not replaced")
	}
	session.controlMu.RLock()
	running, bound := session.controlRunning, session.controlTransport
	session.controlMu.RUnlock()
	if !running || bound != replacement {
		t.Fatalf("IKE control running=%v bound to new transport=%v", running, bound == replacement)
	}
	if err := session.TerminalError(); err != nil {
		t.Fatalf("session failed after float: %v", err)
	}
}

func TestNATTFloatReportsSOCKS5ReassociationFailure(t *testing.T) {
	server := newNATTSOCKSServer(t, 1)
	session, original := newNATTSOCKSSession(t, server)

	err := session.applyNATTraversal([]byte{1}, []byte{2})
	if err == nil || !strings.Contains(err.Error(), "open SOCKS5 NAT-T association") {
		t.Fatalf("float error=%v, want association failure", err)
	}
	if session.transport() != original {
		t.Fatalf("transport replaced despite failed reassociation: %T", session.transport())
	}
	if session.remotePort != 500 || original.RemotePort() != 500 {
		t.Fatalf("failed float changed port: session=%d transport=%d", session.remotePort, original.RemotePort())
	}
}

func TestIKEInitReportsNATTReassociationFailure(t *testing.T) {
	server := newNATTSOCKSServer(t, 1)
	session, original := newNATTSOCKSSession(t, server)
	if _, err := session.buildIKESAInitPacket(); err != nil {
		t.Fatal(err)
	}
	responderDH, err := crypto.NewDiffieHellman(session.dhGroup)
	if err != nil {
		t.Fatal(err)
	}
	response := buildInitResp(t, session, responderDH,
		&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.NAT_DETECTION_SOURCE_IP, NotifyData: make([]byte, sha1.Size)},
		&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.NAT_DETECTION_DESTINATION_IP, NotifyData: make([]byte, sha1.Size)},
	)
	err = session.handleIKESAInitResp(encodeInitPacket(t, response))
	if err == nil || !strings.Contains(err.Error(), "open SOCKS5 NAT-T association") {
		t.Fatalf("IKE init error=%v, want NAT-T failure", err)
	}
	if cleanupErr := session.cleanupConnectAttempt(context.Background(), err); cleanupErr == nil {
		t.Fatal("cleanup lost the handshake error")
	}
	if session.transport() != nil {
		t.Fatal("failed handshake retained its transport")
	}
	if _, open := <-original.IKEPackets(); open {
		t.Fatal("failed handshake did not close the original association")
	}
}

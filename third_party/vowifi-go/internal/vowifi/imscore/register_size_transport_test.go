package imscore

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func largeRegisterTestConfig(registrar string) *IMSConfig {
	cfg := registerTransportTestConfig("auto", registrar)
	cfg.UserAgent = strings.Repeat("x", maxUDPRequestBytes)
	return cfg
}

func TestAutoRegisterOverUDPLimitUsesTCP(t *testing.T) {
	tcpRegistrar, udpRegistrar := listenRegisterTransports(t)
	defer tcpRegistrar.Close()
	defer udpRegistrar.Close()
	seen := make(chan string, 1)
	result := make(chan error, 1)
	go serveTCPRegisterStatus(tcpRegistrar, seen, result)

	svc, err := New(largeRegisterTestConfig(tcpRegistrar.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	request := <-seen
	if len(request) <= maxUDPRequestBytes || !strings.HasPrefix(sipHeaderValue(request, "Via"), "SIP/2.0/TCP ") ||
		!strings.Contains(sipHeaderValue(request, "Contact"), ";transport=tcp>") {
		t.Fatalf("large auto REGISTER was not sent as TCP (%d bytes): %q", len(request), sipHeaderValue(request, "Via"))
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	assertNoUDPRegister(t, udpRegistrar)
}

func TestAutoRegisterOverUDPLimitStaysOnUDPWhenTCPIsRefused(t *testing.T) {
	registrar, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer registrar.Close()
	seen := make(chan string, 1)
	go serveRegisterStatus(registrar, 200, seen)

	svc, err := New(largeRegisterTestConfig(registrar.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if request := <-seen; !strings.HasPrefix(sipHeaderValue(request, "Via"), "SIP/2.0/UDP ") {
		t.Fatalf("REGISTER did not fall back to UDP: %q", sipHeaderValue(request, "Via"))
	}
}

func TestAutoRegisterOverUDPLimitDoesNotRetryUDPAfterSIPFailure(t *testing.T) {
	tcpRegistrar, udpRegistrar := listenRegisterTransports(t)
	defer tcpRegistrar.Close()
	defer udpRegistrar.Close()
	tcpResult := make(chan error, 1)
	go serveTCPRegisterStatusCode(tcpRegistrar, 503, nil, tcpResult)

	svc, err := New(largeRegisterTestConfig(tcpRegistrar.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("Register error = %v, want status 503", err)
	}
	if err := <-tcpResult; err != nil {
		t.Fatal(err)
	}
	assertNoUDPRegister(t, udpRegistrar)
}

// Fails UDP listens once a TCP dial has happened.
type udpFailsAfterTCPNetwork struct {
	*SystemIMSNetwork
	dialed atomic.Bool
}

func (n *udpFailsAfterTCPNetwork) DialTCPContext(ctx context.Context, local, remote *net.TCPAddr) (net.Conn, error) {
	n.dialed.Store(true)
	return n.SystemIMSNetwork.DialTCPContext(ctx, local, remote)
}

func (n *udpFailsAfterTCPNetwork) ListenPacket(network string, addr *net.UDPAddr) (net.PacketConn, error) {
	if n.dialed.Load() {
		return nil, errors.New("udp unavailable")
	}
	return n.SystemIMSNetwork.ListenPacket(network, addr)
}

func TestAutoRegisterOverUDPLimitDoesNotReopenTCPWhenUDPFallbackFails(t *testing.T) {
	tcpRegistrar, udpRegistrar := listenRegisterTransports(t)
	defer tcpRegistrar.Close()
	defer udpRegistrar.Close()
	tcpResult := make(chan error, 1)
	go resetAcceptedTCPRegister(tcpRegistrar, tcpResult)

	cfg := largeRegisterTestConfig(tcpRegistrar.Addr().String())
	cfg.IMSNetwork = &udpFailsAfterTCPNetwork{SystemIMSNetwork: NewSystemIMSNetwork(net.IPv4(127, 0, 0, 1))}
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = svc.Register(ctx)
	if serverErr := <-tcpResult; serverErr != nil {
		t.Fatalf("reset registrar: %v", serverErr)
	}
	if err == nil || !strings.Contains(err.Error(), "udp unavailable") {
		t.Fatalf("Register error = %v, want the UDP fallback failure", err)
	}
	if err := tcpRegistrar.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if conn, err := tcpRegistrar.AcceptTCP(); err == nil {
		conn.Close()
		t.Fatal("failed TCP transport was reopened")
	}
}

func resetAcceptedTCPRegister(listener *net.TCPListener, result chan<- error) {
	conn, err := listener.AcceptTCP()
	if err != nil {
		result <- err
		return
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		result <- err
		return
	}
	// Wait for REGISTER so the reset tests an established transport, not a failed dial.
	request, err := readSIPStreamMessage(bufio.NewReader(conn))
	if err != nil {
		result <- err
		return
	}
	if !strings.HasPrefix(request, "REGISTER ") {
		result <- errors.New("expected TCP REGISTER before reset")
		return
	}
	if err := conn.SetLinger(0); err != nil {
		result <- err
		return
	}
	result <- conn.Close()
}

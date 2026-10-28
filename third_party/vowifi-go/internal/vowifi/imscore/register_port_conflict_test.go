package imscore

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRefreshPortConflictRegistersWithFreshSession(t *testing.T) {
	registrar, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer registrar.Close()
	requests := make(chan string, 3)
	go func() {
		buffer := make([]byte, 64*1024)
		for _, extra := range []string{
			"Expires: 1\r\n",
			"Warning: 399 pcscf \"AKA IP+Port conflict\"\r\n",
			"Expires: 3600\r\n",
		} {
			n, remote, err := registrar.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			request := string(buffer[:n])
			requests <- request
			status := 200
			if extra[0] == 'W' {
				status = 500
			}
			_, _ = registrar.WriteToUDP([]byte(registerWireResponse(request, status, extra)), remote)
		}
	}()
	svc, err := New(&IMSConfig{
		DeviceID: "dev-conflict", IMSI: "310260123456789", IMPI: "310260123456789@ims.example",
		IMPU: "sip:310260123456789@ims.example", Domain: "ims.example",
		LocalIP: net.IPv4(127, 0, 0, 1), Transport: "udp", Registrar: registrar.LocalAddr().String(),
		IMSNetwork: NewSystemIMSNetwork(net.IPv4(127, 0, 0, 1)), AKAProvider: stubAKAProvider{},
		EnableIPSec3GPP: disabledBoolPointer(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	seen := make([]string, 0, 3)
	for len(seen) < 3 {
		select {
		case request := <-requests:
			seen = append(seen, request)
		case <-ctx.Done():
			t.Fatalf("received %d REGISTER requests before timeout", len(seen))
		}
	}
	if sipHeaderValue(seen[0], "Call-ID") != sipHeaderValue(seen[1], "Call-ID") {
		t.Fatal("refresh changed Call-ID")
	}
	if sipHeaderValue(seen[2], "Call-ID") == sipHeaderValue(seen[1], "Call-ID") {
		t.Fatal("port conflict retry reused the refreshed registration")
	}
	deadline := time.Now().Add(2 * time.Second)
	for svc.registrationGeneration.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.registrationGeneration.Load() < 2 || !svc.IsRegistered() {
		t.Fatalf("fresh registration did not complete: state %q", svc.RegState())
	}
	select {
	case err := <-svc.RegistrationErrors():
		t.Fatalf("unexpected registration error: %v", err)
	default:
	}
}

// Globe PH never answers REGISTER over UDP. Its preset configures TCP so the
// fresh-port registration after a conflict does not spend the refresh deadline
// waiting on UDP.
func TestPortConflictRetryOnTCPNeverWaitsOnUDP(t *testing.T) {
	tcpRegistrar, udpRegistrar := listenRegisterTransports(t)
	defer tcpRegistrar.Close()
	defer udpRegistrar.Close()
	var mu sync.Mutex
	replies := []string{"Expires: 1\r\n", "Warning: 399 pcscf \"AKA IP+Port conflict\"\r\n", "Expires: 3600\r\n"}
	registers := make(chan string, len(replies))
	go func() {
		for {
			conn, err := tcpRegistrar.AcceptTCP()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					request, err := readSIPStreamMessage(reader)
					if err != nil {
						return
					}
					extra := ""
					mu.Lock()
					if sipRequestMethod(request) == "REGISTER" && len(replies) > 0 {
						extra, replies = replies[0], replies[1:]
						registers <- request
					}
					mu.Unlock()
					status := 200
					if strings.HasPrefix(extra, "Warning") {
						status = 500
					}
					_, _ = conn.Write([]byte(registerWireResponse(request, status, extra)))
				}
			}()
		}
	}()
	svc, err := New(registerTransportTestConfig("tcp", tcpRegistrar.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err != nil {
		t.Fatalf("Register: %v", err)
	}
	seen := make([]string, 0, 3)
	for len(seen) < 3 {
		select {
		case request := <-registers:
			seen = append(seen, request)
		case <-ctx.Done():
			t.Fatalf("received %d REGISTER requests over TCP before timeout", len(seen))
		}
	}
	if sipHeaderValue(seen[0], "Call-ID") != sipHeaderValue(seen[1], "Call-ID") {
		t.Fatal("refresh changed Call-ID")
	}
	if sipHeaderValue(seen[2], "Call-ID") == sipHeaderValue(seen[1], "Call-ID") {
		t.Fatal("port conflict retry reused the refreshed registration")
	}
	deadline := time.Now().Add(2 * time.Second)
	for svc.registrationGeneration.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.registrationGeneration.Load() < 2 || !svc.IsRegistered() {
		t.Fatalf("fresh registration did not complete: state %q", svc.RegState())
	}
	if got := svc.currentRegistrationTransport(); got != "tcp" {
		t.Fatalf("fresh registration transport = %q, want tcp", got)
	}
	// Anything sent over UDP is still queued on the unread socket.
	assertNoUDPRegister(t, udpRegistrar)
}

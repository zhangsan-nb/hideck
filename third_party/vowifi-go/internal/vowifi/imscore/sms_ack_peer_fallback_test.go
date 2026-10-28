package imscore

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/internal/smscodec"
)

func newRPReportFlowTestService(t *testing.T, portUS bool) (*Service, <-chan string) {
	t.Helper()
	s, _, _ := newInboundSMSTestService(t)
	registered := startProtectedTestTCPOutbound(t, s)
	s.mu.Lock()
	s.cfg.LocalIP = net.IPv4(127, 0, 0, 1)
	s.protectedClientPort, s.protectedServerPort = 5062, 5064
	s.cfg.IMSRegisterTemplate.RPReportOverPortUS = portUS
	s.mu.Unlock()
	return s, registered
}

// brokenPeer stands in for the connection the P-CSCF opened to port_us. Any
// RP report written to it fails, which costs an attempt.
func brokenPeer(t *testing.T) net.Conn {
	t.Helper()
	peer, network := net.Pipe()
	_ = network.Close()
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

// TS 33.203 7.1: a UE request goes on its own connection from port_uc to port_ps.
func TestRPReportStartsOnRegisteredFlow(t *testing.T) {
	s, registered := newRPReportFlowTestService(t, false)
	raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x46, "+447700900123", "default"))
	attempts, err := s.sendRPReportWithRetryPolicy(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x46), RPMR: 0x46, PeerConn: brokenPeer(t),
	}, 0, 0)
	if err != nil || attempts != 1 {
		t.Fatalf("attempts=%d error=%v, want one attempt on the registered flow", attempts, err)
	}
	report := waitForOutboundSMSControl(t, registered)
	if body, err := rawSIPBody(report); err != nil || string(body) != string(smscodec.BuildRPAck(0x46)) {
		t.Fatalf("RP report = %x, %v", body, err)
	}
	if !strings.HasPrefix(sipHeaderValue(report, "Via"), "SIP/2.0/TCP ") {
		t.Fatal("RP report did not use the registered TCP flow")
	}
}

func TestRPReportPresetStartsOnPortUSAndFallsBack(t *testing.T) {
	s, registered := newRPReportFlowTestService(t, true)
	raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x44, "+447700900123", "peer"))
	attempts, err := s.sendRPReportWithRetryPolicy(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x44), RPMR: 0x44, PeerConn: brokenPeer(t),
	}, 0, 0)
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d error=%v, want the port_us failure then the registered flow", attempts, err)
	}
	waitForOutboundSMSControl(t, registered)
}

// A SIP answer on the registered flow is final for that path: the report is
// never resent over the P-CSCF's port_us connection to get around it.
func TestRPReportAnsweredOnRegisteredFlowNeverSwitchesToPeer(t *testing.T) {
	for _, test := range []struct {
		status, attempts int
	}{
		{403, 1}, {488, 1}, {408, rpReportMaxAttempts}, {503, rpReportMaxAttempts},
	} {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			s, _, _ := newInboundSMSTestService(t)
			sent := 0
			s.transport.SetSendFn(func(request string) error {
				sent++
				s.transport.DeliverResponse(registerResponseForRequest(request, test.status, nil))
				return nil
			})
			peer, network := net.Pipe()
			t.Cleanup(func() { _ = peer.Close(); _ = network.Close() })
			peerWrites := make(chan struct{}, 1)
			go func() {
				if _, err := network.Read(make([]byte, 1)); err == nil {
					peerWrites <- struct{}{}
				}
			}()
			raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x47, "+447700900123", "answered"))
			attempts, err := s.sendRPReportWithRetryPolicy(rpReportRequest{
				Inbound: raw, Body: smscodec.BuildRPAck(0x47), RPMR: 0x47, PeerConn: peer,
			}, 0, 0)
			if err == nil || attempts != test.attempts || sent != test.attempts {
				t.Fatalf("attempts=%d sent=%d error=%v, want %d on the registered flow", attempts, sent, err, test.attempts)
			}
			select {
			case <-peerWrites:
				t.Fatal("RP report switched to the port_us connection after a SIP answer")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

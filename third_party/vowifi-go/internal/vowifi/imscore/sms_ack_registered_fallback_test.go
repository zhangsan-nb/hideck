package imscore

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/iniwex5/vowifi-go/internal/smscodec"
)

func TestRPReportFallsBackToPeerWhenRegisteredFlowFails(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	registeredAttempts := 0
	service.transport.SetSendFn(func(string) error {
		registeredAttempts++
		return errors.New("registered flow write failed")
	})
	peer, network := net.Pipe()
	t.Cleanup(func() { _ = peer.Close(); _ = network.Close() })
	peerResult := make(chan error, 1)
	go func() {
		request, err := readSIPStreamMessage(bufio.NewReader(network))
		if err == nil && !strings.HasPrefix(request, "MESSAGE ") {
			err = errors.New("peer received a non-MESSAGE request")
		}
		if err == nil {
			service.transport.DeliverResponse(registerResponseForRequest(request, 202, nil))
		}
		peerResult <- err
	}()
	raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x48, "+447700900123", "registered failure"))
	attempts, err := service.sendRPReportWithRetryPolicy(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x48), RPMR: 0x48, PeerConn: peer,
	}, 0, 0)
	if err != nil || attempts != 2 || registeredAttempts != 1 {
		t.Fatalf("attempts=%d registered=%d error=%v, want one registered failure then peer success", attempts, registeredAttempts, err)
	}
	if err := <-peerResult; err != nil {
		t.Fatalf("read peer RP report: %v", err)
	}
}

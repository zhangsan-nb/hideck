package imscore

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/internal/smscodec"
)

func TestVodafoneUKMTReport488TriesTunnelAlternateBeforeRuntime(t *testing.T) {
	first, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstSeen, secondSeen := make(chan string, 1), make(chan string, 1)
	go serveRegisterStatus(first, 200, firstSeen)
	go serveRegisterStatus(second, 200, secondSeen)

	config := registerTransportTestConfig("udp", first.LocalAddr().String()+";"+second.LocalAddr().String())
	config.CarrierPresetID = vodafoneUKCarrierPresetID
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer service.StopCurrent()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.Register(ctx); err != nil {
		t.Fatal(err)
	}
	<-firstSeen
	service.triggerMTReportPCSCFRecovery(&rpReportRejectError{
		Status: 488, Registrar: first.LocalAddr().String(),
	})
	select {
	case <-secondSeen:
	case <-ctx.Done():
		t.Fatal("488 did not try the unattempted P-CSCF in the current tunnel")
	}
	if got := service.StatusCurrent().Registrar; got != second.LocalAddr().String() {
		t.Fatalf("registrar = %s, want %s", got, second.LocalAddr())
	}
	select {
	case err := <-service.RegistrationErrors():
		t.Fatalf("alternate registration requested a full runtime: %v", err)
	default:
	}
}

func TestVodafoneUKMTReport488WaitsForRedeliveryOnReplacement(t *testing.T) {
	first, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstSeen, secondSeen := make(chan string, 1), make(chan string, 1)
	go serveRegisterStatus(first, 200, firstSeen)
	go serveRegisterStatus(second, 200, secondSeen)

	config := registerTransportTestConfig("udp", first.LocalAddr().String()+";"+second.LocalAddr().String())
	config.CarrierPresetID = vodafoneUKCarrierPresetID
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer service.StopCurrent()
	service.portSFailoverVerifyWait = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.Register(ctx); err != nil {
		t.Fatal(err)
	}
	<-firstSeen
	service.triggerMTReportPCSCFRecovery(&rpReportRejectError{
		Status: 488, Registrar: first.LocalAddr().String(),
	})
	select {
	case <-secondSeen:
	case <-ctx.Done():
		t.Fatal("488 did not register on the replacement P-CSCF")
	}
	waitForPCSCFRecoveryToFinish(t, service)
	time.Sleep(3 * service.portSFailoverVerifyWait)

	service.mu.RLock()
	watch := service.replacementDownlinkWatch
	service.mu.RUnlock()
	if service.RegState() != regRegistered || service.registrarPenalties.recoveryInProgress() || watch != nil {
		t.Fatal("replacement REGISTER did not settle into passive redelivery wait")
	}
	states := service.registrarPenalties.states(time.Now())
	if states[first.LocalAddr().String()].reason != vodafoneUKMTReportFailure {
		t.Fatal("488 penalty on the rejected P-CSCF was lost")
	}
	if states[second.LocalAddr().String()].reason == "downlink_unverified" {
		t.Fatal("silent replacement P-CSCF was treated as a failed downlink")
	}
	select {
	case recoveryErr := <-service.RegistrationErrors():
		t.Fatalf("silent replacement requested another recovery: %v", recoveryErr)
	default:
	}
}

func waitForPCSCFRecoveryToFinish(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for service.pcscfRecoveryPending.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.pcscfRecoveryPending.Load() {
		t.Fatal("P-CSCF recovery did not finish")
	}
}

func TestVodafoneUKRepeatedMTReport488ExhaustsAssignedCandidates(t *testing.T) {
	service := newPortSSessionTestService(t, vodafoneUKCarrierPresetID)
	service.mu.Lock()
	service.externalTransport = true
	service.regState = regRegistered
	service.mu.Unlock()

	service.markVodafoneRegistrarFailure("pcscf-a.example:5060", vodafoneUKMTReportFailure, nil)
	if next := service.selectMTReportAlternate("pcscf-a.example:5060"); next != "pcscf-b.example:5060" {
		t.Fatalf("first 488 alternate = %q", next)
	}
	service.markVodafoneRegistrarFailure("pcscf-b.example:5060", vodafoneUKMTReportFailure, nil)
	if next := service.selectMTReportAlternate("pcscf-b.example:5060"); next != "" {
		t.Fatalf("reused rejected P-CSCF before fresh discovery: %q", next)
	}
	service.registrarPenalties.mu.Lock()
	round := service.registrarPenalties.downlinkRound
	exhausted := round != nil && round.rediscoveryRequested
	service.registrarPenalties.mu.Unlock()
	if !exhausted {
		t.Fatal("repeated 488 did not exhaust the assigned P-CSCF set")
	}
}

func TestVodafoneUKMTReport488AfterPassiveWaitSkipsEarlierRejectedPath(t *testing.T) {
	service := newPortSSessionTestService(t, vodafoneUKCarrierPresetID)
	service.mu.Lock()
	service.registrar = "pcscf-b.example:5060"
	service.registrarCandidates = []string{
		"pcscf-a.example:5060", "pcscf-b.example:5060", "pcscf-c.example:5060",
	}
	service.externalTransport = true
	service.regState = regRegistered
	service.mu.Unlock()
	service.markVodafoneRegistrarFailure("pcscf-a.example:5060", vodafoneUKMTReportFailure, nil)
	service.mu.Lock()
	service.registrarRecoveryAttempt = registrarRecoveryAttempt{
		registrar: service.registrar, generation: service.registrarPenalties.recoveryGeneration(),
	}
	service.mu.Unlock()
	if !service.settleMTReportRecoveryAfterRegister() {
		t.Fatal("replacement REGISTER did not enter passive redelivery wait")
	}

	service.markVodafoneRegistrarFailure("pcscf-b.example:5060", vodafoneUKMTReportFailure, nil)
	if next := service.selectMTReportAlternate("pcscf-b.example:5060"); next != "pcscf-c.example:5060" {
		t.Fatalf("second 488 selected %q instead of the untried P-CSCF", next)
	}
	states := service.registrarPenalties.states(time.Now())
	if states["pcscf-a.example:5060"].reason != vodafoneUKMTReportFailure ||
		states["pcscf-b.example:5060"].reason != vodafoneUKMTReportFailure {
		t.Fatal("passive wait lost an earlier actual 488 penalty")
	}
}

func TestVodafoneUKMTReport488RequestsFreshPCSCFPath(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	service.cfg.CarrierPresetID = vodafoneUKCarrierPresetID
	service.mu.Lock()
	service.registrar = "pcscf-a.example:5060"
	service.registrarCandidates = []string{"pcscf-a.example:5060", "pcscf-b.example:5060"}
	service.mu.Unlock()
	// RP-report 488 requests a new path independently of port-s grace/backoff.
	service.portSReconnectGrace = time.Hour
	service.recordPortSRecoveryFailure(registerResponseErrorWithRetryAfter(t, "600"), time.Now())
	service.transport.SetSendFn(func(request string) error {
		service.transport.DeliverResponse(registerResponseForRequest(request, 488, nil))
		return nil
	})

	raw := inboundSMSRequest(t, imsSMSContentType,
		inboundRPData(t, 0x42, "+447700900123", "rejected report"))
	service.sendRPReportWithRetry(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x42), RPMR: 0x42,
	})

	select {
	case err := <-service.RegistrationErrors():
		if err == nil || !strings.Contains(err.Error(), "pcscf-b.example:5060") ||
			!strings.Contains(err.Error(), "fresh runtime required") {
			t.Fatalf("runtime recovery error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing runtime recovery request")
	}
	status := service.StatusCurrent()
	until := status.DeprioritizedPCSCF["pcscf-a.example:5060"]
	if until.Before(time.Now().Add(29 * time.Minute)) {
		t.Fatalf("rejected P-CSCF penalty expires too early: %s", until)
	}
	if service.RegState() == regRegistered || !strings.Contains(status.SignalingFailureReason, "mt_report_488") {
		t.Fatalf("rejected P-CSCF state = %+v", status)
	}
}

func TestMTReport488RecoveryIsCarrierScoped(t *testing.T) {
	service := newPortSSessionTestService(t, "2degrees_nz")
	service.mu.Lock()
	service.regState = regRegistered
	service.signalingReady = true
	service.mu.Unlock()

	service.triggerMTReportPCSCFRecovery(&rpReportRejectError{
		Status: 488, Registrar: "pcscf-a.example:5060",
	})

	select {
	case err := <-service.RegistrationErrors():
		t.Fatalf("other carrier requested runtime recovery: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	status := service.StatusCurrent()
	if len(status.DeprioritizedPCSCF) != 0 || service.RegState() != regRegistered {
		t.Fatalf("other carrier state changed = %+v", status)
	}
}

func TestVodafoneUKMTReportNon488DoesNotChangePCSCFPath(t *testing.T) {
	service := newPortSSessionTestService(t, vodafoneUKCarrierPresetID)
	service.mu.Lock()
	service.regState = regRegistered
	service.signalingReady = true
	service.mu.Unlock()

	service.triggerMTReportPCSCFRecovery(&rpReportRejectError{
		Status: 503, Registrar: "pcscf-a.example:5060",
	})

	select {
	case err := <-service.RegistrationErrors():
		t.Fatalf("non-488 report requested runtime recovery: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	status := service.StatusCurrent()
	if len(status.DeprioritizedPCSCF) != 0 || service.RegState() != regRegistered {
		t.Fatalf("non-488 report changed P-CSCF state = %+v", status)
	}
}

func TestMTReport488DoesNotTearDownAChangedPCSCFPath(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	service.cfg.CarrierPresetID = vodafoneUKCarrierPresetID
	// Only a report sent over the P-CSCF's own connection can carry a stale path.
	service.cfg.IMSRegisterTemplate.RPReportOverPortUS = true
	service.mu.Lock()
	service.registrar = "pcscf-a.example:5060"
	service.registrarCandidates = []string{"pcscf-a.example:5060", "pcscf-b.example:5060"}
	service.mu.Unlock()
	serviceConn, networkConn := net.Pipe()
	t.Cleanup(func() { _ = serviceConn.Close() })
	t.Cleanup(func() { _ = networkConn.Close() })
	service.recordPortSOpened(serviceConn, time.Now())

	service.mu.Lock()
	service.registrar = "pcscf-b.example:5060"
	service.registrarIndex = 1
	service.mu.Unlock()
	readResult := make(chan error, 1)
	go func() {
		request, err := readSIPStreamMessage(bufio.NewReader(networkConn))
		if err == nil {
			service.transport.DeliverResponse(registerResponseForRequest(request, 488, nil))
		}
		readResult <- err
	}()
	raw := inboundSMSRequest(t, imsSMSContentType,
		inboundRPData(t, 0x43, "+447700900123", "stale path"))
	service.sendRPReportWithRetry(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x43), PeerConn: serviceConn, RPMR: 0x43,
	})
	if err := <-readResult; err != nil {
		t.Fatalf("read RP report: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for service.pcscfRecoveryPending.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.pcscfRecoveryPending.Load() {
		t.Fatal("stale report recovery did not finish")
	}
	select {
	case err := <-service.RegistrationErrors():
		t.Fatalf("stale report recovery tore down the current path: %v", err)
	default:
	}
	status := service.StatusCurrent()
	if _, penalized := status.DeprioritizedPCSCF["pcscf-a.example:5060"]; !penalized {
		t.Fatal("the P-CSCF that returned 488 was not penalized")
	}
	if _, penalized := status.DeprioritizedPCSCF["pcscf-b.example:5060"]; penalized {
		t.Fatal("the replacement P-CSCF was incorrectly penalized")
	}
	if service.RegState() != regRegistered {
		t.Fatalf("replacement P-CSCF registration state = %s", service.RegState())
	}
}

package imscore

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newPortSTimeoutTestService(t *testing.T) *Service {
	t.Helper()
	s := newProtectedKeepaliveTestService(t)
	s.cfg.CarrierPresetID = vodafoneUKCarrierPresetID
	s.registrar = "pcscf-a.example:5060"
	s.registrarCandidates = []string{s.registrar}
	s.portSReconnectGrace = time.Hour // Tests explicitly advance each watchdog phase.
	s.portSRecoveryJitter = func(upper time.Duration) time.Duration { return upper / 2 }
	return s
}

func failPortSTimeoutValidation(t *testing.T, s *Service) {
	t.Helper()
	closeTestPortS(t, s, syscall.ETIMEDOUT)
	s.handleProtectedServerPushClosed()
	fireTestPortSWatch(t, s)
	if !s.portSRecoveryPending.Load() {
		t.Fatal("missing original-node REGISTER attempt")
	}
	s.reRegisterPending.Store(false)
	s.completePortSRecovery(nil, true)
	if s.pendingPortSTimeoutFailover().registrar != "" {
		t.Fatal("REGISTER 200 switched nodes without downlink validation")
	}
	fireTestPortSWatch(t, s)
	if s.pendingPortSTimeoutFailover().registrar == "" {
		t.Fatal("unsuccessful downlink validation did not arm replacement")
	}
}

func expirePortSTimeoutBackoff(t *testing.T, s *Service) {
	t.Helper()
	s.portSWatchMu.Lock()
	s.portSBackoff.retryAt = time.Now().Add(-time.Second)
	s.portSWatchMu.Unlock()
	fireTestPortSWatch(t, s)
}

func TestPortSTimeoutFailoverWaitsForValidationAndBackoff(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	failPortSTimeoutValidation(t, s)
	retryAt, waiting := s.portSRecoveryDeadline(time.Now())
	if !waiting || time.Until(retryAt) < 89*time.Second {
		t.Fatal("missing RFC 5626 retry delay")
	}
	s.completePortSRecovery(nil, true) // A periodic refresh must not postpone switching.
	if got, _ := s.portSRecoveryDeadline(time.Now()); !got.Equal(retryAt) {
		t.Fatal("periodic REGISTER 200 changed the established deadline")
	}
	fireTestPortSWatch(t, s)
	if s.RegState() != regRegistered || s.pcscfRecoveryPending.Load() || s.reRegisterPending.Load() {
		t.Fatal("replacement bypassed its retry deadline")
	}
	expirePortSTimeoutBackoff(t, s)
	select {
	case err := <-s.RegistrationErrors():
		if !strings.Contains(err.Error(), "fresh runtime required") {
			t.Fatal(err)
		}
	default:
		t.Fatal("single-candidate failure did not request candidate rediscovery")
	}
	entry := s.registrarPenalties.states(time.Now())[s.registrar]
	if entry.reason != portSTransportTimeoutFailure || entry.deprioritizedUntil.Before(time.Now().Add(29*time.Minute)) {
		t.Fatalf("timeout penalty = %+v", entry)
	}
}

func TestFailedPortSTimeoutValidationStopsAwaitingPeer(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	registration, registrationPeer := net.Pipe()
	t.Cleanup(func() { registration.Close(); registrationPeer.Close() })
	s.mu.Lock()
	s.registrationTCP = registration
	s.registrationTCPProtected = true
	s.mu.Unlock()
	var reasons []string
	s.SetOnSMSReadinessChanged(func(state SMSReadiness) { reasons = append(reasons, state.Reason) })
	failPortSTimeoutValidation(t, s)
	if !slices.Contains(reasons, smsReadyReasonPortSAwaitingPeer) {
		t.Fatalf("published reasons = %q, want the timeout reported as awaiting the peer first", reasons)
	}
	if last := reasons[len(reasons)-1]; last != smsReadyReasonReceiverNotReady {
		t.Fatalf("reason after failed validation = %q, want %q", last, smsReadyReasonReceiverNotReady)
	}
}

func TestPortSTimeoutFailoverIsCarrierAndErrorScoped(t *testing.T) {
	for _, preset := range []string{vodafoneUKCarrierPresetID, "2degrees_nz", "ctexcel", ""} {
		for _, closeErr := range []error{syscall.ETIMEDOUT, io.EOF, os.ErrDeadlineExceeded, syscall.ECONNRESET} {
			t.Run(preset+"/"+closeErr.Error(), func(t *testing.T) {
				s := newPortSTimeoutTestService(t)
				s.cfg.CarrierPresetID = preset
				closeTestPortS(t, s, closeErr)
				s.handleProtectedServerPushClosed()
				fireTestPortSWatch(t, s)
				s.completePortSRecovery(nil, true)
				fireTestPortSWatch(t, s)
				got := s.pendingPortSTimeoutFailover().registrar != ""
				want := preset == vodafoneUKCarrierPresetID &&
					(closeErr == syscall.ETIMEDOUT || closeErr == syscall.ECONNRESET)
				if got != want {
					t.Fatalf("timeout replacement pending = %t, want %t", got, want)
				}
			})
		}
	}
}

func TestVodafoneUKPeerResetWithoutReopenedDownlinkRequestsFreshPath(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	closeTestPortS(t, s, syscall.ECONNRESET)
	s.handleProtectedServerPushClosed()
	fireTestPortSWatch(t, s)
	if !s.portSRecoveryPending.Load() {
		t.Fatal("peer reset did not request same-P-CSCF REGISTER recovery")
	}
	s.reRegisterPending.Store(false)
	s.completePortSRecovery(nil, true)
	fireTestPortSWatch(t, s)
	pending := s.pendingPortSTimeoutFailover()
	if pending.registrar == "" || pending.failure != portSPeerResetFailure {
		t.Fatalf("missing post-REGISTER downlink validation failure: %+v", pending)
	}
	expirePortSTimeoutBackoff(t, s)
	select {
	case err := <-s.RegistrationErrors():
		if !strings.Contains(err.Error(), "fresh runtime required") {
			t.Fatal(err)
		}
	default:
		t.Fatal("unrecovered peer reset kept refreshing the same P-CSCF")
	}
}

func TestClosedPortSCleanupCannotHideValidatedTimeoutFailure(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	s.recordPortSOpened(client, time.Now())
	if !s.trackProtectedConnection(client) {
		t.Fatal("track port-s")
	}
	s.recordPortSClosed(client, syscall.ETIMEDOUT, time.Now())
	s.portSSessionMu.Lock()
	s.portSSession.timeoutRecovery.validationFailed = true
	s.portSSessionMu.Unlock()
	pending := s.pendingPortSTimeoutFailover()
	_, _, changed := s.commitPortSFailover(pending.registrar, portSFailoverCause{
		reason: pending.failure, generation: pending.generation,
	})
	if !changed {
		t.Fatal("closed port-s awaiting map cleanup canceled a validated failure")
	}
}

func TestPortSTimeoutFailedRegisterDoesNotArmReplacement(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	closeTestPortS(t, s, syscall.ETIMEDOUT)
	s.handleProtectedServerPushClosed()
	fireTestPortSWatch(t, s)
	s.reRegisterPending.Store(false)
	s.completePortSRecovery(registerResponseErrorWithRetryAfter(t, "600"), true)
	if s.pendingPortSTimeoutFailover().registrar != "" {
		t.Fatal("REGISTER rejection was mistaken for failed post-200 validation")
	}
	retryAt, waiting := s.portSRecoveryDeadline(time.Now())
	if !waiting || time.Until(retryAt) < 599*time.Second {
		t.Fatal("REGISTER Retry-After was not preserved")
	}
	fireTestPortSWatch(t, s)
	if s.reRegisterPending.Load() || s.pcscfRecoveryPending.Load() {
		t.Fatal("REGISTER rejection bypassed Retry-After")
	}
}

func TestPortSTimeoutRecoveryCanceledByDownlinkOrLifecycle(t *testing.T) {
	for _, event := range []string{"port-s", "inbound SIP", "stop", "new registrar", "reset transport"} {
		t.Run(event, func(t *testing.T) {
			s := newPortSTimeoutTestService(t)
			failPortSTimeoutValidation(t, s)
			s.portSWatchMu.Lock()
			generation := s.portSWatchGeneration
			s.portSWatchTimer.Stop()
			s.portSBackoff.retryAt = time.Now().Add(-time.Second)
			s.portSWatchMu.Unlock()
			registrar := s.registrar
			switch event {
			case "port-s":
				openFinalizationTestPortS(t, s)
				if s.portSOnDemandObserved.Load() {
					t.Fatal("late post-REGISTER reconnect was mistaken for on-demand capability")
				}
			case "inbound SIP":
				conn := newRecoveryCompletionPortS(t)
				s.registrationTCP, s.registrationTCPProtected = conn, true
				s.recordCurrentDownlinkRequest(conn, s.captureDownlinkCheckpoint())
				if !s.SMSReadiness().Ready {
					t.Fatal("current protected downlink did not restore SMS readiness")
				}
			case "stop":
				s.StopCurrent()
			case "new registrar":
				s.registrar = "pcscf-b.example:5060"
			case "reset transport":
				s.resetPortSRecoveryKnowledge()
			}
			s.portSReconnectWatchFired(generation, registrar)
			if s.pcscfRecoveryPending.Load() || len(s.registrarPenalties.states(time.Now())) != 0 {
				t.Fatal("stale timeout attempt penalized or replaced the current binding")
			}
		})
	}
}

func TestPortSTimeoutIgnoresDownlinkFromRetiredConnection(t *testing.T) {
	s := newPortSTimeoutTestService(t)
	old := openFinalizationTestPortS(t, s)
	s.markPortSLocalClose(old)
	s.untrackProtectedConnection(old)
	failPortSTimeoutValidation(t, s)
	s.recordCurrentDownlinkRequest(old, s.captureDownlinkCheckpoint())
	s.recordCurrentDownlinkRequest(nil, s.captureDownlinkCheckpoint())
	if s.pendingPortSTimeoutFailover().registrar == "" || s.portSTimeoutDownlinkProven() {
		t.Fatal("retired connection canceled the new timeout recovery")
	}
}

func TestPortSTimeoutWatchdogSwitchesToAvailableRegistrar(t *testing.T) {
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
	secondSeen := make(chan string, 8)
	go serveRegisterStatus(second, 200, secondSeen)
	cfg := registerTransportTestConfig("udp", first.LocalAddr().String()+";"+second.LocalAddr().String())
	cfg.CarrierPresetID = vodafoneUKCarrierPresetID
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.StopCurrent()
	s.portSReconnectGrace = time.Hour
	s.registrar = first.LocalAddr().String()
	s.registrarCandidates = []string{s.registrar, second.LocalAddr().String()}
	s.regState = regRegistered
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	failPortSTimeoutValidation(t, s)
	expirePortSTimeoutBackoff(t, s)
	select {
	case <-secondSeen:
	case <-ctx.Done():
		t.Fatal("watchdog did not register through the alternate P-CSCF")
	}
	if s.currentPortSRecoveryRegistrar() != second.LocalAddr().String() {
		t.Fatal("recovery remained on the original P-CSCF")
	}
}

package imscore

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TS 33.203 §7.1 item 1 (TCP case): the P-CSCF sets up a connection to
// port_us before sending a request, and NOTE 6 does not require keeping it.
// Once a REGISTER after the timeout succeeds on a carrier known to reopen a
// timed-out port-s (Lebara UK), the UE waits for it instead of re-registering;
// that list is our choice. peerSeen marks an earlier peer reconnect, which a
// timeout does not rely on. timedOutPortS closes port-s by timeout and waits
// for the recovery REGISTER request.
func timedOutPortS(t *testing.T, preset string, peerSeen bool) *Service {
	t.Helper()
	service := newProtectedKeepaliveTestService(t)
	service.cfg.CarrierPresetID = preset
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	service.registrationTCP, service.registrationTCPProtected = registration, true
	service.portSOnDemandObserved.Store(peerSeen)
	closeTestPortS(t, service, syscall.ETIMEDOUT)
	service.handleProtectedServerPushClosed()
	waitForPortSCondition(t, func() bool { return service.reRegisterPending.Load() })
	if !service.portSRecoveryPending.Load() {
		t.Fatal("timeout did not request a REGISTER first")
	}
	return service
}

func TestTimedOutPortSAwaitsPeerAfterSuccessfulRegister(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, true)
	service.completePortSRecovery(nil, true)
	if !service.canAwaitPeerAfterTimeout() || !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("successful REGISTER after timeout did not await the peer")
	}
	service.portSWatchMu.Lock()
	timer := service.portSWatchTimer
	service.portSWatchMu.Unlock()
	if timer != nil {
		t.Fatal("awaiting the peer still scheduled REGISTER recovery")
	}
	if _, waiting := service.portSRecoveryDeadline(time.Now()); waiting {
		t.Fatal("awaiting the peer kept a recovery backoff")
	}
	if got := service.SMSReadiness(); !got.Ready || !got.HealthReady || got.Reason != smsReadyReasonAwaitingPeerPortS {
		t.Fatalf("readiness while awaiting = %+v", got)
	}

	// A periodic refresh during the await neither re-arms recovery nor
	// re-enters the await.
	service.completePortSRecovery(nil, true)
	service.portSWatchMu.Lock()
	timer = service.portSWatchTimer
	service.portSWatchMu.Unlock()
	if timer != nil || !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("periodic refresh during the await restarted recovery")
	}

	reopenTestPortS(t, service)
	if service.portSTimeoutAwaitEntered.Load() || service.portSReconnectWaiting.Load() {
		t.Fatal("peer reopen did not end the await")
	}
}

func TestTimedOutPortSAwaitNeedsRegisterAfterThisClose(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	service.cfg.CarrierPresetID = lebaraUKCarrierPresetID
	service.portSOnDemandObserved.Store(true)
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	service.recordPortSOpened(client, time.Now())
	if !service.trackProtectedConnection(client) {
		t.Fatal("track port-s")
	}
	// A REGISTER completion that raced the reopen left this behind.
	service.portSRecoveryAwaitingFlow.Store(true)
	service.recordPortSClosed(client, syscall.ETIMEDOUT, time.Now())
	service.untrackProtectedConnection(client)
	service.handleProtectedServerPushClosed()
	if service.canAwaitPeerAfterTimeout() {
		t.Fatal("stale REGISTER success skipped the REGISTER for this timeout")
	}
	waitForPortSCondition(t, func() bool { return service.reRegisterPending.Load() })
}

func TestTimedOutPortSAwaitEndsWhenRegisterFails(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, true)
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("did not enter the await")
	}
	service.completePortSRecovery(errors.New("503"), true)
	if service.canAwaitPeerAfterTimeout() || service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("failed REGISTER kept awaiting the peer")
	}
	if _, waiting := service.portSRecoveryDeadline(time.Now()); !waiting {
		t.Fatal("failed REGISTER during the await did not resume recovery")
	}
	// The recovery REGISTER after the backoff succeeds: wait again.
	service.portSWatchMu.Lock()
	service.portSBackoff.retryAt = time.Now()
	service.portSWatchMu.Unlock()
	fireTestPortSWatch(t, service)
	if !service.portSRecoveryPending.Load() {
		t.Fatal("backoff did not send the recovery REGISTER")
	}
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("successful recovery REGISTER did not resume the await")
	}
}

func TestTimedOutPortSAwaitSkipsVodafoneValidation(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	service.cfg.CarrierPresetID = vodafoneUKCarrierPresetID
	service.portSOnDemandObserved.Store(true)
	closeTestPortS(t, service, syscall.ETIMEDOUT)
	service.handleProtectedServerPushClosed()
	fireTestPortSWatch(t, service)
	if !service.portSRecoveryPending.Load() {
		t.Fatal("Vodafone UK timeout did not request a REGISTER")
	}
	service.completePortSRecovery(nil, true)
	if service.canAwaitPeerAfterTimeout() || service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("Vodafone UK timeout bypassed its downlink validation")
	}
	fireTestPortSWatch(t, service)
	if _, waiting := service.portSRecoveryDeadline(time.Now()); !waiting {
		t.Fatal("Vodafone UK missing downlink did not back off toward failover")
	}
}

func reopenTestPortS(t *testing.T, service *Service) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	if !service.trackProtectedPortSConnection(client, time.Now()) {
		t.Fatal("track reopened port-s")
	}
}

func TestOlderRegisterCompletionKeepsNewerTimeoutRecovery(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, true)
	// Before the REGISTER completes, the peer reopens port-s and that
	// connection times out too.
	closeTestPortS(t, service, syscall.ETIMEDOUT)
	service.handleProtectedServerPushClosed()
	service.completePortSRecovery(nil, true)
	if service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("an older REGISTER completion entered the await for a newer timeout")
	}
	if service.awaitingPeerAfterTimeout() || !service.protectedSMSPushRequiredLocked() {
		t.Fatal("a refused await still suppressed recovery or readiness checks")
	}
	// Lebara UK probes the newer timeout with another REGISTER; the watch
	// for it fires at once.
	waitForPortSCondition(t, func() bool { return service.portSRecoveryPending.Load() })
}

func TestClearingStaleAwaitRepublishesReadiness(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	service.registrationTCP = registration
	service.registrationTCPProtected = true
	service.portSOnDemandObserved.Store(true)
	client, other := net.Pipe()
	t.Cleanup(func() { client.Close(); other.Close() })
	service.recordPortSOpened(client, time.Now())
	if !service.trackProtectedConnection(client) {
		t.Fatal("track port-s")
	}
	// A reopen that raced the await entry left the await marked.
	service.portSTimeoutAwaitEntered.Store(true)
	service.recordPortSClosed(client, syscall.ETIMEDOUT, time.Now())
	service.untrackProtectedConnection(client)
	var last SMSReadiness
	service.SetOnSMSReadinessChanged(func(readiness SMSReadiness) { last = readiness })
	if !last.Ready {
		t.Fatal("test setup: stale await should still report ready before the close is handled")
	}
	service.handleProtectedServerPushClosed()
	if last.Ready {
		t.Fatalf("clearing the stale await did not republish readiness: %+v", last)
	}
}

func TestTimedOutPortSAwaitSkipsOutboundFlow(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, true)
	service.sipOutboundFlow.Store(true)
	service.completePortSRecovery(nil, true)
	if service.canAwaitPeerAfterTimeout() || service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("an RFC 5626 outbound flow skipped its flow recovery")
	}
}

func TestLebaraFirstTimedOutPortSAwaitsPeerAfterSuccessfulRegister(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	if got := service.SMSReadiness(); !got.Ready || !got.HealthReady || got.Reason != smsReadyReasonProbingPortS {
		t.Fatalf("readiness before the probe REGISTER answered = %+v", got)
	}
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("first timeout on Lebara UK did not await the peer after REGISTER 200")
	}
	if got := service.SMSReadiness(); !got.Ready || got.Reason != smsReadyReasonAwaitingPeerPortS {
		t.Fatalf("readiness while awaiting = %+v", got)
	}
	reopenTestPortS(t, service)
	if service.portSTimeoutAwaitEntered.Load() || !service.portSOnDemandObserved.Load() {
		t.Fatal("peer reopen during the await was not recorded as on-demand")
	}
}

// Regression guard, not a spec requirement: carriers outside the list keep
// main's validation after a timeout, even after an earlier peer reconnect.
func TestTimedOutPortSOutsideTheListKeepsValidation(t *testing.T) {
	for _, preset := range []string{"", oneNZCarrierPresetID, twoDegreesNZCarrierPresetID} {
		for _, peerSeen := range []bool{false, true} {
			service := timedOutPortS(t, preset, peerSeen)
			if got := service.SMSReadiness(); got.Ready || got.HealthReady {
				t.Fatalf("%q peer seen %t: timeout reported ready before REGISTER answered: %+v", preset, peerSeen, got)
			}
			service.completePortSRecovery(nil, true)
			if service.canAwaitPeerAfterTimeout() || service.portSTimeoutAwaitEntered.Load() {
				t.Fatalf("%q peer seen %t: timeout awaited the peer", preset, peerSeen)
			}
			service.portSWatchMu.Lock()
			timer := service.portSWatchTimer
			service.portSWatchMu.Unlock()
			if timer == nil {
				t.Fatalf("%q peer seen %t: no port-s validation scheduled after REGISTER", preset, peerSeen)
			}
		}
	}
}

func TestLebaraFirstTimeoutReportsFailedRegister(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	service.completePortSRecovery(errors.New("503"), true)
	if service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("failed REGISTER entered the await")
	}
	if got := service.SMSReadiness(); got.Ready || got.HealthReady {
		t.Fatalf("failed REGISTER after the timeout still reported ready: %+v", got)
	}
}

func TestTimedOutPortSAwaitRevokedWhenRefreshNegotiatesOutbound(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, true)
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("did not enter the await")
	}
	service.sipOutboundFlow.Store(true)
	service.completePortSRecovery(nil, true)
	if service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("await survived a refresh that negotiated an outbound flow")
	}
	service.portSWatchMu.Lock()
	timer := service.portSWatchTimer
	service.portSWatchMu.Unlock()
	if timer == nil {
		t.Fatal("revoked await did not schedule port-s validation")
	}
	if got := service.SMSReadiness(); got.Reason == smsReadyReasonAwaitingPeerPortS {
		t.Fatalf("readiness still reports the await: %+v", got)
	}
}

// lebaraTimeoutDuringRefresh leaves Lebara UK awaiting the peer, then times
// port-s out again while a periodic REGISTER of the live binding is in flight
// (registerMu held, regRegistering).
func lebaraTimeoutDuringRefresh(t *testing.T) *Service {
	t.Helper()
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("did not enter the await")
	}
	service.registerMu.Lock()
	service.mu.Lock()
	service.regSession.expires = time.Hour
	service.lastRegisterOKAt = time.Now()
	service.regState = regRegistering
	service.mu.Unlock()
	closeTestPortS(t, service, syscall.ETIMEDOUT)
	service.handleProtectedServerPushClosed()
	if got := service.SMSReadiness(); !got.Ready || got.Reason != smsReadyReasonProbingPortS {
		service.registerMu.Unlock()
		t.Fatalf("readiness after a timeout during a REGISTER = %+v", got)
	}
	return service
}

func TestLebaraTimeoutDuringRefreshProbesBeforeAwaiting(t *testing.T) {
	service := lebaraTimeoutDuringRefresh(t)
	service.portSRecoveryPending.Store(false)
	// The periodic REGISTER answers 200, but it was sent before this close.
	service.completePortSRecovery(nil, true)
	setTestRegState(service, regRegistered)
	service.registerMu.Unlock()
	if service.portSTimeoutAwaitEntered.Load() || service.portSMissingAfterRegister.Load() {
		t.Fatal("a REGISTER sent before the close entered the await or failed validation")
	}
	waitForPortSCondition(t, func() bool { return service.portSRecoveryPending.Load() })
	if got := service.SMSReadiness(); !got.Ready {
		t.Fatalf("readiness while the probe REGISTER is in flight = %+v", got)
	}
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("the probe REGISTER after the close did not enter the await")
	}
}

func TestLebaraTimeoutDuringRefreshEndsGraceWhenRefreshFails(t *testing.T) {
	service := lebaraTimeoutDuringRefresh(t)
	// A failed refresh that keeps the binding returns to registered first.
	setTestRegState(service, regRegistered)
	service.completePortSRecovery(errors.New("503"), true)
	service.registerMu.Unlock()
	if got := service.SMSReadiness(); got.Ready || got.HealthReady {
		t.Fatalf("failed REGISTER after the timeout still reported ready: %+v", got)
	}
}

func TestPeerReopenDuringRegisterEndsAwaitWithoutOnDemandEvidence(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	service.completePortSRecovery(nil, true)
	if !service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("did not enter the await")
	}
	setTestRegState(service, regReregister)
	reopenTestPortS(t, service)
	if service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("peer reopen did not end the await")
	}
	if service.portSOnDemandObserved.Load() {
		t.Fatal("a reopen during a REGISTER counted as on-demand evidence")
	}
}

// Regression guard, not a spec requirement: an RFC 5626 outbound flow on
// Lebara UK keeps its earlier readiness and recovery after a timeout.
func TestLebaraOutboundTimeoutKeepsRecovery(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	service.cfg.CarrierPresetID = lebaraUKCarrierPresetID
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	service.registrationTCP, service.registrationTCPProtected = registration, true
	service.sipOutboundFlow.Store(true)
	closeTestPortS(t, service, syscall.ETIMEDOUT)
	service.handleProtectedServerPushClosed()
	waitForPortSCondition(t, func() bool { return service.reRegisterPending.Load() })
	if got := service.SMSReadiness(); got.Ready || got.HealthReady {
		t.Fatalf("outbound timeout reported ready: %+v", got)
	}
	service.completePortSRecovery(nil, true)
	if service.portSTimeoutAwaitEntered.Load() {
		t.Fatal("outbound timeout awaited the peer")
	}
	service.portSWatchMu.Lock()
	timer := service.portSWatchTimer
	service.portSWatchMu.Unlock()
	if timer == nil {
		t.Fatal("outbound timeout did not schedule port-s validation")
	}
}

func TestLebaraTimeoutDuringRecoveryBackoffGetsNoGrace(t *testing.T) {
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	service.completePortSRecovery(errors.New("503"), true)
	if _, waiting := service.portSRecoveryDeadline(time.Now()); !waiting {
		t.Fatal("failed probe REGISTER did not back off")
	}
	// The peer reopens port-s during a REGISTER, which keeps the backoff, and
	// it times out again before the backoff ends.
	var (
		publishedMu sync.Mutex
		published   SMSReadiness
	)
	service.SetOnSMSReadinessChanged(func(readiness SMSReadiness) {
		publishedMu.Lock()
		published = readiness
		publishedMu.Unlock()
	})
	setTestRegState(service, regReregister)
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	service.recordPortSOpened(client, time.Now())
	if !service.trackProtectedConnection(client) {
		t.Fatal("track port-s")
	}
	setTestRegState(service, regRegistered)
	service.recordPortSClosed(client, syscall.ETIMEDOUT, time.Now())
	service.untrackProtectedConnection(client)
	publishedMu.Lock()
	got := published
	publishedMu.Unlock()
	if !got.Ready {
		t.Fatalf("the close itself did not publish the timeout grace: %+v", got)
	}
	service.handleProtectedServerPushClosed()
	publishedMu.Lock()
	got = published
	publishedMu.Unlock()
	if got.Ready || got.HealthReady {
		t.Fatalf("timeout during the backoff published ready with no probe REGISTER: %+v", got)
	}
}

func setTestRegState(service *Service, state string) {
	service.mu.Lock()
	service.regState = state
	service.mu.Unlock()
}

func TestLebaraTimeoutGraceEndsOnAnotherRegistrationPath(t *testing.T) {
	// A new registration on the same P-CSCF, after the await, before port-s.
	service := timedOutPortS(t, lebaraUKCarrierPresetID, false)
	service.completePortSRecovery(nil, true)
	service.resetPortSRecoveryKnowledge()
	if got := service.SMSReadiness(); got.Ready || got.Reason == smsReadyReasonProbingPortS {
		t.Fatalf("new registration kept the old timeout grace: %+v", got)
	}

	// Another P-CSCF while the probe REGISTER is still in flight.
	service = timedOutPortS(t, lebaraUKCarrierPresetID, false)
	if got := service.SMSReadiness(); !got.Ready {
		t.Fatalf("readiness before the probe answered = %+v", got)
	}
	service.mu.Lock()
	service.registrar = "pcscf-b.ims.example"
	service.mu.Unlock()
	if got := service.SMSReadiness(); got.Ready || got.Reason == smsReadyReasonProbingPortS {
		t.Fatalf("another P-CSCF kept the old timeout grace: %+v", got)
	}
}

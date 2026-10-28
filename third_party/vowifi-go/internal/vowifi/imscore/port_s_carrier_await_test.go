package imscore

import (
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func TestCarrierPresetsAwaitPortSWithoutPriorReconnect(t *testing.T) {
	for _, preset := range []string{globePHCarrierPresetID, ditoPHCarrierPresetID, twoDegreesNZCarrierPresetID, oneNZCarrierPresetID, ""} {
		for _, closeErr := range []error{io.EOF, syscall.ECONNRESET} {
			t.Run(preset+"/"+closeErr.Error(), func(t *testing.T) {
				service := newCarrierAwaitTestService(t, preset)
				closeIdleTestPortS(t, service, closeErr, 20*time.Minute)
				service.handleProtectedServerPushClosed()
				timer := stopTestPortSWatch(service)
				eofAwait := preset == globePHCarrierPresetID || preset == ditoPHCarrierPresetID || preset == twoDegreesNZCarrierPresetID
				await := (eofAwait && closeErr == io.EOF) ||
					(preset == oneNZCarrierPresetID && closeErr == syscall.ECONNRESET)
				if (timer == nil) != await {
					t.Fatalf("awaited peer reconnect = %t, want %t", timer == nil, await)
				}
				if got := service.SMSReadiness(); got.Ready != await {
					t.Fatalf("readiness = %+v, want ready %t", got, await)
				}
			})
		}
	}
}

func TestOneNZPortSResetAwaitsAtAnyIdle(t *testing.T) {
	for _, idle := range []time.Duration{time.Minute, 13 * time.Minute, 20 * time.Minute} {
		t.Run(idle.String(), func(t *testing.T) {
			service := newCarrierAwaitTestService(t, oneNZCarrierPresetID)
			closeIdleTestPortS(t, service, syscall.ECONNRESET, idle)
			service.handleProtectedServerPushClosed()
			if stopTestPortSWatch(service) != nil {
				t.Fatal("One NZ reset scheduled a REGISTER instead of awaiting the peer")
			}
			if got := service.SMSReadiness(); !got.Ready {
				t.Fatalf("readiness = %+v, want ready while awaiting the peer", got)
			}
		})
	}
}

func TestOtherPresetsRecoverPeerResetAfterPeerReconnect(t *testing.T) {
	for _, preset := range []string{"", globePHCarrierPresetID, ditoPHCarrierPresetID, twoDegreesNZCarrierPresetID, vodafoneUKCarrierPresetID} {
		t.Run(preset, func(t *testing.T) {
			service := newCarrierAwaitTestService(t, preset)
			service.portSOnDemandObserved.Store(true)
			closeIdleTestPortS(t, service, syscall.ECONNRESET, 20*time.Minute)
			service.handleProtectedServerPushClosed()
			if stopTestPortSWatch(service) == nil {
				t.Fatal("peer reset awaited the peer outside the One NZ preset")
			}
		})
	}
}

func TestCarrierAwaitBacksOffWhenRegisterDoesNotRestorePortS(t *testing.T) {
	for _, tc := range []struct {
		name     string
		preset   string
		closeErr error
		observed bool
	}{
		{"one-nz/first", oneNZCarrierPresetID, syscall.ECONNRESET, false},
		{"one-nz/after-peer-reconnect", oneNZCarrierPresetID, syscall.ECONNRESET, true},
		{"globe-eof/first", globePHCarrierPresetID, io.EOF, false},
		{"dito-eof/first", ditoPHCarrierPresetID, io.EOF, false},
		{"2degrees-eof/first", twoDegreesNZCarrierPresetID, io.EOF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newCarrierAwaitTestService(t, tc.preset)
			service.portSOnDemandObserved.Store(tc.observed)
			closeIdleTestPortS(t, service, tc.closeErr, 20*time.Minute)
			service.handleProtectedServerPushClosed()
			service.completePortSRecovery(nil, true)
			if !service.portSRecoveryAwaitingFlow.Load() {
				t.Fatal("periodic REGISTER did not start port-s validation")
			}
			fireTestPortSWatch(t, service)
			if _, waiting := service.portSRecoveryDeadline(time.Now()); !waiting {
				t.Fatal("port-s still missing after REGISTER did not enter backoff")
			}
			// The backoff ends in a REGISTER, not in another open-ended wait.
			service.clearPortSRecoveryDeadline()
			fireTestPortSWatch(t, service)
			if !service.portSRecoveryPending.Load() {
				t.Fatal("expired backoff kept waiting instead of sending REGISTER")
			}
			stopTestPortSWatch(service)
		})
	}
}

// Replays the One NZ sequence seen on 10-16: a reset after 13 minutes idle,
// a periodic REGISTER while port-s is down, then the peer's own reconnect.
func TestOneNZFieldResetCycle(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "after-peer-reconnect"}[observed], func(t *testing.T) {
			runOneNZFieldResetCycle(t, observed)
		})
	}
}

func runOneNZFieldResetCycle(t *testing.T, observed bool) {
	service := newCarrierAwaitTestService(t, oneNZCarrierPresetID)
	service.portSOnDemandObserved.Store(observed)
	closeIdleTestPortS(t, service, syscall.ECONNRESET, 13*time.Minute)
	service.handleProtectedServerPushClosed()
	if stopTestPortSWatch(service) != nil {
		t.Fatal("first reset scheduled a REGISTER")
	}
	service.completePortSRecovery(nil, true)
	fireTestPortSWatch(t, service)
	if !service.portSMissingAfterRegister.Load() {
		t.Fatal("missing port-s after REGISTER was not recorded")
	}
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	if !service.trackProtectedPortSConnection(client, time.Now()) {
		t.Fatal("track reopened port-s")
	}
	if service.portSMissingAfterRegister.Load() {
		t.Fatal("peer reconnect left the REGISTER failure in place")
	}
	service.portSWatchMu.Lock()
	failures := service.portSBackoff.failures
	service.portSWatchMu.Unlock()
	if failures != 0 {
		t.Fatalf("peer reconnect left %d backoff failures in place", failures)
	}
	service.recordPortSClosed(client, syscall.ECONNRESET, time.Now())
	service.untrackProtectedConnection(client)
	service.handleProtectedServerPushClosed()
	if stopTestPortSWatch(service) != nil {
		t.Fatal("next reset scheduled a REGISTER")
	}
}

func TestPortSReopenDuringBackoffDoesNotLeaveRegisterFailure(t *testing.T) {
	service := newCarrierAwaitTestService(t, oneNZCarrierPresetID)
	closeIdleTestPortS(t, service, syscall.ECONNRESET, 20*time.Minute)
	seen := service.capturePortSSession().generation
	// The peer reconnects after the watch saw no connection but before it
	// recorded the failure.
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	if !service.trackProtectedPortSConnection(client, time.Now()) {
		t.Fatal("track reopened port-s")
	}
	service.backoffMissingPortSAfterRegister(seen)
	if stopTestPortSWatch(service) != nil {
		t.Fatal("stale watch scheduled a REGISTER")
	}
	if service.portSMissingAfterRegister.Load() {
		t.Fatal("stale watch marked the reopened port-s as missing after REGISTER")
	}
	service.portSWatchMu.Lock()
	backoff := service.portSBackoff
	service.portSWatchMu.Unlock()
	if backoff.failures != 0 || !backoff.retryAt.IsZero() {
		t.Fatalf("stale watch recorded backoff %+v", backoff)
	}
}

func TestPortSReopenWhileRecordingBackoffDropsTheFailure(t *testing.T) {
	service := newCarrierAwaitTestService(t, oneNZCarrierPresetID)
	closeIdleTestPortS(t, service, syscall.ECONNRESET, 20*time.Minute)
	seen := service.capturePortSSession().generation
	// The peer reconnects after the first generation check, while the failure
	// is being recorded.
	service.portSRecoveryJitter = func(upper time.Duration) time.Duration {
		service.portSSessionMu.Lock()
		service.portSSession.generation++
		service.portSSessionMu.Unlock()
		return upper / 2
	}
	service.backoffMissingPortSAfterRegister(seen)
	if stopTestPortSWatch(service) != nil {
		t.Fatal("stale failure scheduled a REGISTER")
	}
	service.portSWatchMu.Lock()
	backoff := service.portSBackoff
	service.portSWatchMu.Unlock()
	if backoff.failures != 0 || !backoff.retryAt.IsZero() {
		t.Fatalf("stale failure left backoff %+v", backoff)
	}
	if service.portSMissingAfterRegister.Load() {
		t.Fatal("stale failure marked port-s missing after REGISTER")
	}
}

func TestOneNZPeerReconnectKeepsRetryAfterFloor(t *testing.T) {
	service := newCarrierAwaitTestService(t, oneNZCarrierPresetID)
	service.portSOnDemandObserved.Store(true)
	closeIdleTestPortS(t, service, syscall.ECONNRESET, 20*time.Minute)
	service.handleProtectedServerPushClosed()
	floor := time.Now().Add(10 * time.Minute)
	service.portSWatchMu.Lock()
	service.portSBackoff = portSRecoveryBackoff{
		registrar: service.currentPortSRecoveryRegistrar(), failures: 3, retryAt: floor,
	}
	service.portSWatchMu.Unlock()
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	if !service.trackProtectedPortSConnection(client, time.Now()) {
		t.Fatal("track reopened port-s")
	}
	service.portSWatchMu.Lock()
	backoff := service.portSBackoff
	service.portSWatchMu.Unlock()
	if backoff.failures != 0 || !backoff.retryAt.Equal(floor) {
		t.Fatalf("backoff after peer reconnect = %+v, want failures reset and Retry-After floor kept", backoff)
	}
}

func newCarrierAwaitTestService(t *testing.T, preset string) *Service {
	t.Helper()
	service := newProtectedKeepaliveTestService(t)
	service.cfg.CarrierPresetID = preset
	service.portSReconnectGrace = time.Hour
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	service.registrationTCP = registration
	service.registrationTCPProtected = true
	return service
}

func closeIdleTestPortS(t *testing.T, service *Service, err error, idle time.Duration) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() { client.Close(); peer.Close() })
	now := time.Now()
	service.recordPortSOpened(client, now.Add(-idle))
	if !service.trackProtectedPortSConnection(client, time.Now()) {
		t.Fatal("track port-s")
	}
	service.recordPortSClosed(client, err, now)
	service.untrackProtectedConnection(client)
}

func stopTestPortSWatch(service *Service) *time.Timer {
	service.portSWatchMu.Lock()
	timer := service.portSWatchTimer
	service.portSWatchMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	return timer
}

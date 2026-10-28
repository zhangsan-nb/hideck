package device

import (
	"testing"
	"time"

	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
)

func replaceWorkerForLifecycleTest(p *Pool, worker *Worker) {
	p.assignWorkerGeneration(worker)
	p.mu.Lock()
	p.workers[worker.ID] = worker
	p.mu.Unlock()
}

func TestAbandonDeviceCancelsWorkerLifecycleTimers(t *testing.T) {
	p, port := testModemVoicePort(t)
	deviceID := port.worker.ID
	owner := p.captureWorkerLifecycleOwner(deviceID)
	simTimer := time.AfterFunc(time.Hour, func() {})
	wakeupTimer := time.AfterFunc(time.Hour, func() {})
	t.Cleanup(func() {
		simTimer.Stop()
		wakeupTimer.Stop()
	})

	p.simEventTimers[deviceID] = simTimer
	p.deviceEventWakeups[deviceID] = &deviceEventRecoverWakeup{
		timer: wakeupTimer, owner: owner, sources: map[string]struct{}{"test": {}},
	}
	if err := p.AbandonDevice(deviceID); err != nil {
		t.Fatal(err)
	}
	if p.simEventTimers[deviceID] != nil || p.deviceEventWakeups[deviceID] != nil {
		t.Fatal("abandoned device retained delayed lifecycle callbacks")
	}
}

func TestStaleDeviceEventCannotFlushReplacementWakeup(t *testing.T) {
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev"}
	p.AttachWorkerForTest(old)
	stale := &deviceEventRecoverWakeup{
		owner: p.captureWorkerLifecycleOwner(old.ID), sources: map[string]struct{}{"old": {}},
	}
	replacement := &Worker{ID: old.ID}
	replaceWorkerForLifecycleTest(p, replacement)
	current := &deviceEventRecoverWakeup{
		owner: p.captureWorkerLifecycleOwner(replacement.ID), sources: map[string]struct{}{"new": {}},
	}
	p.deviceEventWakeups[old.ID] = current

	p.flushDesiredVoWiFiRecoverFromDeviceEvent(old.ID, stale)
	if p.deviceEventWakeups[old.ID] != current {
		t.Fatal("stale device event consumed the replacement wakeup")
	}
}

func TestVoWiFiReadyStartRejectsReplacementWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev"}
	p.AttachWorkerForTest(old)
	start := p.captureVoWiFiReadyStart(old)

	replacement := &Worker{ID: old.ID}
	replaceWorkerForLifecycleTest(p, replacement)
	p.voWiFiHost().InvalidateRuntime(old.ID, "worker_replaced")
	if err := p.enableVoWiFiWhenReady(start, time.Millisecond, "test"); err == nil {
		t.Fatal("stale automatic VoWiFi start accepted the replacement worker")
	}
}

func TestOnDemandCallCleanupRejectsReplacementWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	network := &fakeController{connected: true}
	old := &Worker{ID: "dev", netOverride: network}
	p.AttachWorkerForTest(old)
	checkOwner := p.outboundOwnerCheck(old)
	replaceWorkerForLifecycleTest(p, &Worker{ID: old.ID})

	p.cleanupOnDemandCellularCall(old, checkOwner)
	if !network.connected {
		t.Fatal("old call cleanup disconnected the replaced worker resources")
	}
}

func TestRetiredWorkerCannotReclaimPersistedSIM(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev", Pool: p, Config: config.DeviceConfig{ModemIMEI: "imei"}}
	old.state.Identity.IMEI = "imei"
	setWorkerSMSIdentity(old, SMSIdentity{ICCID: "card-old", IMSI: "imsi-old"})
	p.AttachWorkerForTest(old)
	p.PersistRuntimeState(old)
	p.PersistIdentityState(old)

	replacement := &Worker{ID: old.ID, Pool: p, Config: old.Config}
	replacement.state.Identity.IMEI = "imei"
	setWorkerSMSIdentity(replacement, SMSIdentity{ICCID: "card-new", IMSI: "imsi-new"})
	replaceWorkerForLifecycleTest(p, replacement)
	p.PersistIdentityState(replacement)
	p.PersistIdentityState(old)

	if got := db.CurrentICCIDForDevice(old.ID); got != "card-new" {
		t.Fatalf("retired worker restored ICCID %q", got)
	}
}

func TestStaleBootstrapGenerationCannotReplacePublishedWorkerGeneration(t *testing.T) {
	p := NewPool(&config.Config{})
	current := &Worker{ID: "dev"}
	p.assignWorkerGeneration(current)
	if err := p.registerWorkerStarting(current); err != nil {
		t.Fatal(err)
	}

	stale := &Worker{ID: current.ID}
	p.assignWorkerGeneration(stale)
	p.transportRecovery.mu.Lock()
	published := p.transportRecovery.workerGenerations[current.ID]
	p.transportRecovery.mu.Unlock()
	if published != current.generation {
		t.Fatalf("stale generation %d replaced published generation %d", published, current.generation)
	}
}

func TestStaleSIMAndWakeEventsDoNotTargetReplacementWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev"}
	p.AttachWorkerForTest(old)
	owner := p.captureWorkerLifecycleOwner(old.ID)
	replaceWorkerForLifecycleTest(p, &Worker{ID: old.ID})

	p.handleSIMStatusEventForOwner(owner, "stale", nil, "")
	p.wakeDesiredVoWiFiRecoverForOwner(owner, "stale")
	if p.simEventTimers[old.ID] != nil {
		t.Fatal("stale SIM event scheduled a timer for the replacement worker")
	}
	if p.deviceEventWakeups[old.ID] != nil {
		t.Fatal("stale device event scheduled VoWiFi recovery for the replacement worker")
	}
}

func TestSIMIdentityRuntimeRetirementRejectsReplacementWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev"}
	p.AttachWorkerForTest(old)
	owner := p.captureWorkerLifecycleOwner(old.ID)
	transition := p.policyTransitionFor(old.ID)
	transition.Lock()
	done := make(chan error, 1)
	go func() { done <- p.retirePhoneRuntimesForSIMChange(owner) }()

	select {
	case <-done:
		t.Fatal("runtime retirement bypassed the policy transition lock")
	case <-time.After(20 * time.Millisecond):
	}
	replaceWorkerForLifecycleTest(p, &Worker{ID: old.ID})
	transition.Unlock()
	if err := <-done; err != errPolicyOwnerChanged {
		t.Fatalf("retirement error=%v, want %v", err, errPolicyOwnerChanged)
	}
}

func TestCurrentMatchedRescanPairRejectsDeletedSnapshot(t *testing.T) {
	snapshot := MatchedPair{
		Config:   config.DeviceConfig{ID: "dev", ModemIMEI: "860000000004004"},
		Hardware: CompatibleModem{IMEI: "860000000004004"},
	}
	if _, ok := matchConfiguredRescanPair(snapshot, []config.DeviceConfig{}); ok {
		t.Fatal("deleted configuration was accepted from a stale rescan snapshot")
	}
}

func TestCurrentMatchedRescanPairReloadsReboundIdentity(t *testing.T) {
	snapshot := MatchedPair{
		Config:   config.DeviceConfig{ID: "dev", ModemIMEI: "860000000004004"},
		Hardware: CompatibleModem{IMEI: "860000000004004"},
	}
	rebound := []config.DeviceConfig{{ID: "dev", ModemIMEI: "860000000005005"}}
	if _, ok := matchConfiguredRescanPair(snapshot, rebound); ok {
		t.Fatal("rebound configuration reused hardware matched by the stale identity")
	}
}

func TestMatchedRescanRejectsConfigurationDeletedWhileWaitingForMutation(t *testing.T) {
	cfg := &config.Config{Devices: []config.DeviceConfig{{
		ID: "dev", ModemIMEI: "860000000004004",
	}}}
	p := NewPool(cfg)
	t.Cleanup(func() { _ = p.Shutdown() })
	release := p.AcquireManagedDeviceMutation("dev")
	configured := append([]config.DeviceConfig(nil), cfg.Devices...)
	snapshot := MatchedPair{
		Config: configured[0], Hardware: CompatibleModem{IMEI: "860000000004004"},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.reconcileMatchedRescanDevice(rescanReconnectOptions{}, snapshot, configured)
	}()

	select {
	case <-done:
		t.Fatal("rescan bypassed the managed mutation lease")
	case <-time.After(20 * time.Millisecond):
	}
	cfg.Devices = nil
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rescan did not resume after mutation lease release")
	}
	if worker := p.GetWorker("dev"); worker != nil {
		t.Fatalf("stale scan recreated deleted worker: %+v", worker)
	}
}

func TestStaleATDisconnectDoesNotStartReplacementRecovery(t *testing.T) {
	p := NewPool(&config.Config{})
	old := &Worker{ID: "dev"}
	p.AttachWorkerForTest(old)
	owner := p.captureWorkerLifecycleOwner(old.ID)
	replaceWorkerForLifecycleTest(p, &Worker{ID: old.ID})

	p.scheduleATDisconnectRecovery(owner, "serial_runtime_error")
	p.mu.RLock()
	recovering := p.modemRebootRecovering[old.ID]
	p.mu.RUnlock()
	if recovering {
		t.Fatal("stale AT disconnect started recovery for the replacement worker")
	}
}

func TestInFlightIPRefreshDoesNotApplyToReplacementWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	started := make(chan struct{})
	release := make(chan struct{})
	network := &fakeController{connected: true, publicIPStarted: started, publicIPRelease: release}
	old := &Worker{ID: "dev", netOverride: network}
	old.cachedIP = "198.51.100.1"
	p.AttachWorkerForTest(old)

	p.refreshIPs(old, true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("public IP refresh did not start")
	}
	replaceWorkerForLifecycleTest(p, &Worker{ID: old.ID})
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		old.ipRefreshMu.Lock()
		inFlight := old.ipRefreshInFlight
		old.ipRefreshMu.Unlock()
		if !inFlight {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if old.GetCachedIP() != "198.51.100.1" {
		t.Fatal("retired worker applied an in-flight public IP result")
	}
}

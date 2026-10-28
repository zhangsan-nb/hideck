package device

import (
	"context"
	"testing"

	"github.com/iniwex5/vowifi-go/runtimehost"
	"github.com/yibaiba/hideck/internal/cardpolicy"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/vowifihost"
)

func TestPhysicalSIMInsertEventRefreshesIdentity(t *testing.T) {
	p := NewPool(&config.Config{})
	t.Cleanup(func() { _ = p.Shutdown() })
	deviceBackend := &workerStartupIdentityBackendStub{liveICCID: "card-new", liveIMSI: "imsi-new"}
	deviceBackend.simInserted = true
	w := &Worker{
		ID:      "dev",
		Pool:    p,
		Backend: deviceBackend,
		Config:  config.DeviceConfig{VoWiFiEnabled: true, PhoneMode: PhoneModeWiFi},
	}
	setWorkerSMSIdentity(w, SMSIdentity{ICCID: "card-old", IMSI: "imsi-old"})
	p.AttachWorkerForTest(w)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{
		ICCID: "card-new", VoWiFiEnabled: true, PhoneMode: PhoneModeWiFi,
	}})

	p.confirmSIMRemovedAndStopVoWiFi(w.ID, "test")
	if got := w.CurrentICCID(); got != "card-new" {
		t.Fatalf("ICCID=%q want card-new", got)
	}
	if got := w.GetCachedIMSI(); got != "imsi-new" {
		t.Fatalf("IMSI=%q want imsi-new", got)
	}
	p.deviceEventWakeMu.Lock()
	_, recoveryScheduled := p.deviceEventWakeups[w.ID]
	p.deviceEventWakeMu.Unlock()
	if !recoveryScheduled {
		t.Fatal("refreshed SIM did not schedule desired VoWiFi recovery")
	}
}

func TestConfirmedSIMRemovalClearsIdentityAndDatabaseBinding(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	p := NewPool(&config.Config{})
	t.Cleanup(func() { _ = p.Shutdown() })
	const imei = "860000000000333"
	if err := db.UpsertDevice(imei, "dev", "model", "port"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSIMCardIdentity("card-old", "imsi-old", "", ptrString(imei)); err != nil {
		t.Fatal(err)
	}
	deviceBackend := &workerStartupIdentityBackendStub{}
	deviceBackend.simInserted = false
	w := &Worker{ID: "dev", Pool: p, Backend: deviceBackend, Config: config.DeviceConfig{ModemIMEI: imei}}
	w.state.Identity.IMEI = imei
	setWorkerSMSIdentity(w, SMSIdentity{ICCID: "card-old", IMSI: "imsi-old"})
	p.AttachWorkerForTest(w)
	before := w.SIMIdentityGeneration()

	p.confirmSIMRemovedAndStopVoWiFi(w.ID, "test")
	if got := w.CurrentICCID(); got != "" {
		t.Fatalf("runtime retained removed SIM %q", got)
	}
	if w.SIMIdentityGeneration() <= before {
		t.Fatal("SIM removal did not retire the previous identity generation")
	}
	if got := db.CurrentICCIDForDevice(w.ID); got != "" {
		t.Fatalf("database retained removed SIM %q", got)
	}
}

func TestPhysicalSIMSwapStopsOldVoWiFiBeforeApplyingNewPolicy(t *testing.T) {
	p := NewPool(&config.Config{})
	t.Cleanup(func() { _ = p.Shutdown() })
	deviceBackend := &workerStartupIdentityBackendStub{liveICCID: "card-new", liveIMSI: "imsi-new"}
	deviceBackend.simInserted = true
	w := &Worker{
		ID:      "dev",
		Pool:    p,
		Backend: deviceBackend,
		Config:  config.DeviceConfig{VoWiFiEnabled: true, PhoneMode: PhoneModeWiFi},
	}
	setWorkerSMSIdentity(w, SMSIdentity{ICCID: "card-old", IMSI: "imsi-old"})
	p.AttachWorkerForTest(w)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{
		ICCID: "card-new", VoWiFiEnabled: true, PhoneMode: PhoneModeWiFi,
	}})
	p.voWiFiRuntimeStore().SetInstance(w.ID, &runtimehost.Instance{})
	t.Cleanup(func() { p.voWiFiRuntimeStore().DeleteInstance(w.ID, nil) })
	commands := make(chan vowifihost.LifecycleCommand, 1)
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(_ context.Context, command vowifihost.LifecycleCommand) error {
		commands <- command
		return nil
	}

	p.confirmSIMRemovedAndStopVoWiFi(w.ID, "test")
	select {
	case command := <-commands:
		if command.Kind != vowifihost.LifecycleCommandDisable || command.Reason != "sim_identity_changed" {
			t.Fatalf("lifecycle command=%s reason=%q want disable/sim_identity_changed", command.Kind, command.Reason)
		}
	default:
		t.Fatal("physical SIM swap left the previous VoWiFi runtime active")
	}
}

func TestConfirmedSIMRemovalCancelsStartingVoWiFi(t *testing.T) {
	p := NewPool(&config.Config{})
	t.Cleanup(func() { _ = p.Shutdown() })
	deviceBackend := &workerStartupIdentityBackendStub{}
	deviceBackend.simInserted = false
	w := &Worker{ID: "dev", Pool: p, Backend: deviceBackend}
	setWorkerSMSIdentity(w, SMSIdentity{ICCID: "card-old", IMSI: "imsi-old"})
	p.AttachWorkerForTest(w)
	if claim := p.voWiFiHost().BeginStart(w.ID); !claim.Accepted {
		t.Fatalf("BeginStart()=%+v want accepted", claim)
	}
	commands := make(chan vowifihost.LifecycleCommand, 1)
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(_ context.Context, command vowifihost.LifecycleCommand) error {
		commands <- command
		return nil
	}

	p.confirmSIMRemovedAndStopVoWiFi(w.ID, "test")
	select {
	case command := <-commands:
		if command.Kind != vowifihost.LifecycleCommandDisable || command.Reason != "sim_removed" {
			t.Fatalf("lifecycle command=%s reason=%q want disable/sim_removed", command.Kind, command.Reason)
		}
	default:
		t.Fatal("SIM removal did not cancel the in-progress VoWiFi start")
	}
}

func TestConfirmedSIMInsertionWaitsForActiveMBNPolicy(t *testing.T) {
	p := NewPool(&config.Config{})
	t.Cleanup(func() { _ = p.Shutdown() })
	w := &Worker{ID: "dev", Pool: p}
	setWorkerSMSIdentity(w, SMSIdentity{ICCID: "card-new", IMSI: "imsi-new"})
	p.AttachWorkerForTest(w)
	transition := p.policyTransitionFor(w.ID)
	transition.mbnSession.Store(&mbnDeviceSession{})
	t.Cleanup(func() { transition.mbnSession.Store(nil) })

	p.finishConfirmedSIMInsertion(p.captureWorkerLifecycleOwner(w.ID), "test", simInsertionRefreshResult{
		current: true, identityChanged: true,
	})
	p.deviceEventWakeMu.Lock()
	_, recoveryScheduled := p.deviceEventWakeups[w.ID]
	p.deviceEventWakeMu.Unlock()
	if recoveryScheduled {
		t.Fatal("SIM insertion scheduled recovery before the MBN policy operation completed")
	}
}

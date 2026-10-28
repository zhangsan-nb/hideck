package volte

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
	"github.com/iniwex5/vowifi-go/runtimehost/voicehost"
)

func TestEnableRequiresVoiceIndications(t *testing.T) {
	host := newFakeModem()
	host.VoiceEnsureErr = errors.New("VOICE indication register failed")
	ctl := NewControllerWithBackup(host, t.TempDir())

	err := ctl.Enable(context.Background(), host.ID)
	if err == nil || !strings.Contains(err.Error(), "VOICE indication") {
		t.Fatalf("want indication failure, got %v", err)
	}
	if st := ctl.Status(host.ID); st.Ready() || st.VoiceIndicationsReady || st.Phase != PhaseFailed {
		t.Fatalf("must not claim ready: %+v", st)
	}
}

func TestEnableRequiresVoiceStatusSubscription(t *testing.T) {
	host := newFakeModem()
	host.VoiceSubscribeErr = errors.New("subscribe unavailable")
	ctl := NewControllerWithBackup(host, t.TempDir())

	err := ctl.Enable(context.Background(), host.ID)
	if err == nil || !strings.Contains(err.Error(), "subscribe QMI VOICE status") {
		t.Fatalf("want subscription failure, got %v", err)
	}
	if ctl.Status(host.ID).VoiceIndicationsReady {
		t.Fatal("failed local subscription must not report VOICE indications ready")
	}
}

func TestDuplicateRegisteredIndicationDoesNotResetVoiceReadiness(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), host.ID); err != nil {
		t.Fatal(err)
	}
	host.fireIMSRegistration(&qmi.IMSARegistrationStatus{
		HasStatus: true,
		Status:    qmi.IMSARegistrationStateRegistered,
	})
	time.Sleep(20 * time.Millisecond)

	host.mu.Lock()
	ensureCount := host.VoiceEnsureCount
	host.mu.Unlock()
	if ensureCount != 1 || !ctl.Status(host.ID).Ready() {
		t.Fatalf("duplicate indication changed readiness: count=%d status=%+v", ensureCount, ctl.Status(host.ID))
	}
}

func TestIMSRegistrationReestablishesVoiceIndications(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), host.ID); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	host.VoiceEnsureErr = errors.New("radio cycle lost VOICE indications")
	host.mu.Unlock()

	host.fireIMSRegistration(&qmi.IMSARegistrationStatus{
		HasStatus: true,
		Status:    qmi.IMSARegistrationStateRegistering,
	})
	host.fireIMSRegistration(&qmi.IMSARegistrationStatus{
		HasStatus: true,
		Status:    qmi.IMSARegistrationStateRegistered,
	})
	waitForVoiceEnsureCount(t, host, 2)
	st := ctl.Status(host.ID)
	if st.Ready() || st.VoiceIndicationsReady || st.Phase != PhaseFailed {
		t.Fatalf("failed re-registration must block calls: %+v", st)
	}
	host.mu.Lock()
	host.VoiceEnsureErr = nil
	host.mu.Unlock()
	host.fireIMSRegistration(&qmi.IMSARegistrationStatus{
		HasStatus: true,
		Status:    qmi.IMSARegistrationStateRegistered,
	})
	waitForVoiceEnsureCount(t, host, 3)
	deadline := time.Now().Add(time.Second)
	for !ctl.Status(host.ID).Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ctl.Status(host.ID).Ready() {
		t.Fatalf("later registered indication did not recover readiness: %+v", ctl.Status(host.ID))
	}
}

func TestDialRevalidatesVoiceIndications(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), host.ID); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	host.VoiceEnsureErr = errors.New("VOICE indications unavailable")
	host.mu.Unlock()

	_, err := ctl.BeginCall(context.Background(), voicehost.BeginCallRequest{DeviceID: host.ID, Callee: "10010"})
	if err == nil || !strings.Contains(err.Error(), "pre_dial") {
		t.Fatalf("want pre-dial indication failure, got %v", err)
	}
	host.mu.Lock()
	dials := host.DialCount
	host.mu.Unlock()
	if dials != 0 {
		t.Fatalf("dial must not reach modem when indications are unavailable: %d", dials)
	}
}

func TestDisableUnsubscribesVoiceStatus(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), host.ID); err != nil {
		t.Fatal(err)
	}
	ctl.Disable(host.ID)

	host.mu.Lock()
	unsubscribes := host.VoiceUnsubscribeCount
	imsUnsubscribes := host.IMSUnsubscribeCount
	handler := host.voiceHandler
	imsRegistrationHandler := host.imsRegHandler
	imsServicesHandler := host.imsSvcHandler
	host.mu.Unlock()
	if unsubscribes != 1 || imsUnsubscribes != 2 || handler != nil || imsRegistrationHandler != nil || imsServicesHandler != nil {
		t.Fatalf("voice_unsubscribe=%d ims_unsubscribe=%d handlers=%v/%v/%v",
			unsubscribes, imsUnsubscribes, handler != nil, imsRegistrationHandler != nil, imsServicesHandler != nil)
	}
}

func waitForVoiceEnsureCount(t *testing.T, host *FakeModem, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		got := host.VoiceEnsureCount
		host.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("VOICE indication ensure did not reach %d", want)
}

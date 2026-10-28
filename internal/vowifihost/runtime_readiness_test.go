package vowifihost

import (
	"context"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
)

type runtimeReadinessAdapter struct {
	started []RuntimeStartedRequest
}

func (*runtimeReadinessAdapter) Context() context.Context                     { return context.Background() }
func (*runtimeReadinessAdapter) IsSwitching(string) bool                      { return false }
func (*runtimeReadinessAdapter) WorkerExists(string) bool                     { return true }
func (*runtimeReadinessAdapter) WaitQMICoreReady(string, time.Duration) error { return nil }
func (*runtimeReadinessAdapter) WaitWorkerReady(string, time.Duration) error  { return nil }
func (*runtimeReadinessAdapter) PrepareStart(context.Context, string, string, string) (PreparedStart, error) {
	return PreparedStart{}, nil
}
func (*runtimeReadinessAdapter) BeforeStart(string, runtimehost.Modem, *runtimehost.ProxyConfig) func(context.Context, runtimehost.SessionConfig) error {
	return nil
}
func (*runtimeReadinessAdapter) HandleStartupError(req StartupErrorRequest) error { return req.Err }
func (adapter *runtimeReadinessAdapter) MarkRuntimeStarted(req RuntimeStartedRequest) {
	adapter.started = append(adapter.started, req)
}
func (*runtimeReadinessAdapter) RestoreSMSMode(string)                {}
func (*runtimeReadinessAdapter) RestoreRadioAfterVoWiFi(string) error { return nil }

func TestManagerMarksRuntimeStartedOnlyAfterSMSReady(t *testing.T) {
	manager := NewManager()
	adapter := &runtimeReadinessAdapter{}
	manager.ConfigureAdapter(adapter)
	deviceID := "dev-readiness"
	claim := manager.BeginStart(deviceID)
	instance := &runtimehost.Instance{}
	var observer runtimehost.Observer
	manager.SetRuntimeStartForTest(func(_ context.Context, req runtimehost.StartRequest) (*runtimehost.Instance, error) {
		observer = req.Observer
		return instance, nil
	})

	_, err := manager.StartRuntime(context.Background(), RuntimeStartRequest{
		DeviceID: deviceID, TraceID: "trace-ready", Epoch: claim.Epoch,
		StartedAt: time.Now().Add(-time.Second),
		Prepared: PreparedStart{
			SIM: runtimehost.NewReaderSIMAdapter(simProviderStub{}),
			Prepared: identity.PreparedSession{
				Profile: identity.Profile{IMSI: "001010000000001"},
			},
		},
		Modem: runtimeStartTestModem{},
	})
	if err != nil {
		t.Fatalf("StartRuntime() error = %v", err)
	}
	emit := func(kind string, imsReady, smsReady bool) {
		observer.OnRuntimeHostEvent(context.Background(), runtimehost.Event{
			Kind: kind, DeviceID: deviceID, TraceID: "trace-ready", Session: instance,
			State: runtimehost.State{DeviceID: deviceID, IMSReady: imsReady, SMSReady: smsReady},
		})
	}

	emit("sms_ready", false, true)
	emit("ims_registered", true, false)
	if len(adapter.started) != 0 {
		t.Fatalf("runtime marked started before SMS readiness: %+v", adapter.started)
	}
	emit("sms_ready", true, true)
	emit("sms_ready", true, true)
	if len(adapter.started) != 1 {
		t.Fatalf("runtime start marks = %d, want 1", len(adapter.started))
	}
	if got := adapter.started[0]; got.DeviceID != deviceID || got.TraceID != "trace-ready" || got.Elapsed < time.Second {
		t.Fatalf("runtime start mark = %+v", got)
	}
}

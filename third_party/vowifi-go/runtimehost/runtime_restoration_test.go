package runtimehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/engine/swu"
	"github.com/iniwex5/vowifi-go/internal/vowifi/imscore"
	"github.com/iniwex5/vowifi-go/internal/vowifi/runtimecore"
	"github.com/iniwex5/vowifi-go/runtimehost/identity"
)

type restorationAccess struct{}

func (restorationAccess) Capabilities() identity.AccessCapabilities {
	return identity.AccessCapabilities{SIM: true, Modem: true}
}

func (restorationAccess) IMSIdentityProvider() identity.IMSIdentityProvider { return nil }

func TestObserverUnsubscribeAndContextPropagation(t *testing.T) {
	instance := &Instance{}
	key := struct{}{}
	ctx := context.WithValue(context.Background(), key, "runtime-context")
	var first, second atomic.Int32
	unsubscribe := instance.AddObserver(ObserverFunc(func(got context.Context, _ Event) {
		if got.Value(key) != "runtime-context" {
			t.Error("observer lost event context")
		}
		first.Add(1)
	}))
	instance.AddObserver(ObserverFunc(func(context.Context, Event) { second.Add(1) }))
	instance.publish(ctx, Event{Kind: "prepared"})
	unsubscribe()
	unsubscribe()
	instance.publish(ctx, Event{Kind: "prepared"})
	if first.Load() != 1 || second.Load() != 2 {
		t.Fatalf("observer calls first=%d second=%d", first.Load(), second.Load())
	}
}

func TestCoreSMSReadinessUpdatesRuntimeState(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{
		DeviceID: "wwan0", Phase: "sms_ready", IMSReady: true, SMSReady: true,
	})
	observer := &instanceObserver{inst: instance, deviceID: "wwan0"}
	request := runtimecore.RuntimeStartRequest{}
	chainSMSReadinessHook(&request, observer)

	request.Hooks.OnSMSReadinessChanged(context.Background(), imscore.SMSReadiness{
		Registered: true, ProfileReady: true, TransportReady: true, SMSCPresent: true,
		Reason: "IMS SMS receiver is not ready",
	})
	state := instance.State()
	if state.SMSReady || state.Phase != "ims_ready" || state.LastEvent != "sms_unavailable" {
		t.Fatalf("SMS unavailable state = %+v", state)
	}
	if state.SMSReadyReason != "IMS SMS receiver is not ready" {
		t.Fatalf("SMS unavailable reason = %q", state.SMSReadyReason)
	}
}

func TestCoreReplaysMOSMSReadinessAfterIMSRegistration(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{DeviceID: "wwan0", Phase: "ipsec_up"})
	observer := &instanceObserver{inst: instance, deviceID: "wwan0"}
	request := runtimecore.RuntimeStartRequest{}
	chainSMSReadinessHook(&request, observer)

	request.Hooks.OnSMSReadinessChanged(context.Background(), imscore.SMSReadiness{
		Registered: true, ProfileReady: true, TransportReady: true,
		SMSCPresent: true, MOReady: true, Reason: "IMS SMS receiver is not ready",
	})
	if state := instance.State(); state.SMSMOReady {
		t.Fatalf("MO readiness published before IMS registration: %+v", state)
	}

	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "ims_registered", DeviceID: "wwan0",
	})
	state := instance.State()
	if !state.IMSReady || !state.SMSMOReady {
		t.Fatalf("MO readiness lost after IMS registration: %+v", state)
	}
	if state.SMSReady || state.SMSReadyReason != "IMS SMS receiver is not ready" {
		t.Fatalf("MT readiness changed after IMS registration: %+v", state)
	}
}

func TestCoreKeepsMOReadinessDuringConcurrentIMSRegistration(t *testing.T) {
	const attempts = 2000
	readiness := imscore.SMSReadiness{
		Registered: true, ProfileReady: true, TransportReady: true,
		SMSCPresent: true, MOReady: true, Reason: "IMS SMS receiver is not ready",
	}
	for attempt := 0; attempt < attempts; attempt++ {
		instance := &Instance{}
		instance.setState(State{DeviceID: "wwan0", Phase: "ipsec_up"})
		observer := &instanceObserver{inst: instance, deviceID: "wwan0"}
		request := runtimecore.RuntimeStartRequest{}
		chainSMSReadinessHook(&request, observer)

		start := make(chan struct{})
		var calls sync.WaitGroup
		calls.Add(2)
		go func() {
			defer calls.Done()
			<-start
			request.Hooks.OnSMSReadinessChanged(context.Background(), readiness)
		}()
		go func() {
			defer calls.Done()
			<-start
			observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
				Kind: "ims_registered", DeviceID: "wwan0",
			})
		}()
		close(start)
		calls.Wait()

		state := instance.State()
		if !state.IMSReady || !state.SMSMOReady {
			t.Fatalf("attempt %d lost concurrent MO readiness: %+v", attempt, state)
		}
	}
}

func TestCoreSMSHealthReadinessIsRecordedBeforeSessionPublication(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{DeviceID: "wwan0", Phase: "ipsec_up"})
	observer := &instanceObserver{inst: instance, deviceID: "wwan0"}
	request := runtimecore.RuntimeStartRequest{}
	chainSMSReadinessHook(&request, observer)

	request.Hooks.OnSMSReadinessChanged(context.Background(), imscore.SMSReadiness{
		Registered: true, ProfileReady: true, TransportReady: true, ReceiverReady: true,
		SMSCPresent: true, Ready: true, HealthReady: true, Reason: "IMS SMS receiver ready",
	})
	state := instance.State()
	if state.IMSReady || state.SMSReady || !state.SMSHealthReady {
		t.Fatalf("pre-session readiness state = %+v", state)
	}
	if state.SMSReadyReason != "IMS SMS receiver ready" || state.UpdatedAt.IsZero() {
		t.Fatalf("pre-session health metadata = %+v", state)
	}
}

func TestSMSReadyClearsRecoveredFailureDetails(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{
		DeviceID: "wwan0", Phase: "ims_ready", IMSReady: true,
		LastReason: "fresh runtime required", LastError: "previous runtime failure",
		LastErrorClass: "runtime", Error: "previous runtime failure",
	})
	observer := &instanceObserver{inst: instance, deviceID: "wwan0"}

	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "sms_ready", Reason: "IMS SMS receiver ready",
	})

	state := instance.State()
	if state.Phase != "sms_ready" || !state.SMSReady || !state.SMSHealthReady {
		t.Fatalf("SMS-ready state = %+v", state)
	}
	if state.LastReason != "" || state.LastError != "" || state.LastErrorClass != "" || state.Error != "" {
		t.Fatalf("recovered state still has failure details = %+v", state)
	}
}

func TestLateSMSReadinessAfterInterruptionKeepsSMSUnavailable(t *testing.T) {
	for _, down := range []struct{ event, phase string }{
		{"interrupted", "interrupted"}, {"retrying", "retrying"}, {"terminal_error", "error"},
		{"stopped", "stopped"}, {"", "restarting"},
	} {
		t.Run(down.phase, func(t *testing.T) {
			instance := &Instance{}
			instance.setState(State{DeviceID: "wwan0", Phase: "sms_ready", IMSReady: true, SMSReady: true,
				SMSHealthReady: true, SMSMOReady: true})
			observer := &instanceObserver{inst: instance, deviceID: "wwan0"}
			var published []Event
			instance.AddObserver(ObserverFunc(func(_ context.Context, event Event) { published = append(published, event) }))
			request := runtimecore.RuntimeStartRequest{}
			chainSMSReadinessHook(&request, observer)
			chainSMSReadyHook(&request, observer)
			if down.event == "" {
				instance.setTunnelReauthenticationRequired(errors.New("fresh runtime required"))
			} else {
				observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
					Kind: down.event, Reason: "swu_session_down",
				})
			}
			if got := instance.State().Phase; got != down.phase {
				t.Fatalf("phase = %q, want %q", got, down.phase)
			}

			// The IMS service of the old runtime reports ready once more, as
			// runtimecore delivers it: the snapshot first, then sms_ready.
			request.Hooks.OnSMSReadinessChanged(context.Background(), imscore.SMSReadiness{
				Registered: true, ProfileReady: true, TransportReady: true, ReceiverReady: true,
				SMSCPresent: true, Ready: true, MOReady: true, HealthReady: true, Reason: "IMS SMS receiver ready",
			})
			request.Hooks.OnSMSReady(context.Background())

			state := instance.State()
			if state.IMSReady || state.SMSReady || state.SMSHealthReady || state.SMSMOReady || state.Phase == "sms_ready" {
				t.Fatalf("state after a late SMS readiness = %+v", state)
			}
			if len(published) != 3 || published[1].Kind != "state" || published[2].Kind != "sms_ready" {
				t.Fatalf("published %d events, want %s, state, sms_ready", len(published), down.phase)
			}
			for _, event := range published[1:] {
				if event.State.SMSReady || event.State.SMSHealthReady || event.State.SMSMOReady {
					t.Fatalf("published %s with SMS readiness: %+v", event.Kind, event.State)
				}
			}
		})
	}
}

func TestCurrentSMSReadinessClearsRecoveredFailureDetails(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{
		DeviceID: "wwan0", Phase: "ims_ready", IMSReady: true,
		LastReason: "IMS registered", LastError: "previous runtime failure",
		LastErrorClass: "runtime", Error: "previous runtime failure",
	})

	instance.updateSMSReadiness(SMSReadiness{
		Registered: true, Ready: true, HealthReady: true, Reason: "IMS SMS receiver ready",
	})

	state := instance.State()
	if state.Phase != "sms_ready" || !state.SMSReady || !state.SMSHealthReady {
		t.Fatalf("SMS-ready state = %+v", state)
	}
	if state.LastReason != "" || state.LastError != "" || state.LastErrorClass != "" || state.Error != "" {
		t.Fatalf("recovered state still has failure details = %+v", state)
	}
}

func TestMainStartWaitsForRecoveredIPSecReadyEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{})
	finished := make(chan struct{})
	observed := make(chan Event, 2)
	request := restoredRuntimeRequest()
	request.Access = restorationAccess{}
	request.Observer = ObserverFunc(func(_ context.Context, event Event) {
		if event.Kind == "prepared" || event.Kind == "connecting" {
			observed <- event
		}
	})
	request.runner = func(ctx context.Context, core runtimecore.RuntimeStartRequest) (StartResult, error) {
		defer close(finished)
		if !core.Reconnect || time.Duration(core.ReconnectDelay(0)) != 5*time.Second {
			return StartResult{}, errors.New("runtime core reconnect policy was not installed")
		}
		core.Observer.OnRuntimeEvent(ctx, runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
			Kind: "prepared", DeviceID: core.DeviceID, TraceID: core.TraceID,
		})
		core.Observer.OnRuntimeEvent(ctx, runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
			Kind: "connecting", DeviceID: core.DeviceID, TraceID: core.TraceID,
		})
		close(started)
		<-release
		session := &runtimecore.SessionResult{DeviceID: core.DeviceID}
		core.Observer.OnRuntimeEvent(ctx, runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
			Kind: "established", Handle: session, DeviceID: core.DeviceID,
			TraceID: core.TraceID, Snapshot: runtimecore.Snapshot{Established: true},
		})
		<-ctx.Done()
		core.Observer.OnRuntimeEvent(ctx, runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
			Kind: "stopped", Handle: session, DeviceID: core.DeviceID, TraceID: core.TraceID,
		})
		return StartResult{TraceID: core.TraceID}, ctx.Err()
	}

	type result struct {
		instance *Instance
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		instance, err := Start(ctx, request)
		resultChannel <- result{instance: instance, err: err}
	}()
	<-started
	select {
	case event := <-observed:
		if !event.State.SIMReady || event.State.AccessReady || event.Session == nil {
			t.Fatalf("prepared event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("initial observer missed prepared event before Start returned")
	}
	select {
	case event := <-observed:
		if event.State.Phase != PhaseAccessReady || !event.State.SIMReady || !event.State.AccessReady {
			t.Fatalf("connecting event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("initial observer missed connecting event before Start returned")
	}
	select {
	case got := <-resultChannel:
		t.Fatalf("Start returned before ipsec_up: %+v", got)
	default:
	}
	close(release)
	got := <-resultChannel
	if got.err != nil || got.instance == nil {
		t.Fatalf("Start instance=%v err=%v", got.instance, got.err)
	}
	state := got.instance.State()
	if !state.TunnelReady || state.LastEvent != "ipsec_up" || state.NetworkMode != "wifi" {
		t.Fatalf("runtime state = %+v", state)
	}
	if state.EPDGAddress != "epdg.example.com" || state.DataplaneMode != swu.DataplaneModeUserspace {
		t.Fatalf("runtime metadata = %+v", state)
	}
	got.instance.mu.RLock()
	hasSession := got.instance.session != nil
	got.instance.mu.RUnlock()
	if !hasSession {
		t.Fatal("ipsec_up did not retain the runtime session")
	}
	if err := got.instance.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-finished
}

func TestReaderStartReturnsBeforeRuntimeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	finished := make(chan struct{})
	request := restoredRuntimeRequest()
	request.Mode = StartModeReader
	request.runner = func(ctx context.Context, core runtimecore.RuntimeStartRequest) (StartResult, error) {
		defer close(finished)
		if time.Duration(core.ReconnectDelay(1)) != 60*time.Second {
			return StartResult{}, errors.New("reader reconnect policy was not installed")
		}
		close(started)
		<-ctx.Done()
		return StartResult{TraceID: core.TraceID}, ctx.Err()
	}
	instance, err := Start(ctx, request)
	if err != nil || instance == nil {
		t.Fatalf("reader Start instance=%v err=%v", instance, err)
	}
	<-started
	if state := instance.State(); state.Phase != "starting" || state.TunnelReady {
		t.Fatalf("reader initial state = %+v", state)
	}
	cancel()
	<-finished
	if err := instance.Stop(context.Background()); err != nil {
		t.Fatalf("reader Stop: %v", err)
	}
}

func TestMainStartExposesRunnerFailureBeforeReady(t *testing.T) {
	want := errors.New("core start rejected")
	request := restoredRuntimeRequest()
	request.runner = func(context.Context, runtimecore.RuntimeStartRequest) (StartResult, error) {
		return StartResult{TraceID: request.TraceID}, want
	}
	instance, err := Start(context.Background(), request)
	if instance != nil || !errors.Is(err, want) {
		t.Fatalf("Start instance=%v err=%v, want %v", instance, err, want)
	}
}

func TestCoreRequestConvertsCompleteBeforeStartConfig(t *testing.T) {
	request := restoredRuntimeRequest()
	request.Dataplane.TUNName = "vowifi-test0"
	request.DNSServer = "192.0.2.53"
	request.Proxy = &ProxyConfig{ID: "p1", Addr: "127.0.0.1:1080", Enabled: true}
	var received SessionConfig
	request.BeforeStart = func(_ context.Context, config SessionConfig) error {
		received = config
		return nil
	}
	core := request.coreRequest()
	if core.BeforeSessionStart == nil || core.Prepared == nil {
		t.Fatal("core request dropped prepared session or BeforeStart")
	}
	ctx := context.Background()
	err := core.BeforeSessionStart(ctx, runtimecore.SessionConfig{
		Ctx: ctx, DeviceID: core.DeviceID, TraceID: core.TraceID, Prepared: *core.Prepared,
		DataplaneMode: core.Dataplane.Mode, TUNName: core.Dataplane.TUNName,
		Proxy: core.Proxy, DNSServer: core.DNSServer,
	})
	if err != nil {
		t.Fatalf("BeforeSessionStart: %v", err)
	}
	if received.Ctx != ctx || received.DeviceID != request.DeviceID || received.TraceID != request.TraceID {
		t.Fatalf("BeforeStart identity = %+v", received)
	}
	if received.Prepared.Profile.IMSI != request.Prepared.Profile.IMSI || received.TUNName != "vowifi-test0" {
		t.Fatalf("BeforeStart prepared/dataplane = %+v", received)
	}
	if received.Proxy == nil || received.Proxy.Addr != request.Proxy.Addr || received.DNSServer != request.DNSServer {
		t.Fatalf("BeforeStart network = %+v", received)
	}
}

func TestRecoveredPreparedSessionConversionsDetachCarrierData(t *testing.T) {
	request := restoredRuntimeRequest()
	request.Prepared.EffectiveCarrier.MCC = request.Prepared.Profile.MCC
	request.Prepared.EffectiveCarrier.MNC = request.Prepared.Profile.MNC
	request.Prepared.EffectiveCarrier.IKEProposals = []string{"aes128-sha256-modp2048"}
	request.Prepared.AuthPlan = identity.AuthPlan{EPDGApp: "ISIM", IMSApp: "invalid"}
	internal := preparedSessionPtrToInternal(request.Prepared)
	if internal == nil || internal.AuthPlan.EPDGApp != "isim" || internal.AuthPlan.IMSApp != "usim" {
		t.Fatalf("internal auth plan = %+v", internal)
	}
	request.Prepared.EffectiveCarrier.IKEProposals[0] = "changed"
	if got := internal.CarrierPlan.IKE.IKEProposals[0]; got != "aes128-sha256-modp2048" {
		t.Fatalf("internal proposal retained caller alias: %q", got)
	}
	external := preparedSessionFromInternal(*internal)
	internal.CarrierPlan.IKE.IKEProposals[0] = "changed-again"
	if got := external.EffectiveCarrier.IKEProposals[0]; got != "aes128-sha256-modp2048" {
		t.Fatalf("external proposal retained internal alias: %q", got)
	}
}

func TestRuntimeEventNamesAndStateAreRecovered(t *testing.T) {
	instance := &Instance{}
	instance.setState(State{
		SessionState: "established", TunnelReady: true, DataPlaneUp: true,
		IMSReady: true, SMSReady: true, SMSHealthReady: true,
		IMSState: "registered", RegStatus: 1, RegStatusText: "registered",
		SMSReadyReason: "IMS SMS receiver ready",
	})
	instance.setSession(&runtimecore.SessionResult{})
	instance.setService(&stubService{})
	observer := &instanceObserver{inst: instance, deviceID: "dev-1"}
	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "error", Message: "authentication rejected",
	})
	state := instance.State()
	if state.LastEvent != "error" || state.Phase != "retrying" || state.SessionState != "retrying" ||
		state.LastError != "authentication rejected" || state.IMSState != "retrying" || state.RegStatus != 0 ||
		state.RegStatusText != "retrying" || state.SMSReadyReason != "" {
		t.Fatalf("retryable error state = %+v", state)
	}
	assertRuntimeHandlesCleared(t, instance)
	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "retry", Attempt: 2, RetryDelay: int64(time.Second), RedirectEPDG: "epdg-2.example",
	})
	state = instance.State()
	if state.LastEvent != "retrying" || state.Phase != "retrying" ||
		state.LastRedirectEPDG != "epdg-2.example" || state.LastError != "authentication rejected" {
		t.Fatalf("retry state = %+v", state)
	}
	newSession := &runtimecore.SessionResult{}
	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "established", Handle: newSession, Snapshot: runtimecore.Snapshot{Established: true},
	})
	state = instance.State()
	if state.Phase != "ipsec_up" || state.IMSState != "registering" ||
		state.RegStatus != 0 || state.RegStatusText != "registering" {
		t.Fatalf("IPsec-ready IMS state = %+v", state)
	}
	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "ims_registered", Handle: newSession,
	})
	state = instance.State()
	if state.Phase != "ims_ready" || state.LastError != "" || state.LastErrorClass != "" || state.Error != "" {
		t.Fatalf("recovered state still has last_error = %+v", state)
	}
	instance.setService(&stubService{})
	observer.OnRuntimeEvent(context.Background(), runtimecore.RuntimeEvent[*runtimecore.SessionResult]{
		Kind: "terminal_error", Message: "redirect loop",
	})
	state = instance.State()
	if state.LastEvent != "terminal_error" || state.Phase != "error" || state.SessionState != "error" ||
		state.LastError != "redirect loop" || state.IMSState != "failed" || state.RegStatus != 0 ||
		state.RegStatusText != "failed" {
		t.Fatalf("terminal state = %+v", state)
	}
	assertRuntimeHandlesCleared(t, instance)
}

func assertRuntimeHandlesCleared(t *testing.T, instance *Instance) {
	t.Helper()
	instance.mu.RLock()
	session := instance.session
	instance.mu.RUnlock()
	if session != nil || instance.Service() != nil {
		t.Fatalf("runtime handles were not cleared: session=%p service=%T", session, instance.Service())
	}
}

func restoredRuntimeRequest() StartRequest {
	return StartRequest{
		Mode: StartModeMain, DeviceID: "dev-1", TraceID: "trace-1", NetworkMode: "wifi",
		Prepared: &identity.PreparedSession{
			Profile: identity.Profile{IMSI: "310260123456789", MCC: "310", MNC: "260"},
			IMSIdentity: identity.IMSIdentityResult{
				IMPI: "310260123456789@ims.example", IMPU: "sip:310260123456789@ims.example",
				Domain: "ims.example", Applied: true,
			},
			EPDGAddr: "epdg.example.com",
		},
		SIM:       NewReaderSIMAdapter(startAKAProvider{}),
		Dataplane: DataplanePolicy{Mode: swu.DataplaneModeUserspace},
	}
}

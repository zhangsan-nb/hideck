package device

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/yibaiba/hideck/internal/backend"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/esim"
	"github.com/yibaiba/hideck/internal/vowifihost"
)

func withFastLebaraUKIdentityWait(t *testing.T) {
	t.Helper()
	origInterval := lebaraUKIdentityPollInterval
	origTimeout := lebaraUKIdentityWaitTimeout
	origCount := lebaraUKIdentityStableCount
	origSettle := lebaraUKProfileCycleSettle
	origIdle := lebaraUKSwitchIdleTimeout
	lebaraUKIdentityPollInterval = time.Millisecond
	lebaraUKIdentityWaitTimeout = 80 * time.Millisecond
	lebaraUKIdentityStableCount = 3
	lebaraUKProfileCycleSettle = 0
	lebaraUKSwitchIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		lebaraUKIdentityPollInterval = origInterval
		lebaraUKIdentityWaitTimeout = origTimeout
		lebaraUKIdentityStableCount = origCount
		lebaraUKProfileCycleSettle = origSettle
		lebaraUKSwitchIdleTimeout = origIdle
	})
}

func setEsimOverviewCacheForTest(t *testing.T, mgr *esim.Manager, overview *esim.EsimOverview) {
	t.Helper()
	field := reflect.ValueOf(mgr).Elem().FieldByName("overviewCache")
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(overview))
}

func TestPickLebaraUKParkingProfilePrefersProvisioningOnSameChip(t *testing.T) {
	groups := []esim.EUICCProfiles{
		{
			AIDHex: "A000",
			Profiles: []esim.ProfileItem{
				{ICCID: "8944000000000000087", Name: "Lebara UK", State: 1, ClassText: "operational"},
				{ICCID: "8944000000000000001", Name: "Bootstrap", State: 0, ClassText: "provisioning"},
				{ICCID: "8944000000000000002", Name: "Lebara UK 2", State: 0, ClassText: "operational"},
			},
		},
	}
	got, ok := pickLebaraUKParkingProfile(groups, "8944000000000000087")
	if !ok || got.ICCID != "8944000000000000001" {
		t.Fatalf("parking=%+v ok=%v", got, ok)
	}
}

func TestPlanLebaraUKIdentityRecoverSelfCycleWhenPPRAllows(t *testing.T) {
	groups := []esim.EUICCProfiles{{
		AIDHex: "A000",
		Profiles: []esim.ProfileItem{
			{ICCID: "8944lebara", Name: "Lebara UK", State: 1},
			{ICCID: "8944park", Name: "Bootstrap", State: 0, ClassText: "provisioning"},
		},
	}}
	plan, err := planLebaraUKIdentityRecover(groups, "8944lebara", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "self_cycle" || plan.ParkingICCID != "8944park" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPlanLebaraUKIdentityRecoverUsesParkingWhenDisableForbidden(t *testing.T) {
	groups := []esim.EUICCProfiles{{
		AIDHex: "A000",
		Profiles: []esim.ProfileItem{
			{ICCID: "8944lebara", Name: "Lebara UK", State: 1, DisablingNotAllowed: true},
			{ICCID: "8944park", Name: "Bootstrap", State: 0, ClassText: "provisioning"},
		},
	}}
	plan, err := planLebaraUKIdentityRecover(groups, "8944lebara", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "parking" || plan.ParkingICCID != "8944park" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPlanLebaraUKIdentityRecoverFailsWithoutParkingWhenPPRUnknown(t *testing.T) {
	_, err := planLebaraUKIdentityRecover(nil, "8944lebara", false, false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWaitLebaraUKHomeIdentityRequiresThreeStableReads(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "234870000000001",
	}
	w := &Worker{ID: "dev-1", Backend: be}
	if err := p.waitLebaraUKHomeIdentity(context.Background(), w, "8944000000000000087"); err != nil {
		t.Fatal(err)
	}
}

func TestWaitLebaraUKHomeIdentityTimesOutOn20404(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "204040000000001",
	}
	w := &Worker{ID: "dev-1", Backend: be}
	err := p.waitLebaraUKHomeIdentity(context.Background(), w, "8944000000000000087")
	if !errors.Is(err, ErrLebaraUKFlippedIMSI) {
		t.Fatalf("err=%v", err)
	}
}

func TestBeginLebaraUKIdentityRecoverCapsAutoAttempts(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	w := &Worker{ID: "dev-1"}
	w.state.Identity.ICCID = "8944000000000000087"
	if !p.beginLebaraUKIdentityRecover(w, "", false) {
		t.Fatal("first begin should succeed")
	}
	p.lebaraRecoverMu.Lock()
	p.lebaraRecover[w.ID].InFlight = false
	p.lebaraRecover[w.ID].Attempts = 2
	p.lebaraRecoverMu.Unlock()
	if p.beginLebaraUKIdentityRecover(w, "", false) {
		t.Fatal("auto recover should stop after two rounds")
	}
	if p.LebaraUKIdentityRecoverSnapshot(w.ID).Status != LebaraUKIdentityFailed {
		t.Fatalf("status=%q", p.LebaraUKIdentityRecoverSnapshot(w.ID).Status)
	}
	if !p.beginLebaraUKIdentityRecover(w, "", true) {
		t.Fatal("manual retry should reset the cap")
	}
}

func TestLebaraUKIdentityRecoverOldRunCannotMutateReplacement(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	oldWorker := &Worker{ID: "dev-1"}
	oldWorker.state.Identity.ICCID = "8944000000000000087"
	p.assignWorkerGeneration(oldWorker)
	p.workers[oldWorker.ID] = oldWorker
	if !p.beginLebaraUKIdentityRecover(oldWorker, "", false) {
		t.Fatal("begin old recovery")
	}
	oldRun := p.currentLebaraUKIdentityRecover(oldWorker.ID)
	p.cancelWorkerLifecycleTimers(oldWorker.ID)

	newWorker := &Worker{ID: oldWorker.ID}
	newWorker.state.Identity.ICCID = "8944000000000000087"
	p.assignWorkerGeneration(newWorker)
	p.workers[newWorker.ID] = newWorker
	if !p.beginLebaraUKIdentityRecover(newWorker, "", false) {
		t.Fatal("begin replacement recovery")
	}
	newRun := p.currentLebaraUKIdentityRecover(newWorker.ID)
	p.finishLebaraUKIdentityRecoverFailedForRun(oldWorker.ID, oldRun, errors.New("stale failure"))
	if p.clearLebaraUKIdentityRecoverRun(oldWorker.ID, oldRun) {
		t.Fatal("old recovery cleared replacement state")
	}
	effectRan := false
	if p.runLebaraUKIdentityOwnerEffect(oldRun, false, func() { effectRan = true }) || effectRan {
		t.Fatal("old recovery executed an effect for the replacement worker")
	}

	if oldRun.ctx.Err() == nil {
		t.Fatal("old recovery context was not canceled")
	}
	if current := p.currentLebaraUKIdentityRecover(newWorker.ID); current != newRun || !current.InFlight {
		t.Fatalf("replacement recovery was changed by old run: %+v", current)
	}
}

func TestBeginLebaraUKIdentityRecoverCreatesNewRunState(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	worker := &Worker{ID: "dev-1"}
	worker.state.Identity.ICCID = "8944000000000000087"
	p.assignWorkerGeneration(worker)
	p.workers[worker.ID] = worker
	if !p.beginLebaraUKIdentityRecover(worker, "", false) {
		t.Fatal("begin first recovery")
	}
	first := p.currentLebaraUKIdentityRecover(worker.ID)
	p.lebaraRecoverMu.Lock()
	first.InFlight = false
	p.lebaraRecoverMu.Unlock()
	if !p.beginLebaraUKIdentityRecover(worker, "", true) {
		t.Fatal("begin manual recovery")
	}
	second := p.currentLebaraUKIdentityRecover(worker.ID)
	if first == second {
		t.Fatal("new recovery reused mutable state from the completed run")
	}
	if first.ctx.Err() == nil {
		t.Fatal("first recovery context was not canceled")
	}
}

func TestBeginLebaraUKIdentityRecoverRunStaysBoundToCreatingWorker(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	oldWorker := &Worker{ID: "dev-1"}
	oldWorker.state.Identity.ICCID = "8944000000000000087"
	p.assignWorkerGeneration(oldWorker)
	p.workers[oldWorker.ID] = oldWorker
	oldRun := p.beginLebaraUKIdentityRecoverRun(oldWorker, "", false)
	if oldRun == nil {
		t.Fatal("begin old recovery")
	}
	p.cancelWorkerLifecycleTimers(oldWorker.ID)

	newWorker := &Worker{ID: oldWorker.ID}
	newWorker.state.Identity.ICCID = "8944000000000000087"
	p.assignWorkerGeneration(newWorker)
	p.workers[newWorker.ID] = newWorker
	newRun := p.beginLebaraUKIdentityRecoverRun(newWorker, "", false)
	if newRun == nil {
		t.Fatal("begin replacement recovery")
	}
	if oldRun == newRun || oldRun.owner.worker != oldWorker || newRun.owner.worker != newWorker {
		t.Fatalf("recovery ownership crossed workers: old=%+v new=%+v", oldRun.owner, newRun.owner)
	}
}

func TestRecoverLebaraUKIdentityOnceSelfCycleThenVoWiFi(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	mgr := &esim.Manager{}
	setEsimOverviewCacheForTest(t, mgr, &esim.EsimOverview{Profiles: []esim.EUICCProfiles{{
		AIDHex: "A000",
		Profiles: []esim.ProfileItem{
			{ICCID: "8944000000000000087", Name: "Lebara UK", State: 1},
			{ICCID: "8944000000000000001", Name: "Bootstrap", State: 0, ClassText: "provisioning"},
		},
	}}})
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "204040000000001",
	}
	w := &Worker{
		ID:      "dev-1",
		EsimMgr: mgr,
		Backend: be,
		Config:  config.DeviceConfig{ID: "dev-1", VoWiFiEnabled: true, PhoneMode: "wifi"},
	}
	w.state.Identity.ICCID = "8944000000000000087"
	w.state.Identity.IMSI = "204040000000001"
	p.workers[w.ID] = w

	var ops []string
	var mu sync.Mutex
	lebaraUKDisableProfileHook = func(_ context.Context, _ *Worker, iccid, _ string) error {
		mu.Lock()
		ops = append(ops, "disable:"+iccid)
		mu.Unlock()
		return nil
	}
	lebaraUKEnableProfileHook = func(_ context.Context, _ *Worker, iccid, _ string) error {
		mu.Lock()
		ops = append(ops, "enable:"+iccid)
		mu.Unlock()
		be.liveIMSI = "234870000000001"
		return nil
	}
	t.Cleanup(func() {
		lebaraUKDisableProfileHook = nil
		lebaraUKEnableProfileHook = nil
	})

	var got []string
	startPolicyLockAvailable := false
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(ctx context.Context, cmd vowifihost.LifecycleCommand) error {
		got = append(got, cmd.Kind.String())
		transition := p.policyTransitionFor(w.ID)
		startPolicyLockAvailable = transition.TryLock()
		if startPolicyLockAvailable {
			transition.Unlock()
		}
		return nil
	}

	if !p.beginLebaraUKIdentityRecover(w, "", false) {
		t.Fatal("begin")
	}
	p.runLebaraUKIdentityRecover(w, false)
	if !reflect.DeepEqual(ops, []string{"disable:8944000000000000087", "enable:8944000000000000087"}) {
		t.Fatalf("ops=%v", ops)
	}
	if p.LebaraUKIdentityRecoverSnapshot(w.ID).Status != "" {
		t.Fatalf("recover state should clear on success: %+v", p.LebaraUKIdentityRecoverSnapshot(w.ID))
	}
	if !reflect.DeepEqual(got, []string{"enable"}) {
		t.Fatalf("vowifi commands=%v", got)
	}
	if !startPolicyLockAvailable {
		t.Fatal("VoWiFi enable ran while the device policy transition lock was held")
	}
	if !IsLebaraUKHomeIMSI(w.GetCachedIMSI()) {
		t.Fatalf("cached IMSI=%q", w.GetCachedIMSI())
	}
}

func TestRestorePostSwitchConnectivitySkipsVoWiFiWhileRecovering(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{mode: backend.BackendQMI, liveICCID: "8944", liveIMSI: "234870000000001"}
	w := &Worker{
		ID:      "dev-1",
		Backend: be,
		Config:  config.DeviceConfig{ID: "dev-1", VoWiFiEnabled: true},
	}
	p.workers[w.ID] = w
	w.state.Identity.ICCID = "8944"
	if !p.beginLebaraUKIdentityRecover(w, "", false) {
		t.Fatal("begin")
	}
	var got []string
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(ctx context.Context, cmd vowifihost.LifecycleCommand) error {
		got = append(got, cmd.Kind.String())
		return nil
	}
	p.restorePostSwitchConnectivity(w.ID, w, esimSwitchContext{TargetLebaraCandidate: true}, nil, true)
	if len(got) != 0 {
		t.Fatalf("SwitchEnd during recover lock: %v", got)
	}
}

func TestRestorePostSwitchConnectivityWaitsFor23487BeforeSwitchEnd(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "234870000000001",
	}
	w := &Worker{
		ID:      "dev-1",
		Backend: be,
		Config:  config.DeviceConfig{ID: "dev-1", VoWiFiEnabled: true},
	}
	w.state.Identity.IMSI = "234870000000001"
	w.state.Identity.ICCID = "8944000000000000087"
	p.workers[w.ID] = w
	var got []string
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(ctx context.Context, cmd vowifihost.LifecycleCommand) error {
		got = append(got, cmd.Kind.String())
		return nil
	}
	p.restorePostSwitchConnectivity(w.ID, w, esimSwitchContext{
		TargetICCID:           "8944000000000000087",
		TargetLebaraCandidate: true,
	}, nil, true)
	if !reflect.DeepEqual(got, []string{"switch_end"}) {
		t.Fatalf("commands=%v", got)
	}
}

func TestRestorePostSwitchConnectivityDoesNotSwitchEndOn20404(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "204040000000001",
	}
	w := &Worker{
		ID:      "dev-1",
		Backend: be,
		Config:  config.DeviceConfig{ID: "dev-1", VoWiFiEnabled: true},
	}
	w.state.Identity.IMSI = "204040000000001"
	w.state.Identity.ICCID = "8944000000000000087"
	p.workers[w.ID] = w
	var got []string
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(ctx context.Context, cmd vowifihost.LifecycleCommand) error {
		got = append(got, cmd.Kind.String())
		return nil
	}
	p.restorePostSwitchConnectivity(w.ID, w, esimSwitchContext{
		TargetICCID:           "8944000000000000087",
		TargetLebaraCandidate: true,
	}, nil, true)
	if len(got) != 0 {
		t.Fatalf("SwitchEnd on 20404: %v", got)
	}
}

func TestPickLebaraUKParkingProfileSkipsLebaraNames(t *testing.T) {
	groups := []esim.EUICCProfiles{{
		AIDHex: "A000",
		Profiles: []esim.ProfileItem{
			{ICCID: "8944000000000000087", Name: "Lebara UK", State: 1, ClassText: "operational"},
			{ICCID: "8944000000000000002", Name: "Lebara UK 2", State: 0, ClassText: "operational"},
		},
	}}
	if _, ok := pickLebaraUKParkingProfile(groups, "8944000000000000087"); ok {
		t.Fatal("must not park on another Lebara UK profile")
	}
}

func TestWaitLebaraUKHomeIdentityIgnoresEmptyICCID(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "",
		liveIMSI:  "234870000000001",
	}
	w := &Worker{ID: "dev-1", Backend: be}
	if err := p.waitLebaraUKHomeIdentity(context.Background(), w, "8944000000000000087"); err == nil {
		t.Fatal("empty ICCID must not count as a stable British identity")
	}
}

func TestShouldWaitLebaraUKHomeIdentityIgnoresClassifyFailed(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	w := &Worker{ID: "dev-1"}
	w.state.Identity.IMSI = "460001234567890"
	if p.shouldWaitLebaraUKHomeIdentity(w, esimSwitchContext{TargetClassifyFailed: true}) {
		t.Fatal("classify failure on a non-Lebara card must not enter the 23487 gate")
	}
}

func TestRecoverUsesPinnedICCIDAfterCurrentCleared(t *testing.T) {
	withFastLebaraUKIdentityWait(t)
	p := NewPool(&config.Config{})
	defer p.cancel()
	mgr := &esim.Manager{}
	setEsimOverviewCacheForTest(t, mgr, &esim.EsimOverview{Profiles: []esim.EUICCProfiles{{
		AIDHex: "A000",
		Profiles: []esim.ProfileItem{
			{ICCID: "8944000000000000087", Name: "Lebara UK", State: 1},
			{ICCID: "8944000000000000001", Name: "Bootstrap", State: 0, ClassText: "provisioning"},
		},
	}}})
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "8944000000000000087",
		liveIMSI:  "204040000000001",
	}
	w := &Worker{ID: "dev-1", EsimMgr: mgr, Backend: be}
	w.state.Identity.ICCID = "8944000000000000087"
	w.state.Identity.IMSI = "204040000000001"
	p.workers[w.ID] = w
	var ops []string
	lebaraUKDisableProfileHook = func(_ context.Context, worker *Worker, iccid, _ string) error {
		ops = append(ops, "disable:"+iccid)
		worker.state.Identity.ICCID = ""
		worker.state.Identity.TargetICCID = ""
		worker.state.Identity.IMSI = ""
		return nil
	}
	lebaraUKEnableProfileHook = func(_ context.Context, _ *Worker, iccid, _ string) error {
		ops = append(ops, "enable:"+iccid)
		be.liveICCID = "8944000000000000087"
		be.liveIMSI = "234870000000001"
		return nil
	}
	t.Cleanup(func() {
		lebaraUKDisableProfileHook = nil
		lebaraUKEnableProfileHook = nil
	})
	if !p.beginLebaraUKIdentityRecover(w, "8944000000000000087", false) {
		t.Fatal("begin")
	}
	if err := p.recoverLebaraUKIdentityOnce(w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ops, []string{"disable:8944000000000000087", "enable:8944000000000000087"}) {
		t.Fatalf("ops=%v", ops)
	}
}

func TestScheduleLebaraUKIdentityRecoverSkipsWhileSwitching(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	mgr := &esim.Manager{}
	setEsimOverviewCacheForTest(t, mgr, &esim.EsimOverview{Profiles: []esim.EUICCProfiles{{
		Profiles: []esim.ProfileItem{{ICCID: "8944000000000000087", Name: "Lebara UK", State: 1}},
	}}})
	w := &Worker{ID: "dev-1", EsimMgr: mgr}
	w.state.Identity.ICCID = "8944000000000000087"
	w.state.Identity.IMSI = "204040000000001"
	p.workers[w.ID] = w
	p.switchMu.Lock()
	p.switchingDevices[w.ID] = true
	p.switchMu.Unlock()
	p.scheduleLebaraUKIdentityRecover(w, false)
	if p.LebaraUKIdentityRecoverSnapshot(w.ID).InFlight {
		t.Fatal("auto recover must not start while an eSIM switch is still running")
	}
}

func TestRestorePostSwitchConnectivityDoesNotWaitOnClassifyFailed(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	be := &esimSwitchRestoreBackendStub{
		mode:      backend.BackendQMI,
		liveICCID: "460001234567890",
		liveIMSI:  "460001234567890",
	}
	w := &Worker{
		ID:      "dev-1",
		Backend: be,
		Config:  config.DeviceConfig{ID: "dev-1", VoWiFiEnabled: true},
	}
	w.state.Identity.IMSI = "460001234567890"
	w.state.Identity.ICCID = "460001234567890"
	p.workers[w.ID] = w
	var got []string
	p.voWiFiHost().LifecycleControllerForTest().TestRun = func(ctx context.Context, cmd vowifihost.LifecycleCommand) error {
		got = append(got, cmd.Kind.String())
		return nil
	}
	p.restorePostSwitchConnectivity(w.ID, w, esimSwitchContext{
		TargetICCID:           "460001234567890",
		TargetClassifyFailed:  true,
		TargetLebaraCandidate: false,
	}, nil, true)
	if !reflect.DeepEqual(got, []string{"switch_end"}) {
		t.Fatalf("commands=%v want switch_end without 23487 wait", got)
	}
}

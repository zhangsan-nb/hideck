package volte

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

func TestControllerReturnsRetryableErrorAfterATProvisionWhenIMSClientsFail(t *testing.T) {
	host := newFakeModem()
	host.EnsureErr = errors.New("allocate IMS: allocate client ID request failed after retries: write failed: write unix @->@qmi-proxy: write: broken pipe")
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err == nil {
		t.Fatal("QMI IMS allocation failure must reach the retry scheduler")
	}
	st := ctl.Status("wwan1")
	if !st.QMIIMSUnavailable {
		t.Fatal("QMI IMS allocate failure must be visible")
	}
	if !st.IMSEnabled || st.Phase != PhaseFailed {
		t.Fatalf("AT provisioning must remain visible without claiming success: %+v", st)
	}
}

func TestControllerEnablesIMSAndUACWithoutClaimingUACReady(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	if host.StopIMS != 1 || host.SetNative != 1 {
		t.Fatalf("stop=%d setNative=%d", host.StopIMS, host.SetNative)
	}
	st := ctl.Status("wwan1")
	if !st.IMSEnabled || !st.VoLTEEnabled || !st.IMSRegistered || st.Phase != PhaseRegistered {
		t.Fatalf("status %+v", st)
	}
	if !st.RebootRequired {
		t.Fatal("UAC enable should mark reboot_required")
	}
	if st.UACEnabled {
		t.Fatal("must not claim UAC ready before re-enumeration verify")
	}
	var sawEnableIMS, sawEnableUAC bool
	for _, cmd := range host.Commands {
		if cmd == IMSEnableCommand() {
			sawEnableIMS = true
		}
		if cmd == `AT+QCFG="usbcfg",0x2C7C,0x125,1,1,1,1,1,0,1` {
			sawEnableUAC = true
		}
	}
	if !sawEnableIMS || !sawEnableUAC {
		t.Fatalf("commands %v", host.Commands)
	}
}

func TestControllerUnverifiedWhenIMSAMissing(t *testing.T) {
	host := newFakeModem()
	host.IMS, host.VoLTE = 1, 1
	host.USB[len(host.USB)-1] = "1"
	host.RegErr = context.DeadlineExceeded
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	st := ctl.Status("wwan1")
	if st.Phase != PhaseUnverified || !st.IMSEnabled {
		t.Fatalf("want unverified, got %+v", st)
	}
}

func TestControllerFailsWhenIMSClientsUnavailable(t *testing.T) {
	host := newFakeModem()
	host.IMS, host.VoLTE = 1, 1
	host.USB[len(host.USB)-1] = "1"
	host.EnsureErr = errors.New("QMI 服务未就绪: IMSA")
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err == nil {
		t.Fatal("want typed IMSA unavailable")
	}
	st := ctl.Status("wwan1")
	if !st.QMIIMSUnavailable || st.Phase != PhaseFailed {
		t.Fatalf("status %+v", st)
	}
}

func TestControllerReleasesIMSClientsAndFollowsIndications(t *testing.T) {
	host := newFakeModem()
	host.IMS, host.VoLTE = 1, 1
	host.USB[len(host.USB)-1] = "1"
	host.RegErr = context.DeadlineExceeded
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	if host.EnsureCount != 1 {
		t.Fatalf("ensure=%d", host.EnsureCount)
	}
	host.fireIMSRegistration(&qmi.IMSARegistrationStatus{
		HasStatus: true,
		Status:    qmi.IMSARegistrationStateRegistered,
	})
	deadline := time.Now().Add(time.Second)
	for !ctl.Status("wwan1").Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	st := ctl.Status("wwan1")
	if st.Phase != PhaseRegistered || !st.IMSRegistered {
		t.Fatalf("after indication %+v", st)
	}
	ctl.Disable("wwan1")
	if host.ReleaseCount != 1 {
		t.Fatalf("release=%d", host.ReleaseCount)
	}
	if ctl.Status("wwan1").Phase != PhaseIdle {
		t.Fatalf("after disable %+v", ctl.Status("wwan1"))
	}
}

func TestControllerForcesNumericCOPSBeforePLMN(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	var sawFormat, sawCEREG, sawCOPS bool
	formatAt := -1
	copsAt := -1
	for i, cmd := range host.Commands {
		if cmd == COPSNumericFormatCommand() {
			sawFormat = true
			formatAt = i
		}
		if cmd == CEREGQueryCommand() {
			sawCEREG = true
		}
		if cmd == COPSQueryCommand() && copsAt < 0 {
			copsAt = i
			sawCOPS = true
		}
	}
	if !sawFormat || !sawCEREG || !sawCOPS || formatAt > copsAt {
		t.Fatalf("command order %v", host.Commands)
	}
}

func TestControllerRejectsUnknownPLMNWithoutGuessingMBN(t *testing.T) {
	host := newFakeModem()
	host.COPS = "23433"
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err == nil || !errors.Is(err, ErrNoUniqueProfile) {
		t.Fatalf("UK PLMN must not guess an MBN: %v", err)
	}
	if ctl.Status("wwan1").Phase != PhaseFailed {
		t.Fatalf("status %+v", ctl.Status("wwan1"))
	}
}

func TestControllerKeepsGoingWhenVoLTEBitStuck(t *testing.T) {
	host := newFakeModem()
	host.FailIMS11 = true
	host.Reg = Registration{}
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	st := ctl.Status("wwan1")
	if !st.IMSEnabled || st.VoLTEEnabled {
		t.Fatalf("status %+v", st)
	}
	if st.ProvisionStage != StageApplyIMS {
		t.Fatalf("want apply_ims drift recorded, got %+v", st)
	}
}

func TestControllerQueriesMBNListOnceBeforeIMSWrite(t *testing.T) {
	host := newFakeModem()
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, cmd := range host.Commands {
		if strings.HasPrefix(cmd, `AT+QCFG="ims",`) {
			break
		}
		if cmd == MBNListQueryCommand() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 MBN list before IMS write, got %d in %v", n, host.Commands)
	}
}

func TestControllerRetriesMBNListTimeout(t *testing.T) {
	host := newFakeModem()
	host.ListTimeouts = 1
	ctl := NewControllerWithBackup(host, t.TempDir())
	if err := ctl.Enable(context.Background(), "wwan1"); err != nil {
		t.Fatal(err)
	}
	if st := ctl.Status("wwan1"); st.MBNName != "Volte_OpenMkt-Commercial-CMCC" {
		t.Fatalf("status %+v", st)
	}
}

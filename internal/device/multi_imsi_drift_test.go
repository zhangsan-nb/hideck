package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yibaiba/hideck/internal/backend"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
)

func TestMultiIMSIHomePrefixNeedsAnEarlierHomeIMSI(t *testing.T) {
	history := map[string][]string{
		"DITO":    {"204040000000001", "515660000000001"},
		"NL-ONLY": {"204040000000002"},
		"OTHER":   {"204040000000003", "260010000000003"},
		"LEBARA":  {"234870000000004", "204040000000004"},
	}
	known := func(iccid string) ([]string, error) { return history[iccid], nil }

	if home, drifted, err := multiIMSIHomePrefix("DITO", "204040000000001", known); err != nil || !drifted || home != "51566" {
		t.Fatalf("DITO on 20404: home=%q drifted=%v err=%v", home, drifted, err)
	}
	if _, drifted, _ := multiIMSIHomePrefix("DITO", "515660000000001", known); drifted {
		t.Fatal("home IMSI must not count as drift")
	}
	if _, drifted, _ := multiIMSIHomePrefix("NL-ONLY", "204040000000002", known); drifted {
		t.Fatal("a card only ever seen on 20404 is not drifted")
	}
	if _, drifted, _ := multiIMSIHomePrefix("OTHER", "204040000000003", known); drifted {
		t.Fatal("an unlisted home PLMN is not drifted")
	}
	if _, drifted, _ := multiIMSIHomePrefix("LEBARA", "204040000000004", known); drifted {
		t.Fatal("Lebara UK on 20404 must stay with the Lebara handling")
	}
}

func stubIMSIHistory(t *testing.T, history map[string][]string, err error) {
	t.Helper()
	prev := listIMSIsForICCID
	listIMSIsForICCID = func(iccid string) ([]string, error) { return history[iccid], err }
	t.Cleanup(func() {
		listIMSIsForICCID = prev
		multiIMSIDriftMu.Lock()
		multiIMSIDrifts = map[string]*multiIMSIDriftState{}
		multiIMSIDriftMu.Unlock()
	})
}

func driftSince(deviceID string) time.Time {
	multiIMSIDriftMu.Lock()
	defer multiIMSIDriftMu.Unlock()
	if state := multiIMSIDrifts[deviceID]; state != nil {
		return state.since
	}
	return time.Time{}
}

func TestCheckMultiIMSIDriftTracksOneWaitPerCard(t *testing.T) {
	stubIMSIHistory(t, map[string][]string{
		"DITO-A": {"515660000000001"},
		"DITO-B": {"515660000000002"},
	}, nil)
	t0 := time.Date(2026, 10, 17, 13, 40, 0, 0, time.UTC)

	if err := checkMultiIMSIDrift("u1", "DITO-A", "204040000000001", "t", t0); !errors.Is(err, ErrMultiIMSIDrift) {
		t.Fatalf("drifted card err=%v, want ErrMultiIMSIDrift", err)
	}
	_ = checkMultiIMSIDrift("u1", "DITO-A", "204040000000001", "t", t0.Add(2*time.Minute))
	if got := driftSince("u1"); !got.Equal(t0) {
		t.Fatalf("retry restarted the wait: since=%s", got)
	}
	_ = checkMultiIMSIDrift("u1", "DITO-B", "204040000000002", "t", t0.Add(3*time.Minute))
	if got := driftSince("u1"); !got.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("another card inherited the wait: since=%s", got)
	}
	later := t0.Add(3*time.Minute + multiIMSIDriftForgetAfter + time.Minute)
	_ = checkMultiIMSIDrift("u1", "DITO-B", "204040000000002", "t", later)
	if got := driftSince("u1"); !got.Equal(later) {
		t.Fatalf("a drift after a long gap inherited the old wait: since=%s", got)
	}
	if err := checkMultiIMSIDrift("u1", "DITO-B", "515660000000002", "t", later.Add(time.Minute)); err != nil {
		t.Fatalf("home IMSI err=%v", err)
	}
	if got := driftSince("u1"); !got.IsZero() {
		t.Fatalf("recovered card kept its wait: since=%s", got)
	}
}

func TestCheckMultiIMSIDriftWaitsWhenHistoryCannotBeRead(t *testing.T) {
	stubIMSIHistory(t, nil, errors.New("db down"))
	now := time.Now()
	if err := checkMultiIMSIDrift("u1", "DITO-A", "204040000000001", "t", now); !errors.Is(err, ErrMultiIMSIDrift) {
		t.Fatalf("sponsor IMSI with unreadable history err=%v, want ErrMultiIMSIDrift", err)
	}
	if err := checkMultiIMSIDrift("u1", "DITO-A", "515660000000001", "t", now); err != nil {
		t.Fatalf("home IMSI must not query history: err=%v", err)
	}
}

// buildVoWiFiStartProfile is shared by the WiFi and cellular start paths.
func TestBuildVoWiFiStartProfileRefusesADriftedCard(t *testing.T) {
	stubIMSIHistory(t, map[string][]string{"8963600000000000001": {"515660000000001"}}, nil)
	p := NewPool(&config.Config{})
	b := &vowifiLiveIdentityBackendStub{
		workerSMSCBackendStub: workerSMSCBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
		},
		liveIMSI: "204040000000001", liveICCID: "8963600000000000001",
	}
	w := &Worker{ID: "dito", Backend: b}
	registerVoWiFiProfileWorkerForTest(p, w)

	if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-drift"); !errors.Is(err, ErrMultiIMSIDrift) {
		t.Fatalf("buildVoWiFiStartProfile() err=%v, want ErrMultiIMSIDrift", err)
	}
}

// Recording the roaming IMSI must not erase the home IMSI from the card's
// history, or the next retry would start on the sponsor preset.
func TestBuildVoWiFiStartProfileKeepsWaitingAfterRecordingTheRoamingIMSI(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	t.Cleanup(func() {
		multiIMSIDriftMu.Lock()
		multiIMSIDrifts = map[string]*multiIMSIDriftState{}
		multiIMSIDriftMu.Unlock()
	})
	const iccid, home, roaming = "8963600000000000001", "515660000000001", "204040000000001"
	if err := db.UpsertSIMCardIdentity(iccid, home, "", nil); err != nil {
		t.Fatal(err)
	}
	p := NewPool(&config.Config{})
	b := &vowifiLiveIdentityBackendStub{
		workerSMSCBackendStub: workerSMSCBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI, nativeMCC: "515", nativeMNC: "66"},
			seq:                     []smscResult{{value: "+639000000000"}},
		},
		liveIMSI: roaming, liveICCID: iccid,
	}
	w := &Worker{ID: "dito", Backend: b}
	registerVoWiFiProfileWorkerForTest(p, w)
	w.cacheMu.Lock()
	w.state.Identity.ICCID = iccid
	w.state.Identity.IMSI = home
	w.state.Identity.IMEI = "861234567890123"
	w.cacheMu.Unlock()

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-drift"); !errors.Is(err, ErrMultiIMSIDrift) {
			t.Fatalf("attempt %d on the roaming IMSI: err=%v, want ErrMultiIMSIDrift", attempt, err)
		}
	}
	if known, err := db.IMSIKnownForICCID(iccid, roaming); err != nil || !known {
		t.Fatalf("roaming IMSI recorded=%v err=%v", known, err)
	}

	b.liveIMSI = home
	if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-home"); err != nil {
		t.Fatalf("back on the home IMSI: err=%v", err)
	}
	if known, err := db.IMSIKnownForICCID(iccid, home); err != nil || !known {
		t.Fatalf("home IMSI still recorded=%v err=%v", known, err)
	}
}

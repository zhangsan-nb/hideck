package device

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yibaiba/hideck/internal/backend"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
	"github.com/yibaiba/hideck/internal/modem"
)

type smsAliasStoreStub struct {
	*smsIdentityStoreStub
	known map[string]bool // iccid+"/"+imsi
}

func (s smsAliasStoreStub) IMSIKnownForICCID(iccid, imsi string) (bool, error) {
	return s.known[iccid+"/"+imsi], nil
}

// The worker reads the home IMSI while the DB binding still holds the roaming IMSI
// of the same multi-IMSI card: the SMS belongs to this card, not to another one.
func TestProcessSMSAcceptsRuntimeIMSIReadOnTheSameCard(t *testing.T) {
	base := &smsIdentityStoreStub{identities: map[string]SMSIdentity{
		"dev-1": {ICCID: "89636CARD", IMSI: "20404ROAM"},
	}}
	store := smsAliasStoreStub{smsIdentityStoreStub: base, known: map[string]bool{"89636CARD/51566HOME": true}}
	pool := &Pool{workers: make(map[string]*Worker), smsIdentities: store}
	worker := &Worker{ID: "dev-1", Pool: pool}
	setWorkerSMSIdentity(worker, SMSIdentity{ICCID: "89636CARD", IMSI: "51566HOME"})
	pool.workers[worker.ID] = worker

	if err := worker.processSMS("DITO", "code", time.Now()); err != nil {
		t.Fatalf("processSMS(home IMSI) error=%v", err)
	}
	if len(base.saved) != 1 || base.saved[0].identity.IMSI != "51566HOME" {
		t.Fatalf("saved=%+v", base.saved)
	}

	setWorkerSMSIdentity(worker, SMSIdentity{ICCID: "89636CARD", IMSI: "99999NEVER"})
	if err := worker.processSMS("DITO", "wrong", time.Now()); !errors.Is(err, ErrSMSIdentityConflict) {
		t.Fatalf("processSMS(unknown IMSI) error=%v, want conflict", err)
	}
}

func TestSyncVoWiFiStartIdentityAdoptsLiveIMSIOfSameCard(t *testing.T) {
	worker := &Worker{ID: "dev-1"}
	setWorkerSMSIdentity(worker, SMSIdentity{ICCID: "89636CARD", IMSI: "20404ROAM"})
	worker.state.Identity.NativeMCC, worker.state.Identity.NativeMNC = "204", "04"

	if persist, err := syncVoWiFiStartIdentity(worker, "89636CARD", "51566HOME", "t"); err != nil || !persist {
		t.Fatalf("sync persist=%v error=%v", persist, err)
	}
	if persist, _ := syncVoWiFiStartIdentity(worker, "89636CARD", "51566HOME", "t"); persist {
		t.Fatal("unchanged identity must not be persisted again")
	}
	if got := worker.state.Identity; got.IMSI != "51566HOME" || got.NativeMCC != "" || got.NativeMNC != "" {
		t.Fatalf("identity after sync=%+v", got)
	}

	if _, err := syncVoWiFiStartIdentity(worker, "89440OTHER", "23487HOME", "t"); err == nil {
		t.Fatal("sync with a different ICCID must abort the start")
	}
}

// The backend derives home MCC/MNC from its snapshot IMSI. When a multi-IMSI SIM has
// already switched, the profile must follow the live IMSI or the wrong preset is picked.
func TestResolveVoWiFiProfileMCCMNCFollowsLiveIMSI(t *testing.T) {
	worker := &Worker{ID: "dev-1", Backend: &workerStatusBackendStub{nativeMCC: "204", nativeMNC: "04"}}

	mcc, mnc, source := resolveVoWiFiProfileMCCMNC(context.Background(), worker, modem.DeviceStatus{}, "515660000000001", "t")
	if mcc != "515" || mnc != "66" || source != "sim_home" {
		t.Fatalf("got %s/%s source=%s, want 515/66 from live IMSI", mcc, mnc, source)
	}

	mcc, mnc, _ = resolveVoWiFiProfileMCCMNC(context.Background(), worker, modem.DeviceStatus{}, "204040000000001", "t")
	if mcc != "204" || mnc != "04" {
		t.Fatalf("matching backend PLMN changed: %s/%s", mcc, mnc)
	}
}

// An IMSI the card reports but the database never recorded (an earlier write
// failed) is written again on the next start.
func TestBuildVoWiFiStartProfileRecordsAnIMSIMissingFromHistory(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	const iccid, imsi = "8963600000000000001", "515660000000001"
	p := NewPool(&config.Config{})
	b := &vowifiLiveIdentityBackendStub{
		workerSMSCBackendStub: workerSMSCBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI, nativeMCC: "515", nativeMNC: "66"},
			seq:                     []smscResult{{value: "+639000000000"}},
		},
		liveIMSI: imsi, liveICCID: iccid,
	}
	w := &Worker{ID: "dito", Backend: b}
	registerVoWiFiProfileWorkerForTest(p, w)
	w.cacheMu.Lock()
	w.state.Identity.ICCID = iccid
	w.state.Identity.IMSI = imsi
	w.state.Identity.IMEI = "861234567890123"
	w.cacheMu.Unlock()

	if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-record"); err != nil {
		t.Fatalf("buildVoWiFiStartProfile() err=%v", err)
	}
	known, err := db.IMSIKnownForICCID(iccid, imsi)
	if err != nil || !known {
		t.Fatalf("IMSIKnownForICCID()=%v err=%v, want the start to record it", known, err)
	}
}

// After a drift wait recorded the roaming IMSI, a status refresh can put the
// home IMSI back in the cache only. Both IMSIs are in the card's history, so
// the start must compare against sim_cards, not the history.
func TestBuildVoWiFiStartProfileRewritesARoamingIMSILeftInSIMCards(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	const iccid, home, roaming = "8963600000000000001", "515660000000001", "204040000000001"
	if err := db.UpsertSIMCardIdentity(iccid, home, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSIMCardIdentity(iccid, roaming, "", nil); err != nil {
		t.Fatal(err)
	}
	p := NewPool(&config.Config{})
	b := &vowifiLiveIdentityBackendStub{
		workerSMSCBackendStub: workerSMSCBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI, nativeMCC: "515", nativeMNC: "66"},
			seq:                     []smscResult{{value: "+639000000000"}},
		},
		liveIMSI: home, liveICCID: iccid,
	}
	w := &Worker{ID: "dito", Backend: b}
	registerVoWiFiProfileWorkerForTest(p, w)
	w.cacheMu.Lock()
	w.state.Identity.ICCID = iccid
	w.state.Identity.IMSI = home
	w.state.Identity.IMEI = "861234567890123"
	w.cacheMu.Unlock()

	if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-rewrite"); err != nil {
		t.Fatalf("buildVoWiFiStartProfile() err=%v", err)
	}
	held, err := db.SIMCardHoldsIMSI(iccid, home)
	if err != nil || !held {
		t.Fatalf("SIMCardHoldsIMSI(home)=%v err=%v, want sim_cards back on the home IMSI", held, err)
	}
}

// A card already stored with its live IMSI is not written again on each start.
// The worker must be QMI with an IMEI: only then does PersistIdentityState
// record current_imei, which is what this test watches.
func TestBuildVoWiFiStartProfileSkipsPersistWhenSIMCardsMatches(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	const iccid, imsi = "8963600000000000001", "515660000000001"
	if err := db.UpsertSIMCardIdentity(iccid, imsi, "", nil); err != nil {
		t.Fatal(err)
	}
	p := NewPool(&config.Config{})
	b := &vowifiLiveIdentityBackendStub{
		workerSMSCBackendStub: workerSMSCBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI, nativeMCC: "515", nativeMNC: "66"},
			seq:                     []smscResult{{value: "+639000000000"}},
		},
		liveIMSI: imsi, liveICCID: iccid,
	}
	w := &Worker{ID: "dito", Backend: b}
	registerVoWiFiProfileWorkerForTest(p, w)
	w.cacheMu.Lock()
	w.state.Identity.ICCID = iccid
	w.state.Identity.IMSI = imsi
	w.state.Identity.IMEI = "861234567890123"
	w.cacheMu.Unlock()

	if _, err := p.buildVoWiFiStartProfile(context.Background(), w, "trace-skip"); err != nil {
		t.Fatalf("buildVoWiFiStartProfile() err=%v", err)
	}
	// PersistIdentityState would record the worker IMEI on the card.
	var sim db.SIMCard
	if err := db.DB.Where("iccid = ?", iccid).First(&sim).Error; err != nil {
		t.Fatal(err)
	}
	if sim.CurrentIMEI != nil {
		t.Fatalf("current_imei=%q, want the start to skip persisting a matching card", *sim.CurrentIMEI)
	}
}

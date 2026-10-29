package device

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/iniwex5/vowifi-go/runtimehost/eventhost"
	"github.com/yibaiba/hideck/internal/backend"
	"github.com/yibaiba/hideck/internal/config"
	"github.com/yibaiba/hideck/internal/db"
)

type workerPhoneBackendStub struct {
	workerStatusBackendStub
	imsi   string
	iccid  string
	msisdn string
}

func (s *workerPhoneBackendStub) GetIMSI(ctx context.Context) (string, error) {
	return s.imsi, nil
}

func (s *workerPhoneBackendStub) GetICCID(ctx context.Context) (string, error) {
	return s.iccid, nil
}

func (s *workerPhoneBackendStub) GetMSISDN(ctx context.Context) (string, error) {
	return s.msisdn, nil
}

type workerStartupIdentityBackendStub struct {
	workerPhoneBackendStub
	liveIMSI      string
	liveICCID     string
	liveNativeSPN string
}

func (s *workerStartupIdentityBackendStub) GetIMSILive(ctx context.Context) (string, error) {
	return s.liveIMSI, nil
}

func (s *workerStartupIdentityBackendStub) GetICCIDLive(ctx context.Context) (string, error) {
	return s.liveICCID, nil
}

func (s *workerStartupIdentityBackendStub) GetNativeSPNLive(ctx context.Context) (string, error) {
	return s.liveNativeSPN, nil
}

func initDevicePhoneNumberTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "device_phone_number.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatalf("db.Init() error=%v", err)
	}
	sqlDB, err := db.DB.DB()
	if err != nil {
		t.Fatalf("db.DB.DB() error=%v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
		db.DB = nil
	})
}

func loadDeviceTestSIMCardByIMSI(t *testing.T, imsi string) db.SIMCard {
	t.Helper()
	var sim db.SIMCard
	if err := db.DB.Where("imsi = ?", imsi).First(&sim).Error; err != nil {
		t.Fatalf("First(sim) error=%v", err)
	}
	return sim
}

func loadDeviceTestSIMSubscriptionByIMSI(t *testing.T, imsi string) db.SIMSubscription {
	t.Helper()
	var sub db.SIMSubscription
	if err := db.DB.Where("imsi = ?", imsi).First(&sub).Error; err != nil {
		t.Fatalf("First(subscription) error=%v", err)
	}
	return sub
}

func TestPersistPCSCIdentityWithoutModemRecord(test *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, imei := range []string{"", "stale-modem-imei"} {
			test.Run(fmt.Sprintf("configured=%t/imei=%s", configured, imei), func(test *testing.T) {
				initDevicePhoneNumberTestDB(test)
				pool := NewPool(nil)
				worker := &Worker{ID: "reader-1"}
				if configured {
					worker.Config = config.DeviceConfig{DeviceBackend: backend.BackendPCSC}
				} else {
					worker.Backend = &workerPhoneBackendStub{workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendPCSC}}
				}
				worker.state.Identity.IMEI = imei
				worker.state.Identity.ICCID = "8986000000000000099"
				worker.state.Identity.IMSI = "pcsc-imsi"
				worker.state.Runtime.Operator = "Test operator"
				previousIMEI := "previous-modem"
				if err := db.UpsertSIMCard(worker.state.Identity.ICCID, "old-imsi", "", "old operator", &previousIMEI); err != nil {
					test.Fatal(err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					pool.PersistRuntimeState(worker)
					pool.PersistIdentityState(worker)
				}
				sim := loadDeviceTestSIMCardByIMSI(test, "pcsc-imsi")
				if sim.ICCID != worker.state.Identity.ICCID || sim.Operator != "Test operator" || sim.CurrentIMEI != nil {
					test.Fatalf("unexpected SIM identity: %+v", sim)
				}
				subscription := loadDeviceTestSIMSubscriptionByIMSI(test, "pcsc-imsi")
				if subscription.CurrentICCID != sim.ICCID {
					test.Fatal("SIM subscription identity was not persisted")
				}
				var count int64
				if err := db.DB.Model(&db.Device{}).Count(&count).Error; err != nil || count != 0 {
					test.Fatalf("reader created a modem record: count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestPersistPCSCIdentityUsesNativeSPNWhenRuntimeOperatorIsEmpty(test *testing.T) {
	initDevicePhoneNumberTestDB(test)
	pool := NewPool(nil)
	worker := &Worker{
		ID:      "reader-native-spn",
		Backend: &workerPhoneBackendStub{workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendPCSC}},
	}
	worker.state.Identity.ICCID = "8986000000000000100"
	worker.state.Identity.IMSI = "pcsc-native-spn-imsi"
	worker.state.Identity.NativeSPN = "Native carrier"

	pool.PersistIdentityState(worker)

	sim := loadDeviceTestSIMCardByIMSI(test, worker.state.Identity.IMSI)
	if sim.Operator != "Native carrier" {
		test.Fatalf("SIM operator=%q want Native carrier", sim.Operator)
	}
	subscription := loadDeviceTestSIMSubscriptionByIMSI(test, worker.state.Identity.IMSI)
	if subscription.Operator != "Native carrier" {
		test.Fatalf("subscription operator=%q want Native carrier", subscription.Operator)
	}
}

func TestPersistModemStillRequiresIMEI(test *testing.T) {
	initDevicePhoneNumberTestDB(test)
	pool := NewPool(nil)
	worker := &Worker{ID: "modem", Backend: &workerPhoneBackendStub{workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI}}}
	worker.state.Identity.ICCID = "8986000000000000099"
	worker.state.Identity.IMSI = "modem-imsi"
	pool.PersistRuntimeState(worker)
	pool.PersistIdentityState(worker)
	for _, model := range []any{&db.Device{}, &db.SIMCard{}} {
		var count int64
		if err := db.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
			test.Fatalf("persisted modem without IMEI: count=%d err=%v", count, err)
		}
	}
}

func TestPersistIdentityStateStoresQMIMSISDNAsModemPhoneNumber(t *testing.T) {
	initDevicePhoneNumberTestDB(t)

	p := NewPool(nil)
	w := &Worker{
		ID: "dev-qmi",
		Backend: &workerPhoneBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
			imsi:                    "imsi-qmi-1",
			iccid:                   "8986000000000000001",
			msisdn:                  "+8613800138000",
		},
	}
	w.state.Identity.IMEI = "imei-qmi-1"
	w.state.Identity.IMSI = "imsi-qmi-1"
	w.state.Identity.ICCID = "8986000000000000001"
	w.state.Runtime.Operator = "中国移动"

	p.PersistIdentityState(w)

	sim := loadDeviceTestSIMCardByIMSI(t, "imsi-qmi-1")
	if sim.ICCID != "8986000000000000001" {
		t.Fatalf("SIMCard ICCID=%q want=8986000000000000001", sim.ICCID)
	}
	sub := loadDeviceTestSIMSubscriptionByIMSI(t, "imsi-qmi-1")
	if sub.ModemPhoneNumber != "+8613800138000" {
		t.Fatalf("ModemPhoneNumber=%q want=+8613800138000", sub.ModemPhoneNumber)
	}
	if sub.PhoneNumber != "+8613800138000" {
		t.Fatalf("PhoneNumber=%q want=+8613800138000", sub.PhoneNumber)
	}
	if sub.CurrentICCID != "8986000000000000001" {
		t.Fatalf("CurrentICCID=%q want=8986000000000000001", sub.CurrentICCID)
	}
}

func TestPersistIdentityStateStoresATMSISDNAsModemPhoneNumber(t *testing.T) {
	initDevicePhoneNumberTestDB(t)

	p := NewPool(nil)
	w := &Worker{
		ID: "dev-at",
		Backend: &workerPhoneBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendAT},
			imsi:                    "imsi-at-1",
			iccid:                   "8986000000000000002",
			msisdn:                  "+8613900139000",
		},
	}
	w.state.Identity.IMEI = "imei-at-1"
	w.state.Identity.IMSI = "imsi-at-1"
	w.state.Identity.ICCID = "8986000000000000002"
	w.state.Runtime.Operator = "中国联通"

	p.PersistIdentityState(w)

	sim := loadDeviceTestSIMCardByIMSI(t, "imsi-at-1")
	if sim.ICCID != "8986000000000000002" {
		t.Fatalf("SIMCard ICCID=%q want=8986000000000000002", sim.ICCID)
	}
	sub := loadDeviceTestSIMSubscriptionByIMSI(t, "imsi-at-1")
	if sub.ModemPhoneNumber != "+8613900139000" {
		t.Fatalf("ModemPhoneNumber=%q want=+8613900139000", sub.ModemPhoneNumber)
	}
	if sub.PhoneNumber != "+8613900139000" {
		t.Fatalf("PhoneNumber=%q want=+8613900139000", sub.PhoneNumber)
	}
	if sub.CurrentICCID != "8986000000000000002" {
		t.Fatalf("CurrentICCID=%q want=8986000000000000002", sub.CurrentICCID)
	}
}

func TestVoWiFiLocalNumberEventWritesHigherPriorityPhoneNumber(t *testing.T) {
	initDevicePhoneNumberTestDB(t)

	if err := db.UpdateSIMCardModemPhoneNumberByIMSI("imsi-vowifi-1", "+8613500135000"); err != nil {
		t.Fatalf("UpdateSIMCardModemPhoneNumberByIMSI() error=%v", err)
	}

	p := NewPool(nil)
	p.workers["dev-phone"] = &Worker{ID: "dev-phone", Backend: &workerPhoneBackendStub{imsi: "imsi-vowifi-1"}}
	if err := (vowifiSMSHistoryRecorder{pool: p}).RecordLocalNumberLearned(eventhost.LocalNumberLearned{
		DevID:  "dev-phone",
		IMSI:   "imsi-vowifi-1",
		Number: "+8613600136000",
		Source: "register",
	}); err != nil {
		t.Fatalf("RecordLocalNumberLearned() error=%v", err)
	}

	sub := loadDeviceTestSIMSubscriptionByIMSI(t, "imsi-vowifi-1")
	if sub.ModemPhoneNumber != "+8613500135000" {
		t.Fatalf("ModemPhoneNumber=%q want=+8613500135000", sub.ModemPhoneNumber)
	}
	if sub.VowifiPhoneNumber != "+8613600136000" {
		t.Fatalf("VowifiPhoneNumber=%q want=+8613600136000", sub.VowifiPhoneNumber)
	}
	if sub.PhoneNumber != "+8613600136000" {
		t.Fatalf("PhoneNumber=%q want=+8613600136000", sub.PhoneNumber)
	}
}

func TestStartupPostApplyPersistsLiveIdentityBeforePhoneNumber(t *testing.T) {
	initDevicePhoneNumberTestDB(t)

	p := NewPool(nil)
	w := &Worker{
		ID: "dev-startup",
		Backend: &workerStartupIdentityBackendStub{
			workerPhoneBackendStub: workerPhoneBackendStub{
				workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
				imsi:                    "stale-imsi",
				iccid:                   "stale-iccid",
				msisdn:                  "+8613700137000",
			},
			liveIMSI:  "live-imsi",
			liveICCID: "8986000000000000009",
		},
	}
	w.state.Identity.IMEI = "imei-startup-1"

	if err := w.RefreshRuntime(nil, "startup_post_apply"); err != nil {
		t.Fatalf("RefreshRuntime() error=%v", err)
	}
	if err := w.RefreshIdentityLive(nil, "startup_post_apply"); err != nil {
		t.Fatalf("RefreshIdentityLive() error=%v", err)
	}
	p.PersistRuntimeState(w)
	p.PersistIdentityState(w)

	var staleCount int64
	if err := db.DB.Model(&db.SIMCard{}).Where("imsi = ? OR iccid = ?", "stale-imsi", "stale-iccid").Count(&staleCount).Error; err != nil {
		t.Fatalf("Count(stale sim) error=%v", err)
	}
	if staleCount != 0 {
		t.Fatalf("startup_post_apply persisted stale identity before live refresh; stale sim rows=%d want=0", staleCount)
	}

	var sim db.SIMCard
	if err := db.DB.Where("imsi = ?", "live-imsi").First(&sim).Error; err != nil {
		t.Fatalf("startup_post_apply should persist live identity before phone number; live sim row missing: %v", err)
	}
	if sim.IMSI != "live-imsi" {
		t.Fatalf("IMSI=%q want=live-imsi", sim.IMSI)
	}
	if sim.ICCID != "8986000000000000009" {
		t.Fatalf("ICCID=%q want=8986000000000000009", sim.ICCID)
	}
	sub := loadDeviceTestSIMSubscriptionByIMSI(t, "live-imsi")
	if sub.ModemPhoneNumber != "+8613700137000" {
		t.Fatalf("ModemPhoneNumber=%q want=+8613700137000", sub.ModemPhoneNumber)
	}
	if sub.CurrentICCID != "8986000000000000009" {
		t.Fatalf("CurrentICCID=%q want=8986000000000000009", sub.CurrentICCID)
	}
}

func TestRefreshIdentityLiveClearsNativeSPNWhenIdentityChangesToEmptySPN(t *testing.T) {
	w := &Worker{
		ID: "dev-spn-refresh",
		Backend: &workerStartupIdentityBackendStub{
			workerPhoneBackendStub: workerPhoneBackendStub{
				workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
			},
			liveIMSI:      "460011234567890",
			liveICCID:     "8986010000000000001",
			liveNativeSPN: "",
		},
	}
	w.state.Identity.IMSI = "460001234567890"
	w.state.Identity.ICCID = "8986000000000000001"
	w.state.Identity.NativeSPN = "中国移动"

	if err := w.RefreshIdentityLive(nil, "test_identity_changed_empty_spn"); err != nil {
		t.Fatalf("RefreshIdentityLive() error=%v", err)
	}

	if got := w.state.Identity.NativeSPN; got != "" {
		t.Fatalf("NativeSPN=%q want empty after identity changed with empty SPN", got)
	}
}

func TestRefreshIdentityLiveKeepsNativeSPNWhenIdentityUnchangedAndSPNEmpty(t *testing.T) {
	w := &Worker{
		ID: "dev-spn-refresh-unchanged",
		Backend: &workerStartupIdentityBackendStub{
			workerPhoneBackendStub: workerPhoneBackendStub{
				workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
			},
			liveIMSI:      "460001234567890",
			liveICCID:     "8986000000000000001",
			liveNativeSPN: "",
		},
	}
	w.state.Identity.IMSI = "460001234567890"
	w.state.Identity.ICCID = "8986000000000000001"
	w.state.Identity.NativeSPN = "中国移动"

	if err := w.RefreshIdentityLive(nil, "test_identity_unchanged_empty_spn"); err != nil {
		t.Fatalf("RefreshIdentityLive() error=%v", err)
	}

	if got := w.state.Identity.NativeSPN; got != "中国移动" {
		t.Fatalf("NativeSPN=%q want preserved", got)
	}
}

func TestPersistIdentityStateDoesNotClearExistingVoWiFiPhoneWhenModemPhoneUnavailable(t *testing.T) {
	initDevicePhoneNumberTestDB(t)

	if err := db.UpdateSIMCardVoWiFiPhoneNumberByIMSI("imsi-vowifi-keep", "+8613600136000"); err != nil {
		t.Fatalf("UpdateSIMCardVoWiFiPhoneNumberByIMSI() error=%v", err)
	}

	p := NewPool(nil)
	w := &Worker{
		ID: "dev-vowifi-keep",
		Backend: &workerPhoneBackendStub{
			workerStatusBackendStub: workerStatusBackendStub{mode: backend.BackendQMI},
			imsi:                    "imsi-vowifi-keep",
			iccid:                   "8986000000000000010",
			msisdn:                  "",
		},
	}
	w.state.Identity.IMEI = "imei-vowifi-keep"
	w.state.Identity.IMSI = "imsi-vowifi-keep"
	w.state.Identity.ICCID = "8986000000000000010"
	w.state.Runtime.Operator = "中国移动"

	p.PersistIdentityState(w)

	sim := loadDeviceTestSIMCardByIMSI(t, "imsi-vowifi-keep")
	if sim.ICCID != "8986000000000000010" {
		t.Fatalf("SIMCard ICCID=%q want=8986000000000000010", sim.ICCID)
	}
	sub := loadDeviceTestSIMSubscriptionByIMSI(t, "imsi-vowifi-keep")
	if sub.VowifiPhoneNumber != "+8613600136000" {
		t.Fatalf("VowifiPhoneNumber=%q want=+8613600136000", sub.VowifiPhoneNumber)
	}
	if sub.PhoneNumber != "+8613600136000" {
		t.Fatalf("PhoneNumber=%q want=+8613600136000", sub.PhoneNumber)
	}
}

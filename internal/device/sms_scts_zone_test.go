package device

import (
	"context"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/eventhost"
	"github.com/yibaiba/hideck/internal/db"
)

func TestCorrectSMSCTimestampOnlyRezonesListedPLMNWithZeroOffset(t *testing.T) {
	utc := time.Date(2026, 10, 17, 17, 26, 32, 0, time.UTC)
	received := time.Date(2026, 10, 17, 17, 26, 34, 0, time.FixedZone("", 8*60*60))

	got := correctSMSCTimestamp("u1", "515660000000001", utc, received)
	if got.Format(time.RFC3339) != "2026-10-17T17:26:32+08:00" {
		t.Fatalf("DITO zero-zone SCTS=%s, want wall time kept at +08:00", got.Format(time.RFC3339))
	}

	withZone := time.Date(2026, 10, 17, 17, 26, 32, 0, time.FixedZone("", 8*60*60))
	if got := correctSMSCTimestamp("u1", "515660000000001", withZone, received); !got.Equal(withZone) {
		t.Fatalf("SCTS with a zone must stay: %s", got)
	}
	if got := correctSMSCTimestamp("u1", "515020000000001", utc, received); !got.Equal(utc) {
		t.Fatalf("unlisted PLMN changed: %s", got)
	}
}

func TestCorrectSMSCTimestampKeepsZeroZoneThatDoesNotMatchReceiveTime(t *testing.T) {
	received := time.Date(2026, 10, 17, 17, 26, 34, 0, time.FixedZone("", 8*60*60))

	realUTC := received.UTC().Add(-2 * time.Second)
	if got := correctSMSCTimestamp("u1", "515660000000001", realUTC, received); !got.Equal(realUTC) {
		t.Fatalf("real UTC SCTS re-zoned to %s", got.Format(time.RFC3339))
	}
	backlog := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	if got := correctSMSCTimestamp("u1", "515660000000001", backlog, received); !got.Equal(backlog) {
		t.Fatalf("backlog SCTS re-zoned to %s", got.Format(time.RFC3339))
	}
}

// ditoSCTSNow is DITO's SMSC stamp for a message sent now: Philippine wall time
// with a zero zone.
func ditoSCTSNow() time.Time {
	wall := time.Now().In(time.FixedZone("", 8*60*60))
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
}

type capturingSMSStore struct {
	observerSMSStore
	saved []inboundSMSRecord
}

func (s *capturingSMSStore) SaveReceived(_ SMSIdentity, message inboundSMSRecord) error {
	s.saved = append(s.saved, message)
	return nil
}

func TestCellularSMSStoresDITOZeroZoneTimestampAsPhilippineTime(t *testing.T) {
	p := NewPool(nil)
	store := &capturingSMSStore{}
	p.smsIdentities = store
	worker := &Worker{ID: "wwan0", Pool: p}
	var got []InboundSMS
	p.OnInboundSMS(func(m InboundSMS) error { got = append(got, m); return nil })

	identity := SMSIdentity{ICCID: "iccid", IMSI: "515660000000001"}
	if err := worker.processSMSWithIdentity(identity, inboundSMSRecord{Sender: "DITO", Content: "code", Timestamp: ditoSCTSNow()}); err != nil {
		t.Fatalf("processSMSWithIdentity() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(got))
	}
	if _, offset := got[0].Time.Zone(); offset != 8*60*60 {
		t.Fatalf("observed SCTS = %s, want +08:00", got[0].Time.Format(time.RFC3339))
	}
	if len(store.saved) != 1 || !store.saved[0].Timestamp.Equal(got[0].Time) {
		t.Fatalf("saved = %+v, want the corrected SCTS", store.saved)
	}
	if _, offset := store.saved[0].Timestamp.Zone(); offset != 8*60*60 {
		t.Fatalf("saved SCTS = %s, want +08:00", store.saved[0].Timestamp.Format(time.RFC3339))
	}
}

type timestampVoWiFiNotifier struct {
	times []time.Time
}

func (n *timestampVoWiFiNotifier) NotifySMS(deviceID, sender, content string, timestamp time.Time) {
	n.times = append(n.times, timestamp)
}

func (n *timestampVoWiFiNotifier) NotifySMSWithSource(deviceID, sender, content, source string, timestamp time.Time) {
	n.times = append(n.times, timestamp)
}

func (n *timestampVoWiFiNotifier) NotifyRaw(string) {}

func (n *timestampVoWiFiNotifier) NotifyIPRotated(string, string, string, time.Duration) {}

func TestVoWiFiSMSStoresAndNotifiesDITOZeroZoneTimestampAsPhilippineTime(t *testing.T) {
	initDevicePhoneNumberTestDB(t)
	const imsi = "515660000000001"
	p := NewPool(nil)
	p.workers["dev-dito"] = &Worker{ID: "dev-dito", Backend: &workerPhoneBackendStub{imsi: imsi}}
	setWorkerSMSIdentity(p.workers["dev-dito"], SMSIdentity{ICCID: "8963600000000000001", IMSI: imsi})
	notifier := &timestampVoWiFiNotifier{}
	p.SetNotifier(notifier)

	poolVoWiFiRuntimeDispatcher{pool: p}.Dispatch(context.Background(), eventhost.SMSReceived{
		DevID: "dev-dito", Sender: "DITO", Content: "code", Time: ditoSCTSNow(),
	})

	if len(notifier.times) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.times))
	}
	if _, offset := notifier.times[0].Zone(); offset != 8*60*60 {
		t.Fatalf("notified SCTS = %s, want +08:00", notifier.times[0].Format(time.RFC3339))
	}
	var stored db.SMS
	if err := db.DB.Where("imsi = ? AND type = ?", imsi, 1).First(&stored).Error; err != nil {
		t.Fatalf("stored SMS: %v", err)
	}
	if !stored.Timestamp.Equal(notifier.times[0]) {
		t.Fatalf("stored SCTS = %s, notified %s", stored.Timestamp.Format(time.RFC3339), notifier.times[0].Format(time.RFC3339))
	}
}

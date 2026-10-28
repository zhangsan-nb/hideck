package device

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/messaging"
	"github.com/yibaiba/hideck/internal/db"
)

func TestLoadInboundFragmentsReturnsRequestedOwner(t *testing.T) {
	previousDB := db.DB
	if err := db.Init(filepath.Join(t.TempDir(), "fragments.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		db.DB = previousDB
	})

	store := vowifiDeliveryStore{}
	saved := messaging.InboundFragmentOwner{DeviceID: "dev-a", ICCID: "89640a0000000000001f", IMSI: "530010000000001"}
	if _, err := store.SaveInboundFragment(
		messaging.InboundFragmentScope{Owner: saved, SessionKey: "session"},
		messaging.InboundFragment{Reference: 7, Total: 2, Sequence: 1, Content: "part", ArrivedAt: time.Now()},
	); err != nil {
		t.Fatalf("save fragment: %v", err)
	}

	for _, owner := range []messaging.InboundFragmentOwner{
		saved,
		{DeviceID: "dev-b", ICCID: "89640A0000000000001F", IMSI: "530010000000001"},
	} {
		records, err := store.LoadInboundFragments(owner)
		if err != nil {
			t.Fatalf("load fragments for %+v: %v", owner, err)
		}
		if len(records) != 1 || records[0].Scope.Owner != owner {
			t.Fatalf("load fragments for %+v = %+v", owner, records)
		}
	}

	for _, other := range []messaging.InboundFragmentOwner{
		{DeviceID: "dev-a", ICCID: "8964010000000000002", IMSI: saved.IMSI},
		{DeviceID: "dev-a", ICCID: saved.ICCID, IMSI: "530010000000002"},
	} {
		records, err := store.LoadInboundFragments(other)
		if err != nil {
			t.Fatalf("load fragments for %+v: %v", other, err)
		}
		if len(records) != 0 {
			t.Fatalf("another SIM %+v loaded %+v", other, records)
		}
	}
}

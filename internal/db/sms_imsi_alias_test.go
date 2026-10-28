package db

import (
	"errors"
	"testing"
	"time"
)

// A multi-IMSI SIM keeps its ICCID while EF_IMSI flips between the home IMSI and
// a roaming IMSI. Both were read live on the card, so SMS addressed to either must
// be stored; an IMSI never read on the card stays a conflict.
func seedMultiIMSICard(t *testing.T) {
	t.Helper()
	openTestDB(t)
	if err := UpsertSIMCardIdentity("ICC_MULTI", "IMSI_HOME", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := UpsertSIMCardIdentity("ICC_MULTI", "IMSI_ROAM", "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestSaveSMSForIdentityAcceptsIMSIReadOnTheSameCard(t *testing.T) {
	seedMultiIMSICard(t)

	err := SaveSMSForIdentity(SMSRecord{
		Identity: SMSIdentity{ICCID: "ICC_MULTI", IMSI: "IMSI_HOME"},
		Sender:   "+10086", Content: "code", Type: 1, Status: 0, Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("SaveSMSForIdentity(home IMSI) error=%v", err)
	}

	err = SaveSMSForIdentity(SMSRecord{
		Identity: SMSIdentity{ICCID: "ICC_MULTI", IMSI: "IMSI_NEVER"},
		Sender:   "+10086", Content: "wrong card", Type: 1, Status: 0, Timestamp: time.Now(),
	})
	if !errors.Is(err, ErrSMSIdentityConflict) {
		t.Fatalf("SaveSMSForIdentity(unknown IMSI) error=%v, want conflict", err)
	}
}

func TestMergeDeliveryIdentityUsesRuntimeIMSIKnownOnTheCard(t *testing.T) {
	seedMultiIMSICard(t)
	bound := SMSIdentity{ICCID: CanonicalICCID("ICC_MULTI"), IMSI: "IMSI_ROAM"}

	got, err := mergeDeliveryIdentity(bound, "IMSI_HOME")
	if err != nil || got.IMSI != "IMSI_HOME" || got.ICCID != bound.ICCID {
		t.Fatalf("mergeDeliveryIdentity(home)=%+v error=%v", got, err)
	}
	if _, err := mergeDeliveryIdentity(bound, "IMSI_NEVER"); !errors.Is(err, ErrSMSIdentityConflict) {
		t.Fatalf("mergeDeliveryIdentity(unknown) error=%v, want conflict", err)
	}
}

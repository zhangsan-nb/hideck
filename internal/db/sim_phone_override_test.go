package db

import "testing"

func TestNormalizeManualPhoneNumber(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "international", raw: "  +447700900123  ", want: "+447700900123"},
		{name: "digits only", raw: "100101", want: "100101"},
		{name: "clear", raw: "  ", want: ""},
		{name: "separator", raw: "+44 7700", wantErr: true},
		{name: "letters", raw: "138abc000", wantErr: true},
		{name: "too short", raw: "12345", wantErr: true},
		{name: "too long", raw: "+1234567890123456", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeManualPhoneNumber(tt.raw)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("NormalizeManualPhoneNumber(%q)=(%q, %v), want (%q, err=%v)", tt.raw, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestManualPhoneOverrideWinsAndClearRestoresAutomaticNumber(t *testing.T) {
	initPhoneNumberTestDB(t)
	const (
		imsi  = "234150000000070"
		iccid = "8944100000000000070"
	)
	if err := RecordVoWiFiPhoneNumber(imsi, iccid, "+447700900070"); err != nil {
		t.Fatal(err)
	}
	policy := DefaultCardPolicy(iccid)
	policy.LocalPhoneOverride = "+447911123456"
	if err := UpsertCardPolicy(policy); err != nil {
		t.Fatal(err)
	}
	identity := SMSIdentity{IMSI: imsi, ICCID: iccid}
	if got, err := GetPhoneNumberForSMSIdentity(identity); err != nil || got != "+447911123456" {
		t.Fatalf("manual number=%q err=%v", got, err)
	}
	policy.LocalPhoneOverride = ""
	if err := UpsertCardPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if got, err := GetPhoneNumberForSMSIdentity(identity); err != nil || got != "+447700900070" {
		t.Fatalf("automatic number after clear=%q err=%v", got, err)
	}
}

func TestManualPhoneOverrideDoesNotCrossReusedIMSI(t *testing.T) {
	initPhoneNumberTestDB(t)
	const (
		imsi     = "234150000000071"
		oldICCID = "8944100000000000071"
		newICCID = "8944100000000000072"
	)
	if err := UpsertSIMCard(oldICCID, imsi, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := UpsertSIMCard(newICCID, imsi, "", "", nil); err != nil {
		t.Fatal(err)
	}
	policy := DefaultCardPolicy(newICCID)
	policy.LocalPhoneOverride = "+447911000072"
	if err := UpsertCardPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if got, err := GetPhoneNumberForSMSIdentity(SMSIdentity{IMSI: imsi, ICCID: oldICCID}); err != nil || got != "" {
		t.Fatalf("old card inherited number %q err=%v", got, err)
	}
	index, err := GetSIMPhoneNumberIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := index.ByICCID[newICCID]; got != "+447911000072" {
		t.Fatalf("new card number=%q", got)
	}
	if got := index.ByICCID[oldICCID]; got != "" {
		t.Fatalf("old card index inherited number %q", got)
	}
}

func TestManualPhoneOverrideWorksWithIMSIOnlyBeforeAutomaticNumber(t *testing.T) {
	initPhoneNumberTestDB(t)
	const (
		imsi  = "234150000000073"
		iccid = "8944100000000000073"
	)
	if err := UpsertSIMCard(iccid, imsi, "", "", nil); err != nil {
		t.Fatal(err)
	}
	policy := DefaultCardPolicy(iccid)
	policy.LocalPhoneOverride = "+447911000073"
	if err := UpsertCardPolicy(policy); err != nil {
		t.Fatal(err)
	}
	got, err := GetPhoneNumberForSMSIdentity(SMSIdentity{IMSI: imsi})
	if err != nil || got != "+447911000073" {
		t.Fatalf("IMSI-only manual number=%q err=%v", got, err)
	}
}

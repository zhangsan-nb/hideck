package identity

import (
	"testing"

	internalprofile "github.com/iniwex5/vowifi-go/internal/vowifi/profile"
)

func TestPrepareStartKeepsICCID(t *testing.T) {
	access := &stubAccess{ident: Identity{
		IMSI: "310280233621715", IMPI: "310280233621715@private.att.net",
		IMPU:   []string{"sip:310280233621715@one.att.net"},
		Domain: "one.att.net",
	}}
	prepared, err := PrepareStart(PrepareStartInput{
		DeviceID: "wwan0",
		Profile:  Profile{ICCID: " 89012802331234567890 ", IMSI: "310280233621715", MCC: "310", MNC: "280"},
		Access:   access,
	})
	if err != nil {
		t.Fatalf("PrepareStart: %v", err)
	}
	if prepared.Profile.ICCID != "89012802331234567890" {
		t.Fatalf("prepared ICCID = %q", prepared.Profile.ICCID)
	}
}

func TestProfileFromInternalKeepsEveryField(t *testing.T) {
	input := internalprofile.Profile{
		ICCID: "89012802331234567890", IMSI: "310280233621715", MCC: "310", MNC: "280",
		IMEI: "123456789012345", UserAgent: "HiDeck/2.1", SMSC: "+12063130004", IMSDomain: "ims.example",
	}
	want := Profile{
		ICCID: input.ICCID, IMSI: input.IMSI, MCC: input.MCC, MNC: input.MNC,
		IMEI: input.IMEI, UserAgent: input.UserAgent, SMSC: input.SMSC, IMSDomain: input.IMSDomain,
	}
	if got := profileFromInternal(input); got != want {
		t.Fatalf("profile = %+v, want %+v", got, want)
	}
}

package device

import (
	"testing"

	"github.com/iniwex5/vowifi-go/runtimehost/identity"
)

func TestBuildVoWiFiRawProfileKeepsEveryField(t *testing.T) {
	input := identity.Profile{
		ICCID: " 8944100000000000001 ", IMSI: " 234150000000001 ",
		MCC: " 234 ", MNC: " 15 ", IMEI: " 123456789012345 ",
		UserAgent: " HiDeck/2.1 ", SMSC: " +447785016005 ", IMSDomain: " ims.mnc015.mcc234.3gppnetwork.org ",
	}
	want := identity.Profile{
		ICCID: "8944100000000000001", IMSI: "234150000000001",
		MCC: "234", MNC: "15", IMEI: "123456789012345",
		UserAgent: "HiDeck/2.1", SMSC: "+447785016005", IMSDomain: "ims.mnc015.mcc234.3gppnetwork.org",
	}
	if got := buildVoWiFiRawProfile(input); got != want {
		t.Fatalf("profile = %+v, want %+v", got, want)
	}
}

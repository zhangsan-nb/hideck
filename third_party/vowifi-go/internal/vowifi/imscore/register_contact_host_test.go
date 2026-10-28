package imscore

import (
	"strings"
	"testing"
)

func TestRedactContactUserDropsIdentities(t *testing.T) {
	got := redactContactUser(`<sip:515020000000001@[2001:db8::1]:5060;transport=tcp>;+sip.instance="<urn:gsma:imei:35000000-000000-0>"`)
	if !strings.Contains(got, "[2001:db8::1]:5060") || strings.Contains(got, "515020000000001") || strings.Contains(got, "imei") {
		t.Fatalf("redacted contact = %q", got)
	}
	for contact, want := range map[string]string{
		`<sip:515020000000001@[2001:db8::1]:5060;transport=tcp;+sip.instance="<urn:gsma:imei:35000000-000000-0>">`: "sip:redacted@[2001:db8::1]:5060;transport=tcp",
		`<sip:[2001:db8::1]:5060;transport=tcp;x-imsi=515020000000001>`:                                            "sip:[2001:db8::1]:5060;transport=tcp",
		`<sip:[2001:db8::1]:5060;transport=tcp?subject=515020000000001>`:                                           "sip:[2001:db8::1]:5060;transport=tcp",
	} {
		if got := redactContactUser(contact); got != want {
			t.Errorf("redactContactUser(%q) = %q, want %q", contact, got, want)
		}
	}
	if got := redactContactUser("<tel:+639170000000>"); got != "tel:redacted" {
		t.Errorf("tel contact = %q", got)
	}
}

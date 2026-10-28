package policy

import "testing"

func TestCarrierSwitchOverrides(t *testing.T) {
	for _, test := range []struct {
		name, mcc, mnc string
		value          *bool
		withhold, keep bool
	}{
		{"spark-default", "530", "05", nil, true, true},
		{"spark-disabled", "530", "05", boolValue(false), false, false},
		{"spark-enabled", "530", "05", boolValue(true), true, true},
		{"vodafone-default", "234", "15", nil, true, false},
		{"2degrees-default", "530", "24", nil, true, false},
		{"no-preset-default", "999", "99", nil, true, false},
		{"explicit-enable", "234", "15", boolValue(true), true, true},
		{"explicit-disable", "234", "15", boolValue(false), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveEffectiveCarrierConfigWithOverride(test.mcc, test.mnc, CarrierOverride{
				WithholdDeviceIdentity: test.value, KeepChildSAOnRekeyDecline: test.value,
			})
			if got.WithholdDeviceIdentity != test.withhold || got.KeepChildSAOnRekeyDecline != test.keep {
				t.Fatalf("withhold=%t keep=%t want withhold=%t keep=%t",
					got.WithholdDeviceIdentity, got.KeepChildSAOnRekeyDecline, test.withhold, test.keep)
			}
		})
	}
}

func TestCarrierSwitchOverridePointersAreDetached(t *testing.T) {
	ClearCarrierOverrides()
	t.Cleanup(ClearCarrierOverrides)
	value := false
	if err := SetCarrierOverrides(map[string]CarrierOverride{"53005": {WithholdDeviceIdentity: &value, KeepChildSAOnRekeyDecline: &value}}); err != nil {
		t.Fatal(err)
	}
	value = true
	got := ResolveEffectiveCarrierConfig("530", "05")
	if got.WithholdDeviceIdentity || got.KeepChildSAOnRekeyDecline {
		t.Fatal("caller mutation changed stored flags")
	}
	copy, _ := carrierOverrideByKey(plmnKey("530", "05"))
	*copy.WithholdDeviceIdentity, *copy.KeepChildSAOnRekeyDecline = true, true
	got = ResolveEffectiveCarrierConfig("530", "05")
	if got.WithholdDeviceIdentity || got.KeepChildSAOnRekeyDecline {
		t.Fatal("returned pointers alias store")
	}
}

func boolValue(value bool) *bool { return &value }

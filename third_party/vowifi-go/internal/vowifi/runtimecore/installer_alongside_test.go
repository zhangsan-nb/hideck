package runtimecore

import (
	"context"
	"reflect"
	"testing"

	"github.com/iniwex5/vowifi-go/engine/swu"
	"github.com/iniwex5/vowifi-go/internal/vowifi/imscore"
	"github.com/iniwex5/vowifi-go/internal/vowifi/ipsec3gpp"
	"github.com/iniwex5/vowifi-go/internal/vowifi/netstack"
)

type alongsideRecordingInstaller struct {
	next    int
	removed []int
}

func (installer *alongsideRecordingInstaller) install() (func() error, error) {
	installer.next++
	id := installer.next
	return func() error {
		installer.removed = append(installer.removed, id)
		return nil
	}, nil
}

func (installer *alongsideRecordingInstaller) InstallIPSec3GPP(context.Context, ipsec3gpp.Policy) (func() error, error) {
	return installer.install()
}

func (installer *alongsideRecordingInstaller) InstallIPSec3GPPAlongside(context.Context, ipsec3gpp.Policy) (func() error, error) {
	return installer.install()
}

func TestInstallerIMSNetworkReportsTwoSASetSupport(t *testing.T) {
	for name, test := range map[string]struct {
		installer imscore.IPSec3GPPInstaller
		want      bool
	}{
		"alongside installer": {installer: &alongsideRecordingInstaller{}, want: true},
		"userspace netstack":  {installer: resolveIPSec3GPPInstaller(swu.DataplaneModeUserspace, &netstack.Network{}), want: true},
		"system dataplane":    {installer: resolveIPSec3GPPInstaller("kernel", nil)},
		"no installer":        {},
	} {
		network := newInstallerIMSNetwork(nil, test.installer)
		got := network.(interface{ KeepsTwoIPSec3GPPSets() bool }).KeepsTwoIPSec3GPPSets()
		if got != test.want {
			t.Errorf("%s: KeepsTwoIPSec3GPPSets = %t, want %t", name, got, test.want)
		}
	}
}

func TestInstallerIMSNetworkKeepsReplacedSetUntilRetired(t *testing.T) {
	installer := &alongsideRecordingInstaller{}
	network := newInstallerIMSNetwork(nil, installer).(*installerIMSNetwork)
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err == nil {
		t.Fatal("added a set with nothing installed")
	}
	if err := network.InstallIPSec3GPP(ipsec3gpp.Policy{}); err != nil { // set 1
		t.Fatal(err)
	}
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err != nil { // set 2
		t.Fatal(err)
	}
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err == nil {
		t.Fatal("added a second pending set")
	}
	if err := network.AbortAddedIPSec3GPP(); err != nil || !reflect.DeepEqual(installer.removed, []int{2}) {
		t.Fatalf("abort removed %v err %v, want only the pending set", installer.removed, err)
	}
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err != nil { // set 3
		t.Fatal(err)
	}
	if err := network.CommitAddedIPSec3GPP(); err != nil || len(installer.removed) != 1 {
		t.Fatalf("commit removed %v err %v", installer.removed, err)
	}
	if err := network.RetirePreviousIPSec3GPP(); err != nil || !reflect.DeepEqual(installer.removed, []int{2, 1}) {
		t.Fatalf("retire removed %v err %v, want the replaced set", installer.removed, err)
	}
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err != nil { // set 4
		t.Fatal(err)
	}
	if err := network.RemoveIPSec3GPP(); err != nil || !reflect.DeepEqual(installer.removed, []int{2, 1, 4, 3}) {
		t.Fatalf("remove removed %v err %v, want pending then active", installer.removed, err)
	}
}

func TestInstallerIMSNetworkRejectsAlongsideWithoutSupport(t *testing.T) {
	network := newInstallerIMSNetwork(nil, &recordingIPSecInstaller{}).(*installerIMSNetwork)
	if err := network.InstallIPSec3GPP(ipsec3gpp.Policy{}); err != nil {
		t.Fatal(err)
	}
	if err := network.AddIPSec3GPP(ipsec3gpp.Policy{}); err == nil {
		t.Fatal("an installer without alongside support accepted a second set")
	}
}

// At most two sets coexist: adding while a replaced set still retires
// deletes that set first.
func TestInstallerIMSNetworkRetiresBeforeAddingAgain(t *testing.T) {
	installer := &alongsideRecordingInstaller{}
	network := newInstallerIMSNetwork(nil, installer).(*installerIMSNetwork)
	for _, step := range []func() error{
		func() error { return network.InstallIPSec3GPP(ipsec3gpp.Policy{}) }, // 1
		func() error { return network.AddIPSec3GPP(ipsec3gpp.Policy{}) },     // 2
		network.CommitAddedIPSec3GPP,
		func() error { return network.AddIPSec3GPP(ipsec3gpp.Policy{}) }, // 3, retires 1 first
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(installer.removed, []int{1}) {
		t.Fatalf("removed %v, want the retiring set before the next add", installer.removed)
	}
	if err := network.RemoveIPSec3GPP(); err != nil || !reflect.DeepEqual(installer.removed, []int{1, 3, 2}) {
		t.Fatalf("remove removed %v err %v", installer.removed, err)
	}
}

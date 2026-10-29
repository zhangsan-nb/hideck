package netstack

import (
	"context"
	"testing"

	"github.com/iniwex5/vowifi-go/internal/vowifi/ipsec3gpp"
)

func TestInstallIPSec3GPPAlongsideKeepsAndRestoresPreviousSet(t *testing.T) {
	network := newOriginalTestNetwork(t, newTestInnerEndpoint())
	defer network.Close()
	ctx := context.Background()
	if _, err := network.InstallIPSec3GPPAlongside(ctx, testIPSecPolicy()); err == nil {
		t.Fatal("alongside install without an installed set succeeded")
	}
	oldCleanup, err := network.InstallIPSec3GPP(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	old := network.bridge.currentTransformer().(*ipsec3gpp.Transport)

	abort, err := network.InstallIPSec3GPPAlongside(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	pending := network.bridge.currentTransformer().(*ipsec3gpp.Transport)
	if pending == old || pending.Previous() != old {
		t.Fatal("alongside set does not keep the installed set")
	}
	if err := abort(); err != nil || network.bridge.currentTransformer() != old {
		t.Fatalf("abort did not restore the previous set: %v", err)
	}

	newCleanup, err := network.InstallIPSec3GPPAlongside(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	replacement := network.bridge.currentTransformer().(*ipsec3gpp.Transport)
	if err := oldCleanup(); err != nil {
		t.Fatal(err)
	}
	if network.bridge.currentTransformer() != replacement || replacement.Previous() != nil {
		t.Fatal("retiring the previous set did not leave only the replacement")
	}
	if err := newCleanup(); err != nil || network.IPSec3GPPPolicyInstalled() {
		t.Fatalf("replacement cleanup left a set installed: %v", err)
	}
}

func TestIPSec3GPPCleanupUnlinksAnywhereInTheChain(t *testing.T) {
	network := newOriginalTestNetwork(t, newTestInnerEndpoint())
	defer network.Close()
	ctx := context.Background()
	oldest, err := network.InstallIPSec3GPP(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	middle, err := network.InstallIPSec3GPPAlongside(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	newest, err := network.InstallIPSec3GPPAlongside(ctx, testIPSecPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := oldest(); err != nil {
		t.Fatal(err)
	}
	if err := middle(); err != nil {
		t.Fatal(err)
	}
	if err := newest(); err != nil || network.IPSec3GPPPolicyInstalled() {
		t.Fatalf("a removed set survived in the chain: %v", err)
	}
}

package netstack

import (
	"context"
	"errors"

	"github.com/iniwex5/vowifi-go/internal/vowifi/ipsec3gpp"
)

func (n *Network) InstallIPSec3GPP(
	ctx context.Context,
	policy ipsec3gpp.Policy,
) (func() error, error) {
	return n.installIPSec3GPP(ctx, policy, false)
}

// InstallIPSec3GPPAlongside installs a replacement SA set while the current
// one keeps protecting its own flows (TS 33.203 7.4.1a). The current set is
// deleted by its own cleanup; this set's cleanup restores it if still present.
func (n *Network) InstallIPSec3GPPAlongside(
	ctx context.Context,
	policy ipsec3gpp.Policy,
) (func() error, error) {
	return n.installIPSec3GPP(ctx, policy, true)
}

func (n *Network) installIPSec3GPP(
	ctx context.Context,
	policy ipsec3gpp.Policy,
	alongside bool,
) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n == nil || n.bridge == nil {
		return nil, errors.New("netstack: packet bridge is not available")
	}
	transport, err := ipsec3gpp.NewTransport(policy)
	if err != nil {
		return nil, err
	}
	n.bridge.mu.Lock()
	if alongside {
		current, _ := n.bridge.transform.(*ipsec3gpp.Transport)
		if current == nil {
			n.bridge.mu.Unlock()
			return nil, errors.New("netstack: no installed SA set to keep alongside")
		}
		transport.SetPrevious(current)
	}
	n.bridge.rememberIPSecLocked()
	n.bridge.ipsecGeneration++
	n.bridge.transform = transport
	n.bridge.mu.Unlock()
	return func() error {
		// Remove only this installation: an earlier cleanup can race with a
		// replacement installed by the current IMS security association.
		n.bridge.mu.Lock()
		defer n.bridge.mu.Unlock()
		if n.bridge.transform == transport {
			n.bridge.rememberIPSecLocked()
			n.bridge.transform = nil
			if previous := transport.Previous(); previous != nil {
				n.bridge.ipsecGeneration++
				n.bridge.transform = previous
			}
			return nil
		}
		current, _ := n.bridge.transform.(*ipsec3gpp.Transport)
		for ; current != nil; current = current.Previous() {
			if current.Previous() == transport {
				current.SetPrevious(transport.Previous())
				break
			}
		}
		return nil
	}, nil
}

package device

import (
	"strings"
	"sync"
)

type managedDeviceMutation struct {
	mu   sync.Mutex
	refs int
}

// AcquireManagedDeviceMutation serializes API-level mutations for one logical
// device. Runtime teardown uses a separate policy lock, so callers may hold
// this lease while invoking RemoveWorker or AbandonDevice.
func (p *Pool) AcquireManagedDeviceMutation(deviceID string) func() {
	if p == nil || strings.TrimSpace(deviceID) == "" {
		return func() {}
	}
	deviceID = strings.TrimSpace(deviceID)
	p.managedMutationMu.Lock()
	if p.managedMutations == nil {
		p.managedMutations = make(map[string]*managedDeviceMutation)
	}
	mutation := p.managedMutations[deviceID]
	if mutation == nil {
		mutation = &managedDeviceMutation{}
		p.managedMutations[deviceID] = mutation
	}
	mutation.refs++
	p.managedMutationMu.Unlock()

	mutation.mu.Lock()
	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() {
			mutation.mu.Unlock()
			p.releaseManagedDeviceMutation(deviceID, mutation)
		})
	}
}

func (p *Pool) releaseManagedDeviceMutation(deviceID string, mutation *managedDeviceMutation) {
	p.managedMutationMu.Lock()
	defer p.managedMutationMu.Unlock()
	mutation.refs--
	if mutation.refs == 0 && p.managedMutations[deviceID] == mutation {
		delete(p.managedMutations, deviceID)
	}
}

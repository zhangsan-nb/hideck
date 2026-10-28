package device

import (
	"testing"
	"time"

	"github.com/yibaiba/hideck/internal/config"
)

func TestManagedDeviceMutationSerializesSameDevice(t *testing.T) {
	p := NewPool(&config.Config{})
	defer p.cancel()
	releaseFirst := p.AcquireManagedDeviceMutation("wwan0")
	acquired := make(chan struct{})
	released := make(chan struct{})
	go func() {
		releaseSecond := p.AcquireManagedDeviceMutation("wwan0")
		close(acquired)
		releaseSecond()
		close(released)
	}()

	select {
	case <-acquired:
		t.Fatal("second mutation acquired the same device before release")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second mutation did not continue after release")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("second mutation did not release its lock")
	}
	p.managedMutationMu.Lock()
	remaining := len(p.managedMutations)
	p.managedMutationMu.Unlock()
	if remaining != 0 {
		t.Fatalf("released mutation entries=%d, want 0", remaining)
	}
}

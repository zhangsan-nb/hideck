package vowifihost

import (
	"context"
	"testing"
	"time"
)

// blockingPrepareAdapter parks PrepareStart until its context ends, like a start
// waiting for the device's card policy lock.
type blockingPrepareAdapter struct {
	runtimeReadinessAdapter
	entered chan struct{}
	ended   chan error
}

func (a *blockingPrepareAdapter) PrepareStart(ctx context.Context, _, _, _ string) (PreparedStart, error) {
	close(a.entered)
	select {
	case <-ctx.Done():
		a.ended <- ctx.Err()
	case <-time.After(2 * time.Second):
		a.ended <- nil
	}
	return PreparedStart{}, context.Canceled
}

// Disable and a preempting SwitchBegin must cancel the context PrepareStart got:
// card policy projection and eSIM switching wait for the start to drain while
// they hold the lock PrepareStart is waiting for.
func TestStopCommandsCancelThePreparingStart(t *testing.T) {
	stops := map[string]func(*Manager, string) error{
		"disable": func(m *Manager, id string) error {
			return m.Disable(context.Background(), id, "policy_projection", false)
		},
		"switch_begin": func(m *Manager, id string) error {
			return m.SwitchBegin(context.Background(), id)
		},
	}
	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			manager := NewManager()
			adapter := &blockingPrepareAdapter{entered: make(chan struct{}), ended: make(chan error, 1)}
			manager.ConfigureAdapter(adapter)
			id := "dev-" + name
			go func() { _ = manager.Enable(context.Background(), id) }()
			select {
			case <-adapter.entered:
			case <-time.After(time.Second):
				t.Fatal("enable did not reach PrepareStart")
			}
			stopped := make(chan error, 1)
			go func() { stopped <- stop(manager, id) }()
			select {
			case err := <-adapter.ended:
				if err == nil {
					t.Fatal("PrepareStart context was not cancelled")
				}
			case <-time.After(time.Second):
				t.Fatal("PrepareStart context was not cancelled")
			}
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("stop command did not finish after the start gave up")
			}
		})
	}
}

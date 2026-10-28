package manager

import (
	"testing"
	"time"
)

func TestEventEmitterEmitAfterCloseDoesNotBlockOrPanic(t *testing.T) {
	e := NewEventEmitterWithQueueSize(1)
	e.Close()

	if ok := e.Emit(Event{Type: EventDisconnected}); ok {
		t.Fatal("Emit after Close returned true, want false")
	}
}

func TestEventEmitterDropsWhenQueueFull(t *testing.T) {
	e := &EventEmitter{
		queue: make(chan Event, 1),
		done:  make(chan struct{}),
	}

	if ok := e.Emit(Event{Type: EventConnected}); !ok {
		t.Fatal("first Emit returned false, want true")
	}
	if ok := e.Emit(Event{Type: EventDisconnected}); ok {
		t.Fatal("second Emit returned true with full queue, want false")
	}
	if dropped := e.Dropped(); dropped != 1 {
		t.Fatalf("Dropped=%d want 1", dropped)
	}
}

func TestEventEmitterSubscriptionCanBeRemoved(t *testing.T) {
	e := NewEventEmitterWithQueueSize(2)
	defer e.Close()
	called := make(chan struct{}, 2)
	unsubscribe := e.Subscribe(func(Event) { called <- struct{}{} })

	if !e.Emit(Event{Type: EventConnected}) {
		t.Fatal("first Emit failed")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("subscribed handler was not called")
	}

	unsubscribe()
	unsubscribe()
	e.mu.RLock()
	handlerCount := len(e.handlers)
	e.mu.RUnlock()
	if handlerCount != 0 {
		t.Fatalf("unsubscribed handler was retained: %d", handlerCount)
	}
	if !e.Emit(Event{Type: EventDisconnected}) {
		t.Fatal("second Emit failed")
	}
	select {
	case <-called:
		t.Fatal("unsubscribed handler was called")
	case <-time.After(20 * time.Millisecond):
	}
}

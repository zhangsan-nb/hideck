package swu

import (
	"context"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/engine/ikev2"
)

// A retransmitted IKE_SA_INIT response reaching a waiter that already holds
// one must be dropped, not block the dispatch loop (and with it
// stopIKEControlGeneration during a NAT-T float).
func TestDispatchDropsDuplicateResponseForFullWaiter(t *testing.T) {
	session := NewSession(&Config{})
	session.spiI = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	header := &ikev2.IKEHeader{
		SPIi:         ikeSPIUint64(session.spiI),
		SPIr:         9,
		ExchangeType: ikev2.IKE_SA_INIT,
		Flags:        ikeResponseFlag,
	}
	key := ikeWaitKey{exchangeType: ikev2.IKE_SA_INIT}
	waiter := make(chan []byte, 1)
	waiter <- []byte("first")
	session.controlMu.Lock()
	session.ikeWaiters[key] = waiter
	session.controlMu.Unlock()
	taskManager := NewTaskManager(context.Background(), "test", nil, 1, func([][]byte) error { return nil })
	t.Cleanup(taskManager.Stop)

	done := make(chan error, 1)
	go func() { done <- session.dispatchIKEResponse(header, []byte("duplicate"), taskManager) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch blocked on a full waiter")
	}
	if got := string(<-waiter); got != "first" {
		t.Fatalf("waiter holds %q want the first response", got)
	}
}

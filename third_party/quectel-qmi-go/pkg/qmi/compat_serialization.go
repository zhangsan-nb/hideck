package qmi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HIDECK_QMI_SERIAL_REQUESTS=1 opts this process into a compatibility mode for
// USB paths that cannot reliably carry concurrent QMI requests. It defaults off
// and covers all Clients for the same device path, with either transport. Other
// processes (including qmicli) are outside this gate and must not probe the same
// device concurrently when using this workaround.
const serialRequestsEnv = "HIDECK_QMI_SERIAL_REQUESTS"

type compatRequestGate struct {
	busy chan struct{}
	refs int // Includes the owner and all waiters; protected by compatRequestGates.
}

var compatRequestGates = struct {
	sync.Mutex
	byPath map[string]*compatRequestGate
}{byPath: make(map[string]*compatRequestGate)}

func (c *Client) acquireCompatRequestGate(ctx context.Context) (func(), error) {
	if os.Getenv(serialRequestsEnv) != "1" {
		return nil, nil
	}
	path := filepath.Clean(c.path)
	compatRequestGates.Lock()
	gate := compatRequestGates.byPath[path]
	if gate == nil {
		gate = &compatRequestGate{busy: make(chan struct{}, 1)}
		compatRequestGates.byPath[path] = gate
	}
	gate.refs++
	compatRequestGates.Unlock()

	dropRef := func() {
		compatRequestGates.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(compatRequestGates.byPath, path)
		}
		compatRequestGates.Unlock()
	}
	select {
	case gate.busy <- struct{}{}:
	case <-ctx.Done():
		dropRef()
		return nil, ctx.Err()
	case <-c.closeCh:
		dropRef()
		return nil, fmt.Errorf("connection closed")
	}
	release := func() {
		<-gate.busy
		dropRef()
	}
	// If cancellation or Close raced with availability, do not allocate a
	// transaction or enqueue a write merely because select chose the gate.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	select {
	case <-c.closeCh:
		release()
		return nil, fmt.Errorf("connection closed")
	default:
		return release, nil
	}
}

// drainCompatRequest owns both the transaction and device gate after the caller
// exits with an error after queuing a request. Keep it outside c.wg: the drain may
// call Close, which must be able to wait for the runtime loops without itself.
func (c *Client) drainCompatRequest(key uint32, entry *transactionEntry, wr writeRequest, writeFinished bool, release func(), cause error) {
	defer release()
	defer func() { c.completeTransaction(key, entry, cause) }()
	if !isContextDone(cause) {
		// closeCh signals shutdown intent before transport.Close completes.
		// Keep the gate until the socket and runtime loops have actually stopped.
		_ = c.Close()
		return
	}
	timeout := c.opts.DefaultRequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	if !writeFinished {
		select {
		case err := <-wr.result:
			if err != nil {
				_ = c.Close()
				return
			}
		case <-c.closeCh:
			_ = c.Close()
			return
		case <-timer.C:
			_ = c.Close()
			return
		}
	}

	select {
	case response, ok := <-entry.ch:
		if !ok || response == nil {
			_ = c.Close()
			return
		}
		cause = nil // The physical reply was consumed; it is no longer pending.
	case <-c.closeCh:
		_ = c.Close()
	case <-timer.C:
		// Closing the socket also prevents a blocked/queued write from being
		// sent later, after a different Client has acquired the device gate.
		_ = c.Close()
	}
}

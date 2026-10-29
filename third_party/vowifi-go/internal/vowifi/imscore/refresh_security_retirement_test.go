package imscore

import (
	"net"
	"testing"
	"time"
)

// A blocked close models a retirement preempted after it claims the old set.
// The next switch must not publish another retiring set until cleanup finishes.
type retirementBlockingConn struct {
	net.Conn
	entered chan struct{}
	resume  chan struct{}
}

func (conn retirementBlockingConn) Close() error {
	close(conn.entered)
	<-conn.resume
	return nil
}

func TestRetirementCompletesBeforeNextSwitch(t *testing.T) {
	network := &switchIPSecNetwork{}
	service := &Service{}
	conn := retirementBlockingConn{entered: make(chan struct{}), resume: make(chan struct{})}
	retirement := &saRetirement{
		switcher: network, oldConn: conn, committedAt: time.Now(),
		timer: time.AfterFunc(time.Hour, func() {}),
	}
	service.saRetirement.Store(retirement)
	retired := make(chan struct{})
	go func() {
		service.retireReplacedSecurity(retirement, "timer")
		close(retired)
	}()
	<-conn.entered
	nextSwitch := make(chan struct{})
	go func() {
		// This is the barrier used by switchRefreshSecurity before AddIPSec3GPP.
		service.retireReplacedSecurity(service.saRetirement.Load(), "next_switch")
		close(nextSwitch)
	}()
	select {
	case <-nextSwitch:
		t.Error("next SA switch passed retirement before the previous cleanup completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(conn.resume)
	<-retired
	<-nextSwitch
	if _, _, _, retires := network.counts(); retires != 1 {
		t.Fatalf("retired %d sets, want exactly one", retires)
	}
}

package api

import (
	"time"

	"github.com/yibaiba/hideck/internal/device"
)

func (s *Server) refreshManagedWorkerRuntime(expected *device.Worker, reason string) bool {
	if s == nil || s.pool == nil || expected == nil {
		return false
	}
	releaseMutation := s.pool.AcquireManagedDeviceMutation(expected.ID)
	defer releaseMutation()
	if s.pool.GetWorker(expected.ID) != expected {
		return false
	}
	_ = expected.RefreshRuntime(nil, reason)
	return true
}

func (s *Server) scheduleFlightModeRuntimeRefresh(worker *device.Worker, recoveringOnline bool) {
	go func() {
		s.refreshManagedWorkerRuntime(worker, "flight_mode_change")
		if !recoveringOnline {
			return
		}
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			s.refreshManagedWorkerRuntime(worker, "flight_mode_recover")
		case <-s.shutdownCh:
		}
	}()
}

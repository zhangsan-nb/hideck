package device

import "strings"

// ICCID alone cannot distinguish the same card returning after a switch.
type simOwnership struct {
	iccid      string
	generation uint64
}

func currentSIMOwnership(w *Worker) simOwnership {
	if w == nil {
		return simOwnership{}
	}
	w.cacheMu.RLock()
	defer w.cacheMu.RUnlock()
	return simOwnership{iccid: strings.TrimSpace(w.state.Identity.ICCID), generation: w.state.Identity.Generation}
}

// SIMIdentityGeneration identifies one concrete SIM ownership interval.
func (w *Worker) SIMIdentityGeneration() uint64 {
	return currentSIMOwnership(w).generation
}

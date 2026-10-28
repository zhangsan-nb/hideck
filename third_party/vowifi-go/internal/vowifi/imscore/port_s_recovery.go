package imscore

import (
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

const (
	rfc5626RecoveryBaseAllFailed = 30 * time.Second
	rfc5626RecoveryBaseFlowAlive = 90 * time.Second
	rfc5626RecoveryMaxDelay      = 30 * time.Minute
)

var errPortSNotReopenedAfterRegister = errors.New("imscore: port-s did not reopen after successful REGISTER")

type portSRecoveryBackoff struct {
	registrar string
	failures  uint32
	retryAt   time.Time
}

type portSRecoverySchedule struct {
	failures      uint32
	delay         time.Duration
	retryAt       time.Time
	retryAfter    time.Duration
	retryAfterSet bool
}

func (s *Service) recordPortSRecoveryFailure(err error, now time.Time) portSRecoverySchedule {
	registrar := s.currentPortSRecoveryRegistrar()
	retryAfter, retryAfterSet := registerRetryAfterFromError(err)
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	if s.portSBackoff.registrar != registrar {
		s.portSBackoff = portSRecoveryBackoff{registrar: registrar}
	}
	s.portSBackoff.failures++
	// The existing registration flow is still alive, so RFC 5626 section 4.5
	// uses the 90-second base. Retry-After may extend, but not shorten, it.
	upper := rfc5626RecoveryUpperBound(s.portSBackoff.failures, false)
	delay := s.jitterPortSRecoveryDelay(upper)
	if retryAfterSet && retryAfter > delay {
		delay = retryAfter
	}
	s.portSBackoff.retryAt = now.Add(delay)
	return portSRecoverySchedule{
		failures: s.portSBackoff.failures, delay: delay, retryAt: s.portSBackoff.retryAt,
		retryAfter: retryAfter, retryAfterSet: retryAfterSet,
	}
}

func (s *Service) portSRecoveryDeadline(now time.Time) (time.Time, bool) {
	registrar := s.currentPortSRecoveryRegistrar()
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	if s.portSBackoff.registrar != registrar {
		s.portSWatchGeneration++
		if s.portSWatchTimer != nil {
			s.portSWatchTimer.Stop()
			s.portSWatchTimer = nil
		}
		s.portSBackoff = portSRecoveryBackoff{}
		return time.Time{}, false
	}
	return s.portSBackoff.retryAt, s.portSBackoff.retryAt.After(now)
}

func (s *Service) resetPortSRecoveryBackoff() {
	if s == nil {
		return
	}
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	s.portSWatchGeneration++
	if s.portSWatchTimer != nil {
		s.portSWatchTimer.Stop()
		s.portSWatchTimer = nil
	}
	s.portSBackoff = portSRecoveryBackoff{}
}

// resetPortSRecoveryFailures restarts the backoff sequence but keeps retryAt,
// so a pending Retry-After deadline still holds until the next failure resets it.
func (s *Service) resetPortSRecoveryFailures() {
	if s == nil {
		return
	}
	s.portSWatchMu.Lock()
	s.portSBackoff.failures = 0
	s.portSWatchMu.Unlock()
}

func (s *Service) clearPortSRecoveryDeadline() {
	if s == nil {
		return
	}
	s.portSWatchMu.Lock()
	s.portSBackoff.retryAt = time.Time{}
	s.portSWatchMu.Unlock()
}

// generation is the port-s generation the caller saw with no connection open.
func (s *Service) backoffMissingPortSAfterRegister(generation uint64) {
	if s.capturePortSSession().generation != generation {
		// The peer reopened port-s after the caller's check; nothing failed.
		return
	}
	backoff := s.recordPortSRecoveryFailure(errPortSNotReopenedAfterRegister, time.Now())
	s.portSMissingAfterRegister.Store(true)
	// A reconnect between the two checks may already have reset the backoff;
	// drop the failure just recorded so it does not lengthen the next one.
	if s.capturePortSSession().generation != generation {
		s.portSMissingAfterRegister.Store(false)
		s.resetPortSRecoveryBackoff()
		return
	}
	if s.armPortSTimeoutFailover() {
		logging.WarnRate("ims-ports-validation-failover-"+s.DeviceID(), 30*time.Second,
			"IMS port-s REGISTER recovery did not restore downlink; schedule P-CSCF replacement after backoff",
			"device", s.DeviceID(), "pcscf", s.currentPortSRecoveryRegistrar(),
			"failures", backoff.failures, "retry_at", backoff.retryAt)
		// Readiness no longer reports this port-s as awaiting the peer.
		s.notifySMSReadiness()
	}
	s.schedulePortSReconnectWatchAt(backoff.retryAt)
	logging.WarnRate("ims-ports-recovery-no-flow-"+s.DeviceID(), 30*time.Second,
		"IMS port-s did not reopen after REGISTER; keep current binding and back off",
		"device", s.DeviceID(), "failures", backoff.failures, "retry_in", backoff.delay)
}

func (s *Service) currentPortSRecoveryRegistrar() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.registrar)
}

func (s *Service) jitterPortSRecoveryDelay(upper time.Duration) time.Duration {
	jitter := randomRFC5626RecoveryDelay
	if s != nil && s.portSRecoveryJitter != nil {
		jitter = s.portSRecoveryJitter
	}
	return clampRFC5626RecoveryDelay(jitter(upper), upper)
}

func rfc5626RecoveryUpperBound(failures uint32, allFlowsFailed bool) time.Duration {
	base := rfc5626RecoveryBaseFlowAlive
	if allFlowsFailed {
		base = rfc5626RecoveryBaseAllFailed
	}
	upper := base
	for attempt := uint32(0); attempt < failures; attempt++ {
		if upper >= rfc5626RecoveryMaxDelay/2 {
			return rfc5626RecoveryMaxDelay
		}
		upper *= 2
	}
	return upper
}

func randomRFC5626RecoveryDelay(upper time.Duration) time.Duration {
	lower := upper / 2
	span := upper - lower
	if span <= 0 {
		return upper
	}
	return lower + time.Duration(rand.Int64N(int64(span)+1))
}

func clampRFC5626RecoveryDelay(delay, upper time.Duration) time.Duration {
	lower := upper / 2
	if delay < lower {
		return lower
	}
	if delay > upper {
		return upper
	}
	return delay
}

func registerRetryAfterFromError(err error) (time.Duration, bool) {
	var responseErr *registerResponseError
	if !errors.As(err, &responseErr) {
		return 0, false
	}
	return responseErr.retryAfter, responseErr.retryAfterSet
}

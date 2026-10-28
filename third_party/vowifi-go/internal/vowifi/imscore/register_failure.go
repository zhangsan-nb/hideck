package imscore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	enginesim "github.com/iniwex5/vowifi-go/engine/sim"
	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
	"github.com/iniwex5/vowifi-go/internal/vowifi/policy"
)

type registerFailureOutcome struct {
	reason       string
	kind         int32
	nextRegister time.Time
	retryDelay   time.Duration
}

func decideRegisterFailureOutcome(
	now time.Time,
	result registerAttemptResult,
	registerPolicy policy.IMSRegisterPolicy,
	transportFailure bool,
) registerFailureOutcome {
	delay := temporaryRegisterRetryInterval(registerPolicy)
	outcome := registerFailureOutcome{
		reason: "network", kind: registrationRejectedTemporary,
		nextRegister: now.Add(delay), retryDelay: delay,
	}
	if result.retryAfterSet {
		outcome.reason = "retry_after"
		outcome.retryDelay = result.retryAfter
		outcome.nextRegister = now.Add(result.retryAfter)
		return outcome
	}
	if result.statusCode == 423 && result.minExpires > 0 {
		outcome.reason = "min_expires"
		return outcome
	}
	if result.statusCode == 305 {
		outcome.reason = "use_proxy"
		return outcome
	}
	if transportFailure {
		outcome.retryDelay = 5 * time.Second
		outcome.nextRegister = now.Add(outcome.retryDelay)
		return outcome
	}
	if isTemporaryRegisterSIPResponse(registerPolicy, result.statusCode) {
		outcome.reason = "temporary"
		return outcome
	}
	if isForbiddenRegisterSIPResponse(registerPolicy, result.statusCode) {
		return permanentRegisterFailure(now, "forbidden", 5*time.Minute)
	}
	if result.statusCode >= 400 {
		return permanentRegisterFailure(now, "permanent", 10*time.Minute)
	}
	return outcome
}

// errProtectedFlowNeedsFreshPorts abandons an attempt whose SA was re-issued on
// client ports that are still draining, so the next attempt can offer new ones.
var errProtectedFlowNeedsFreshPorts = errors.New(
	"imscore: protected IPsec SA re-issued on client ports still in use",
)

// registerAttemptTransportFailure reports whether the attempt failed below the
// SIP response level, leaving the registrar's own verdict unknown. RFC 3261
// 17.1.2 makes a timed-out or transport-failed client transaction a transport
// failure, which retries, rather than a rejection.
func registerAttemptTransportFailure(err error) bool {
	return errors.Is(err, sip.ErrTransactionTimeout) ||
		errors.Is(err, sip.ErrTransactionTransport) ||
		errors.Is(err, errProtectedFlowNeedsFreshPorts)
}

func permanentRegisterFailure(now time.Time, reason string, delay time.Duration) registerFailureOutcome {
	return registerFailureOutcome{
		reason: reason, kind: registrationRejectedPermanent,
		nextRegister: now.Add(delay), retryDelay: delay,
	}
}

func nextRegisterAtAfterSuccess(now time.Time, delay time.Duration) time.Time {
	return now.Add(delay)
}

func (s *Service) handleRegisterAPDUBusy(err error) error {
	if s == nil || !errors.Is(err, enginesim.ErrAPDUBusy) {
		return err
	}
	s.mu.Lock()
	s.nextRegister = time.Now().Add(3 * time.Second)
	s.lastError = err.Error()
	s.lastRegisterErr = err.Error()
	s.mu.Unlock()
	s.reRegisterPending.Store(false)
	s.transitionRegStatus(registrationRejectedTemporary)
	return err
}

func (s *Service) triggerRegisterReconnect() {
	if s == nil || s.OnReconnectNeeded == nil || !s.reconnectTriggering.CompareAndSwap(false, true) {
		return
	}
	callback := s.OnReconnectNeeded
	go func() {
		defer s.reconnectTriggering.Store(false)
		callback()
	}()
}

type registerRetryPolicy struct {
	maxAuthChallenges             int
	defaultInitialRetryStatusCode map[int]struct{}
}

func defaultRegisterRetryPolicy(registerPolicy policy.IMSRegisterPolicy) registerRetryPolicy {
	normalized := normalizedRegisterPolicy(registerPolicy)
	statusCodes := make(map[int]struct{}, len(normalized.InitialRejectFallbackStatusCodes))
	for _, statusCode := range normalized.InitialRejectFallbackStatusCodes {
		statusCodes[statusCode] = struct{}{}
	}
	return registerRetryPolicy{maxAuthChallenges: 3, defaultInitialRetryStatusCode: statusCodes}
}

func (p registerRetryPolicy) ShouldRetryDefaultInitial(statusCode int, warning, body string) bool {
	if strings.TrimSpace(warning) != "" || strings.TrimSpace(body) != "" {
		return false
	}
	_, retry := p.defaultInitialRetryStatusCode[statusCode]
	return retry
}

// isProtectedPortConflict matches a re-REGISTER rejected because its protected
// client port is already in the P-CSCF SA table. TS 33.203 7.1 rule 3 makes the
// P-CSCF check this and rule 6 lets the UE retry with new ports. Globe PH
// rejects every refresh that offers the current SA ports this way.
func isProtectedPortConflict(err error) bool {
	var responseErr *registerResponseError
	return errors.As(err, &responseErr) && responseErr.statusCode == 500 &&
		strings.Contains(strings.ToLower(responseErr.message), "ip+port conflict")
}

// registerWithFreshProtectedPorts drops the current SA and transport and runs
// an initial registration, which reserves and offers new protected ports.
// Called with registerMu held. The registration starts over in the configured
// transport order and has only what is left of the refresh deadline.
func (s *Service) registerWithFreshProtectedPorts(ctx context.Context, conflict error) (time.Duration, error) {
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline).Round(time.Millisecond)
	}
	logging.Info("IMS re-REGISTER rejected for a protected port conflict; registering with new ports",
		"device", s.DeviceID(), "pcscf", s.currentPortSRecoveryRegistrar(),
		"previous_transport", s.currentRegistrationTransport(),
		"transport_order", registerTransportCandidates(s.cfg.Transport), "budget", budget, "err", conflict)
	if err := s.resetRegistrationForPCSCFSwitch(); err != nil {
		logging.Info("IMS reset after protected port conflict failed",
			"device", s.DeviceID(), "pcscf", s.currentPortSRecoveryRegistrar(), "err", err)
		return 0, fmt.Errorf("imscore: reset registration after protected port conflict: %w", err)
	}
	started := time.Now()
	expires, err := s.runRegisterFlow(ctx)
	if err != nil {
		logging.Info("IMS registration with new protected ports failed",
			"device", s.DeviceID(), "pcscf", s.currentPortSRecoveryRegistrar(),
			"last_transport", s.currentRegistrationTransport(), "elapsed", time.Since(started).Round(time.Millisecond),
			"ctx_err", ctx.Err(), "err", err)
		return expires, err
	}
	logging.Info("IMS registered with new protected ports",
		"device", s.DeviceID(), "pcscf", s.currentPortSRecoveryRegistrar(),
		"transport", s.currentRegistrationTransport(), "elapsed", time.Since(started).Round(time.Millisecond))
	return expires, nil
}

package imscore

import (
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

const (
	smsReadyReasonReady             = "IMS SMS receiver ready"
	smsReadyReasonNotRegistered     = "IMS registration is not ready"
	smsReadyReasonProfileNotReady   = "IMS registered identity is not ready"
	smsReadyReasonTransportNotReady = "IMS registered signaling transport is not ready"
	smsReadyReasonReceiverNotReady  = "IMS SMS receiver is not ready"
	smsReadyReasonSMSCNotConfigured = "IMS SMSC is not configured"
	smsReadyReasonAwaitingDownlink  = "IMS registered; SMS reception awaiting downlink verification"
	smsReadyReasonRegistrationFlow  = "IMS SMS receiver verified on registered TCP flow"
	smsReadyReasonPortSAwaitingPeer = "IMS registered; port-s closed or timed out, awaiting the P-CSCF reconnect"
	smsReadyReasonAwaitingPeerPortS = "IMS SMS receiver ready; port-s timed out, P-CSCF reconnects on demand"
	smsReadyReasonProbingPortS      = "IMS SMS receiver ready; port-s timed out, REGISTER probing the P-CSCF"
)

type smsHealthInput struct {
	readiness             SMSReadiness
	baseReceiverReady     bool
	protectedPushRequired bool
	portS                 portSSessionSnapshot
}

// SMSReadiness returns a consistent snapshot of the SMS prerequisites.
func (s *Service) SMSReadiness() SMSReadiness {
	if s == nil {
		return evaluateSMSReadiness(false, false, false, false, "")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	registered := s.regState == regRegistered
	protectedPushRequired := s.protectedSMSPushRequiredLocked()
	portS := s.capturePortSSession()
	// A periodic REGISTER in flight keeps the binding it refreshes.
	probeGrace := protectedPushRequired && s.timeoutProbeGraceLocked(portS) &&
		(registered || s.bindingLiveDuringRegisterLocked(time.Now()))
	if probeGrace {
		registered = true
	}
	profileReady := registered && s.regSession != nil &&
		strings.TrimSpace(s.regSession.publicID) != "" &&
		strings.TrimSpace(s.regSession.contactUser) != ""
	transportReady := registered && s.registeredSIPTransportReadyLocked()
	baseReceiverReady := s.smsReceiverReady
	receiverReady := baseReceiverReady
	registrationSMSProven := s.registrationSMSProvenLocked()
	awaitingEvidence := !s.smsReception.failed && s.smsReception.peer == nil
	if protectedPushRequired && !probeGrace {
		receiverReady = receiverReady && (s.portSPushReady.Load() || registrationSMSProven)
	}
	smsc := ""
	if s.cfg != nil {
		smsc = s.cfg.SMSC
	}
	readiness := evaluateSMSReadiness(registered, profileReady, transportReady, receiverReady, smsc)
	readiness.HealthReady = readiness.Ready || smsReceiverHealthReady(smsHealthInput{
		readiness: readiness, baseReceiverReady: baseReceiverReady,
		protectedPushRequired: protectedPushRequired, portS: portS,
	})
	readiness.ReceiverPending = !readiness.Ready && !readiness.HealthReady &&
		registered && profileReady && transportReady && baseReceiverReady &&
		protectedPushRequired && awaitingEvidence && portS.generation == 0 && portS.lastCloseKind == ""
	if readiness.ReceiverPending && readiness.SMSCPresent {
		readiness.Reason = smsReadyReasonAwaitingDownlink
	}
	if readiness.Ready && registrationSMSProven {
		readiness.Reason = smsReadyReasonRegistrationFlow
	} else if readiness.Ready && !s.portSPushReady.Load() && s.awaitingPeerAfterTimeout() {
		readiness.Reason = smsReadyReasonAwaitingPeerPortS
	} else if readiness.Ready && probeGrace {
		readiness.Reason = smsReadyReasonProbingPortS
	}
	if !readiness.Ready && readiness.SMSCPresent && !s.smsReception.reportFailed &&
		strings.EqualFold(portS.closedRegistrar, strings.TrimSpace(s.registrar)) && portSAwaitingPeer(smsHealthInput{
		readiness: readiness, baseReceiverReady: baseReceiverReady,
		protectedPushRequired: protectedPushRequired, portS: portS,
	}) {
		readiness.Reason = smsReadyReasonPortSAwaitingPeer
	}
	return readiness
}

// portSAwaitingPeer reports a registration whose only missing piece is a
// port-s the P-CSCF closed (EOF) or that timed out. TS 33.203 7.1 has the
// P-CSCF reopen it before its next request, so this only changes the reason;
// readiness and health stay as they are.
func portSAwaitingPeer(input smsHealthInput) bool {
	kind := input.portS.lastCloseKind
	return input.readiness.Registered && input.readiness.ProfileReady &&
		input.readiness.TransportReady && input.baseReceiverReady &&
		input.protectedPushRequired && !input.portS.connected && input.portS.generation > 0 &&
		(kind == portSCloseEOF || kind == portSCloseTimeout) &&
		!input.portS.closedAt.Before(input.portS.openedAt) && !input.portS.timeoutValidationFailed
}

// bindingLiveDuringRegisterLocked reports a REGISTER in flight on a binding
// that has not expired yet. The caller holds mu.
func (s *Service) bindingLiveDuringRegisterLocked(now time.Time) bool {
	return (s.regState == regRegistering || s.regState == regReregister) &&
		s.regSession != nil && s.regSession.expires > 0 && !s.lastRegisterOKAt.IsZero() &&
		now.Before(s.lastRegisterOKAt.Add(s.regSession.expires))
}

func smsReceiverHealthReady(input smsHealthInput) bool {
	return input.readiness.Registered && input.readiness.ProfileReady &&
		input.readiness.TransportReady && input.baseReceiverReady &&
		input.protectedPushRequired && !input.portS.connected &&
		input.portS.lastCloseKind == portSCloseEOF &&
		!input.portS.closedAt.Before(input.portS.openedAt)
}

func (s *Service) protectedSMSPushRequiredLocked() bool {
	return !s.externalTransport && !s.canAwaitOnDemandPortS() && !s.portSTimeoutDownlinkProven() &&
		!s.awaitingPeerAfterTimeout() &&
		s.regSession != nil &&
		s.regSession.security != nil &&
		strings.TrimSpace(s.regSession.security.verifyHeader) != ""
}

// SetOnSMSReadinessChanged installs the readiness observer and immediately
// publishes a snapshot so callers cannot miss startup transitions.
func (s *Service) SetOnSMSReadinessChanged(fn func(SMSReadiness)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onSMSReadiness = fn
	s.mu.Unlock()
	if fn != nil {
		fn(s.SMSReadiness())
	}
}

// SetOnSMSReady restores the original one-shot SMS readiness callback.
func (s *Service) SetOnSMSReady(fn func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onSMSReady = fn
	s.mu.Unlock()
	s.maybeNotifySMSReady(smsReadyReasonReady)
}

func (s *Service) setSMSReceiverReady(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	changed := s.smsReceiverReady != ready
	s.smsReceiverReady = ready
	callback := s.onSMSReadiness
	s.mu.Unlock()
	if ready {
		s.maybeNotifySMSReady(smsReadyReasonReady)
	}
	if !changed {
		return
	}
	readiness := s.SMSReadiness()
	s.logSMSReadinessChange(readiness)
	if callback != nil {
		callback(readiness)
	}
}

func (s *Service) notifySMSReadiness() {
	if s == nil {
		return
	}
	s.mu.RLock()
	callback := s.onSMSReadiness
	s.mu.RUnlock()
	s.maybeNotifySMSReady(smsReadyReasonReady)
	readiness := s.SMSReadiness()
	s.logSMSReadinessChange(readiness)
	if callback != nil {
		callback(readiness)
	}
}

// logSMSReadinessChange logs each new readiness reason. The UI shows SMS RX
// state from these snapshots, and the not-ready transitions were otherwise
// visible only there.
func (s *Service) logSMSReadinessChange(readiness SMSReadiness) {
	if previous, _ := s.smsReadinessReason.Swap(readiness.Reason).(string); previous == readiness.Reason {
		return
	}
	logging.Info("IMS SMS readiness changed",
		"device", s.DeviceID(), "reason", readiness.Reason,
		"ready", readiness.Ready, "health_ready", readiness.HealthReady,
		"receiver_pending", readiness.ReceiverPending, "port_s_connected", s.portSPushReady.Load())
}

func evaluateSMSReadiness(registered, profileReady, transportReady, receiverReady bool, smsc string) SMSReadiness {
	readiness := SMSReadiness{
		Registered:     registered,
		ProfileReady:   profileReady,
		TransportReady: transportReady,
		ReceiverReady:  receiverReady,
		SMSCPresent:    strings.TrimSpace(smsc) != "",
	}
	readiness.MOReady = readiness.Registered && readiness.ProfileReady &&
		readiness.TransportReady && readiness.SMSCPresent
	switch {
	case !readiness.Registered:
		readiness.Reason = smsReadyReasonNotRegistered
	case !readiness.ProfileReady:
		readiness.Reason = smsReadyReasonProfileNotReady
	case !readiness.TransportReady:
		readiness.Reason = smsReadyReasonTransportNotReady
	case !readiness.SMSCPresent && !readiness.ReceiverReady:
		readiness.Reason = smsReadyReasonSMSCNotConfigured
	case !readiness.ReceiverReady:
		readiness.Reason = smsReadyReasonReceiverNotReady
	default:
		readiness.Ready = true
		readiness.HealthReady = true
		if !readiness.SMSCPresent {
			readiness.Reason = smsReadyReasonSMSCNotConfigured
			break
		}
		readiness.MOReady = true
		readiness.Reason = smsReadyReasonReady
	}
	return readiness
}

package imscore

import (
	"net"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

const portSTransportTimeoutFailure = "port_s_transport_timeout"

// Protected by portSSessionMu. A timeout or first peer reset never requests a
// proxy switch by itself: REGISTER must succeed and downlink validation fail.
type portSTimeoutRecoveryState struct {
	registrar        string
	generation       uint64
	observedAt       time.Time
	failure          string
	validationFailed bool
	downlinkProven   bool
}

func (s *Service) recordPortSTimeoutLocked(registrar, kind string) {
	s.portSSession.timeoutRecovery = portSTimeoutRecoveryState{}
	failure := portSValidationFailure(kind)
	if failure != "" && usesVodafoneUKPortSResetRecovery(s.cfg) {
		s.portSSession.timeoutRecovery = portSTimeoutRecoveryState{
			registrar: registrar, generation: s.portSSession.generation,
			observedAt: s.portSSession.closedAt, failure: failure,
		}
	}
}

func portSValidationFailure(kind string) string {
	switch kind {
	case portSCloseTimeout:
		return portSTransportTimeoutFailure
	case portSClosePeerReset:
		return portSPeerResetFailure
	default:
		return ""
	}
}

func (s *Service) armPortSTimeoutFailover() bool {
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	state := &s.portSSession.timeoutRecovery
	if state.registrar == "" || state.downlinkProven || s.portSPushReady.Load() {
		return false
	}
	state.validationFailed = true
	return true
}

func (s *Service) pendingPortSTimeoutFailover() portSTimeoutRecoveryState {
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	state := s.portSSession.timeoutRecovery
	if !state.validationFailed || state.downlinkProven {
		return portSTimeoutRecoveryState{}
	}
	return state
}

// Called by the recovery watchdog with registerMu held, after its retry deadline.
func (s *Service) recoverTimedOutPortSLocked() bool {
	state := s.pendingPortSTimeoutFailover()
	if state.registrar == "" {
		return false
	}
	if !s.pcscfRecoveryPending.CompareAndSwap(false, true) {
		return true
	}
	defer s.finishPCSCFRecovery()
	failure := state.failure
	if failure == "" {
		failure = portSTransportTimeoutFailure
	}
	s.recoverPCSCFAfterPortSFailureLocked(state.registrar, portSFailoverCause{
		reason: failure, observedAt: state.observedAt,
		generation: state.generation,
	})
	return true
}

// Called with mu held immediately before committing the proxy switch.
func (s *Service) consumePortSTimeoutFailoverLocked(registrar string, generation uint64, failure string) bool {
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	state := s.portSSession.timeoutRecovery
	if state.registrar != registrar || state.generation != generation ||
		state.failure != failure ||
		!state.validationFailed || state.downlinkProven || s.udpDownlinkProven.Load() ||
		s.portSSession.generation != generation || portSValidationFailure(s.portSSession.lastCloseKind) != failure {
		return false
	}
	s.portSSession.timeoutRecovery = portSTimeoutRecoveryState{}
	return true
}

func (s *Service) clearPortSTimeoutRecovery() portSTimeoutRecoveryState {
	s.portSSessionMu.Lock()
	previous := s.portSSession.timeoutRecovery
	s.portSSession.timeoutRecovery = portSTimeoutRecoveryState{}
	s.portSSessionMu.Unlock()
	return previous
}

func (s *Service) portSTimeoutDownlinkProven() bool {
	if s.udpDownlinkProven.Load() {
		return true
	}
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	return s.portSSession.timeoutRecovery.downlinkProven
}

// A successfully handled request on the current protected connection can also
// prove reachability. Late traffic from detached connections must not cancel
// recovery of their replacement. REGISTER responses never enter this path.
// The caller holds mu across both request accounting and this state transition.
func (s *Service) confirmPortSTimeoutDownlinkLocked(peer net.Conn) bool {
	if peer == nil {
		return false
	}
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	state := &s.portSSession.timeoutRecovery
	connection, tracked := s.portSSession.connections[peer]
	current := (peer == s.registrationTCP && s.registrationTCPProtected) ||
		(tracked && !connection.localClosing && connection.registrar == s.registrar)
	if datagram, ok := peer.(*protectedUDPPeer); ok {
		current = s.currentProtectedUDPPeerLocked(datagram)
	}
	proven := state.registrar != "" && !state.downlinkProven &&
		state.registrar == strings.TrimSpace(s.registrar) && current && !s.stopped()
	if proven {
		state.downlinkProven = true
		state.validationFailed = false
		s.portSRecoveryAwaitingFlow.Store(false)
		s.portSTimeoutAwaitEntered.Store(false)
		s.portSReconnectWaiting.Store(false)
		s.resetPortSRecoveryBackoff()
	}
	return proven
}

// Observers may reenter Service, so publish only after releasing its locks.
func (s *Service) notifyPortSTimeoutDownlink() {
	s.notifySMSReadiness()
	logging.Info("IMS port-s recovery validated by current inbound SIP request", "device", s.DeviceID())
}

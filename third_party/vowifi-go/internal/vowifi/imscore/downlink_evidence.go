package imscore

import (
	"net"
	"strings"
	"time"
)

// Transport generations, unlike REGISTER CSeq, survive periodic refreshes and
// change when a path is replaced. All evidence is protected by Service.mu.
type downlinkCheckpoint struct {
	registrar  string
	generation uint64
	requests   uint64
}

func (s *Service) downlinkCheckpointLocked() downlinkCheckpoint {
	return downlinkCheckpoint{
		registrar:  strings.TrimSpace(s.registrar),
		generation: s.downlinkGeneration, requests: s.downlinkRequests,
	}
}

func (s *Service) captureDownlinkCheckpoint() downlinkCheckpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downlinkCheckpointLocked()
}

func (checkpoint downlinkCheckpoint) samePath(other downlinkCheckpoint) bool {
	return checkpoint.registrar == other.registrar && checkpoint.generation == other.generation
}

func (s *Service) currentDownlinkPeerLocked(peer net.Conn) bool {
	if datagram, ok := peer.(*protectedUDPPeer); ok {
		return s.currentProtectedUDPPeerLocked(datagram)
	}
	if peer == nil {
		return s.externalTransport || s.registrationIO != nil
	}
	if peer == s.registrationTCP {
		return s.registrationTCPProtected
	}
	s.protectedConnMu.Lock()
	defer s.protectedConnMu.Unlock()
	if _, live := s.protectedConns[peer]; !live {
		return false
	}
	s.portSSessionMu.Lock()
	defer s.portSSessionMu.Unlock()
	connection, recorded := s.portSSession.connections[peer]
	return !recorded || (!connection.localClosing && connection.registrar == s.registrar)
}

// Check both before and after dispatch: a handler may finish after its source
// connection has been retired, including a replacement using the same P-CSCF.
func (s *Service) captureInboundDownlink(peer net.Conn) (downlinkCheckpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downlinkCheckpointLocked(), s.currentDownlinkPeerLocked(peer)
}

func (s *Service) recordCurrentDownlinkRequest(peer net.Conn, checkpoint downlinkCheckpoint) {
	s.mu.Lock()
	current := !s.stopped() && checkpoint.samePath(s.downlinkCheckpointLocked()) && s.currentDownlinkPeerLocked(peer)
	timeoutProven := false
	if current {
		s.downlinkRequests++
		if _, datagram := peer.(*protectedUDPPeer); datagram {
			s.udpDownlinkProven.Store(true)
			s.portSRecoveryAwaitingFlow.Store(false)
			s.portSTimeoutAwaitEntered.Store(false)
			s.portSReconnectWaiting.Store(false)
			s.resetPortSRecoveryBackoff()
			s.recordPortSInbound(time.Now())
		}
		// Failover commits under mu too: it must not observe the request before
		// the same request has canceled its pending timeout recovery.
		timeoutProven = s.confirmPortSTimeoutDownlinkLocked(peer)
		// A current downlink also proves that a recovered TCP path is usable.
		// Clear the earlier reset incident only after timeout recovery has been
		// canceled so both recovery state transitions remain atomic to failover.
		s.clearPortSResetRecovery(checkpoint.registrar)
	}
	s.mu.Unlock()
	if !current {
		return
	}
	if timeoutProven {
		s.notifyPortSTimeoutDownlink()
	}
	s.confirmCurrentRegistrarDownlinkHealthy()
	s.signalDownlinkValidation()
	if _, datagram := peer.(*protectedUDPPeer); datagram {
		s.notifySMSReadiness()
	}
}

func (s *Service) downlinkEvidenceSinceLocked(baseline downlinkCheckpoint) string {
	s.protectedConnMu.Lock()
	hasPush := false
	for conn := range s.protectedConns {
		s.portSSessionMu.Lock()
		state, recorded := s.portSSession.connections[conn]
		live := !recorded || (!state.localClosing && state.registrar == s.registrar)
		s.portSSessionMu.Unlock()
		if live {
			hasPush = true
			break
		}
	}
	s.protectedConnMu.Unlock()
	if hasPush {
		return "port-s"
	}
	current := s.downlinkCheckpointLocked()
	if current.requests > baseline.requests || (!baseline.samePath(current) && current.requests > 0) {
		return "inbound_sip_request"
	}
	return ""
}

func (s *Service) confirmCurrentRegistrarDownlinkHealthy() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.regState != regRegistered || s.stopped() ||
		s.registrarRecoveryAttempt.registrar != strings.TrimSpace(s.registrar) ||
		s.downlinkEvidenceSinceLocked(s.registerDownlinkBaseline) == "" {
		return
	}
	// The store also checks the incident generation, so an old binding cannot
	// clear a rejection observed during its REGISTER or downlink handler.
	s.registrarPenalties.clearFailures(s.registrarRecoveryAttempt)
	s.cancelReplacementDownlinkWatchLocked()
}

package imscore

import "net"

// Evidence belongs to a concrete protected TCP connection, not an operator.
// A failure epoch prevents an in-flight old message from undoing a later fault.
type smsReceptionEvidence struct {
	peer              net.Conn
	path              downlinkCheckpoint
	failureGeneration uint64
	failed            bool
	// An RP report over the registration flow failed, so a closed port-s is
	// not just awaiting the P-CSCF. Cleared by the next proof or port-s open.
	reportFailed bool
}

type smsReceptionCheckpoint struct {
	path              downlinkCheckpoint
	failureGeneration uint64
}

func (s *Service) captureSMSReceptionCheckpoint() smsReceptionCheckpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return smsReceptionCheckpoint{s.downlinkCheckpointLocked(), s.smsReception.failureGeneration}
}

func (s *Service) currentSMSReceptionLocked(peer net.Conn, checkpoint smsReceptionCheckpoint) bool {
	return peer != nil && peer == s.registrationTCP && s.registrationTCPProtected &&
		checkpoint.path.samePath(s.downlinkCheckpointLocked()) &&
		checkpoint.failureGeneration == s.smsReception.failureGeneration && !s.stopped()
}

func (s *Service) recordRegistrationSMS(peer net.Conn, checkpoint smsReceptionCheckpoint) {
	s.mu.Lock()
	current := s.currentSMSReceptionLocked(peer, checkpoint)
	if current {
		s.smsReception.peer = peer
		s.smsReception.path = checkpoint.path
		s.smsReception.failed = false
		s.smsReception.reportFailed = false
	}
	s.mu.Unlock()
	if current {
		s.notifySMSReadiness()
	}
}

func (s *Service) invalidateSMSReceptionLocked() {
	s.smsReception.peer = nil
	s.smsReception.failed = true
	s.smsReception.failureGeneration++
}

func (s *Service) recordRegistrationSMSReportFailure(peer net.Conn, checkpoint smsReceptionCheckpoint) {
	s.mu.Lock()
	// A port-s close during the report retries moves the failure epoch but
	// does not repair the registration flow the report failed on.
	sameFlow := s.currentSMSReceptionLocked(peer, checkpoint) ||
		s.currentSMSReceptionLocked(peer, smsReceptionCheckpoint{
			path: checkpoint.path, failureGeneration: s.smsReception.failureGeneration,
		})
	if sameFlow {
		// Advance the epoch so an MT already in flight cannot erase this fault.
		s.invalidateSMSReceptionLocked()
		s.smsReception.reportFailed = true
	}
	s.mu.Unlock()
	if sameFlow {
		s.notifySMSReadiness()
	}
}

func (s *Service) registrationSMSProvenLocked() bool {
	proof := s.smsReception
	return !proof.failed && proof.peer != nil && proof.peer == s.registrationTCP &&
		s.registrationTCPProtected && proof.path.samePath(s.downlinkCheckpointLocked())
}

package swu

import (
	"encoding/binary"
	"time"

	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"github.com/iniwex5/vowifi-go/engine/ipsec"
	"go.uber.org/zap"
)

// defersPeerChildRekeySwitch is the Spark preset key defer_peer_child_rekey_switch.
// After answering a peer CHILD_SA rekey, keep sending on the old SA until the
// peer uses the new one (RFC 7296 2.8).
func (s *Session) defersPeerChildRekeySwitch() bool {
	return s.cfg != nil && s.cfg.DeferPeerChildRekeySwitch
}

// pendingPeerChildRekey is a peer CHILD_SA rekey we answered but do not send
// on yet. RFC 7296 2.8: the responder keeps sending on the old SA until it
// receives a valid packet on the new one or the peer deletes the old one.
// Spark rekeyed the old SA again a second after our answer and never used the
// new one, so switching right away cut the tunnel. Guarded by childSAMu.
type pendingPeerChildRekey struct {
	runtime      *childSARuntime
	oldLocalSPI  uint32
	oldRemoteSPI uint32
	msgID        uint32
	dhGroup      uint16
	response     string
	answeredAt   time.Time
}

func (s *Session) answerPeerChildSARekeyDeferred(
	packet *ikev2.IKEPacket,
	response []ikev2.Payload,
	pending *pendingPeerChildRekey,
) error {
	pending.answeredAt = time.Now()
	// RFC 7296 2.8: be ready to receive on the new SA before answering.
	s.childSAMu.Lock()
	replaced := s.takePendingPeerRekeyLocked(nil)
	if s.espInboundSAs == nil {
		s.espInboundSAs = make(map[uint32]*ipsec.SecurityAssociation)
	}
	s.espInboundSAs[pending.runtime.localSPI] = pending.runtime.inbound
	s.pendingPeerRekey = pending
	s.pendingRekeySPI.Store(pending.runtime.localSPI)
	s.childSAMu.Unlock()
	if replaced != nil {
		wipeChildSARuntime(replaced.runtime)
	}
	if err := s.sendEstablishedIKEResponse(packet, response); err != nil {
		s.childSAMu.Lock()
		unsent := s.takePendingPeerRekeyLocked(pending)
		s.childSAMu.Unlock()
		if unsent != nil {
			wipeChildSARuntime(unsent.runtime)
		}
		return err
	}
	s.Logger.Info("peer CHILD_SA rekey answered; sending stays on the old SA until the peer uses the new one or deletes the old one",
		zap.Uint32("msg_id", pending.msgID),
		zap.Uint32("old_local_spi", pending.oldLocalSPI), zap.Uint32("old_remote_spi", pending.oldRemoteSPI),
		zap.Uint32("new_local_spi", pending.runtime.localSPI), zap.Uint32("new_remote_spi", pending.runtime.remoteSPI),
		zap.Uint16("dh_group", pending.dhGroup), zap.String("response", pending.response))
	return nil
}

// takePendingPeerRekeyLocked clears the pending rekey and stops accepting on
// its new SA. With want set it only clears that exact rekey.
func (s *Session) takePendingPeerRekeyLocked(want *pendingPeerChildRekey) *pendingPeerChildRekey {
	pending := s.pendingPeerRekey
	if pending == nil || want != nil && pending != want {
		return nil
	}
	delete(s.espInboundSAs, pending.runtime.localSPI)
	s.pendingPeerRekey = nil
	s.pendingRekeySPI.Store(0)
	return pending
}

func (s *Session) currentPendingPeerRekey() *pendingPeerChildRekey {
	s.childSAMu.RLock()
	defer s.childSAMu.RUnlock()
	return s.pendingPeerRekey
}

// adoptPendingPeerRekey moves sending to the answered SA with the given
// inbound SPI once the peer shows it installed it. Install and retirement of
// the old SA share one critical section, so a racing Delete of the old SA sees
// either the pending rekey or the old SA already retired.
func (s *Session) adoptPendingPeerRekey(trigger string, localSPI uint32) {
	s.childSAMu.Lock()
	pending := s.pendingPeerRekey
	if pending == nil || pending.runtime.localSPI != localSPI {
		s.childSAMu.Unlock()
		return
	}
	s.pendingPeerRekey = nil
	s.pendingRekeySPI.Store(0)
	s.installChildSARuntimeLocked(pending.runtime)
	if s.retiredChildSAs == nil {
		s.retiredChildSAs = make(map[uint32]uint32)
	}
	s.retiredChildSAs[pending.oldLocalSPI] = pending.oldRemoteSPI
	s.childSAMu.Unlock()
	s.logChildKeys(childKeysFromRuntime(pending.runtime))
	s.Logger.Info("peer CHILD_SA rekey installed",
		zap.String("trigger", trigger), zap.Duration("since_answer", time.Since(pending.answeredAt)),
		zap.Uint32("old_local_spi", pending.oldLocalSPI), zap.Uint32("old_remote_spi", pending.oldRemoteSPI),
		zap.Uint32("new_local_spi", pending.runtime.localSPI), zap.Uint32("new_remote_spi", pending.runtime.remoteSPI),
		zap.Uint16("dh_group", pending.dhGroup))
	s.markChildRekeyComplete()
}

// resolvePendingPeerRekeyDelete applies a peer ESP Delete to a pending rekey.
// It returns our inbound SPIs to list in the reply for SAs that only the
// pending rekey knew about.
func (s *Session) resolvePendingPeerRekeyDelete(spis []byte) []uint32 {
	pending := s.currentPendingPeerRekey()
	if pending == nil {
		return nil
	}
	for ; len(spis) >= 4; spis = spis[4:] {
		switch binary.BigEndian.Uint32(spis[:4]) {
		case pending.oldRemoteSPI:
			s.adoptPendingPeerRekey("peer deleted the old SA", pending.runtime.localSPI)
			return nil
		case pending.runtime.remoteSPI:
			s.childSAMu.Lock()
			deleted := s.takePendingPeerRekeyLocked(pending)
			s.childSAMu.Unlock()
			if deleted == nil {
				return nil
			}
			s.Logger.Warn("peer deleted the CHILD_SA we answered before using it; keeping the old SA",
				zap.Uint32("msg_id", deleted.msgID), zap.Duration("since_answer", time.Since(deleted.answeredAt)),
				zap.Uint32("new_local_spi", deleted.runtime.localSPI), zap.Uint32("new_remote_spi", deleted.runtime.remoteSPI))
			localSPI := deleted.runtime.localSPI
			wipeChildSARuntime(deleted.runtime)
			return []uint32{localSPI}
		}
	}
	return nil
}

// deferLocalRekeyForPeerAnswer holds back our own CHILD_SA rekey while a peer
// rekey answer is pending: rekeying the old SA would race it. The answer stays
// until the peer uses it, deletes either SA, rekeys the old SA again or the IKE
// SA ends; dropping it on a timer would lose an SA the peer may still adopt.
// ponytail: our rekeys wait as long as the answer is unused; Spark ends the
// IKE SA at 8 hours anyway. Add a bound if a carrier leaves answers unused.
func (s *Session) deferLocalRekeyForPeerAnswer() bool {
	pending := s.currentPendingPeerRekey()
	if pending == nil {
		return false
	}
	s.Logger.Info("local CHILD_SA rekey skipped; the peer has not used our answer to its rekey",
		zap.Uint32("answered_msg_id", pending.msgID), zap.Duration("since_answer", time.Since(pending.answeredAt)),
		zap.Uint32("unused_local_spi", pending.runtime.localSPI), zap.Uint32("unused_remote_spi", pending.runtime.remoteSPI))
	return true
}

// logUnusedPeerRekey records a pending answer the peer did not act on before
// it sent another request or ended the IKE SA.
func (s *Session) logUnusedPeerRekey(event string) {
	pending := s.currentPendingPeerRekey()
	if pending == nil {
		return
	}
	s.Logger.Warn("peer has not used our CHILD_SA rekey answer",
		zap.String("event", event), zap.Uint32("answered_msg_id", pending.msgID),
		zap.Duration("since_answer", time.Since(pending.answeredAt)),
		zap.Uint32("unused_local_spi", pending.runtime.localSPI), zap.Uint32("unused_remote_spi", pending.runtime.remoteSPI),
		zap.String("answered_response", pending.response))
}

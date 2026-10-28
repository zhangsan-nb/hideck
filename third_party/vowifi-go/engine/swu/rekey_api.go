package swu

import (
	"fmt"
	"time"

	enginecrypto "github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"go.uber.org/zap"
)

const rekeyCooldown = 30 * time.Second

func (s *Session) RekeyIKESA() error {
	s.rekeyMu.Lock()
	defer s.rekeyMu.Unlock()
	if s.ikeRekeyInCooldown() {
		return nil
	}
	s.dropStaleRetiredIKESA()
	if err := s.performIKESARekey(s.ctx); err != nil {
		return err
	}
	s.Logger.Info("IKE SA rekey completed")
	return nil
}

func (s *Session) RekeyChildSA() error {
	s.rekeyMu.Lock()
	defer s.rekeyMu.Unlock()
	if s.childRekeyInCooldown() || s.deferLocalRekeyForPeerAnswer() {
		return nil
	}
	err := s.performChildSARekey(s.ctx)
	if err == nil {
		s.Logger.Info("CHILD_SA rekey completed")
	}
	return err
}

func (s *Session) HandleRekeyIKESARequest(msgID uint32, payloads []ikev2.Payload) error {
	packet := &ikev2.IKEPacket{Header: newIKEHeader(
		s.spiI, s.spiR, ikev2.CREATE_CHILD_SA,
		s.localIKEFlags(false)^ikeInitiatorFlag, msgID,
	)}
	return s.handlePeerIKESARekey(packet, payloads)
}

func (s *Session) handleRekeyIKESAResp(
	data, initiatorNonce []byte,
	newDH *enginecrypto.DiffieHellman,
	newSPIi uint64,
	oldSKd []byte,
	oldSPIi, oldSPIr uint64,
) error {
	packet, err := ikev2.DecodePacket(data)
	if err != nil {
		return fmt.Errorf("swu: decode IKE SA rekey response: %w", err)
	}
	payloads, err := s.decryptAndParse(packet)
	if err != nil {
		return fmt.Errorf("swu: decrypt IKE SA rekey response: %w", err)
	}
	if err := ikeAuthenticationError(payloads); err != nil {
		return err
	}
	s.mu.RLock()
	oldInitiator := s.localIKEInitiator
	s.mu.RUnlock()
	return s.completeInitiatedIKESARekey(initiatedIKERekey{
		ctx: s.ctx, payloads: payloads, nonce: initiatorNonce, dh: newDH,
		initiatorSPI: ikeSPIBytes(newSPIi), oldSKd: oldSKd,
		oldSPIi: ikeSPIBytes(oldSPIi), oldSPIr: ikeSPIBytes(oldSPIr),
		oldInitiator: oldInitiator,
	})
}

func (s *Session) handleCreateChildSAResp(
	data, initiatorNonce []byte,
	newSPI uint32,
	newChildDH *enginecrypto.DiffieHellman,
) error {
	_, err := s.handleCreateChildSARespOffer(data, initiatorNonce, newSPI, newChildDH, childRekeyDHOffer{})
	return err
}

// handleCreateChildSARespOffer installs the answer to a CHILD_SA rekey offer
// and returns the selected D-H group, 0 for none.
func (s *Session) handleCreateChildSARespOffer(
	data, initiatorNonce []byte,
	newSPI uint32,
	newChildDH *enginecrypto.DiffieHellman,
	offer childRekeyDHOffer,
) (uint16, error) {
	packet, err := ikev2.DecodePacket(data)
	if err != nil {
		newChildDH.Wipe()
		return 0, fmt.Errorf("swu: decode CHILD_SA rekey response: %w", err)
	}
	payloads, err := s.decryptAndParse(packet)
	if err != nil {
		newChildDH.Wipe()
		return 0, fmt.Errorf("swu: decrypt CHILD_SA rekey response: %w", err)
	}
	if err := childSARejectionError(payloads); err != nil {
		newChildDH.Wipe()
		return 0, err
	}
	s.childSAMu.RLock()
	oldLocalSPI, oldRemoteSPI := s.espLocalSPI, s.espRemoteSPI
	s.childSAMu.RUnlock()
	selected, err := s.completeInitiatedChildSARekeySelected(initiatedChildRekey{
		payloads: payloads, nonce: initiatorNonce, localSPI: newSPI,
		oldLocalSPI: oldLocalSPI, oldRemoteSPI: oldRemoteSPI, newDH: newChildDH,
		offersNone: offer.offersNone(),
	})
	if err != nil {
		return 0, fmt.Errorf("%w (answer: %s)", err, createChildSAOffer(payloads))
	}
	return selected, nil
}

func childSARejectionError(payloads []ikev2.Payload) error {
	for _, payload := range payloads {
		notify, ok := payload.(*ikev2.EncryptedPayloadNotify)
		if ok && notify.NotifyType < 16384 {
			return &createChildSARejectError{
				NotifyType: notify.NotifyType, NotifyData: append([]byte(nil), notify.NotifyData...),
			}
		}
	}
	return nil
}

func (s *Session) signalRekeyReset(channel chan struct{}) {
	if channel == nil {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (s *Session) ikeRekeyInCooldown() bool {
	s.mu.RLock()
	last := s.lastIKERekeyTime
	s.mu.RUnlock()
	return !last.IsZero() && time.Since(last) < rekeyCooldown
}

func (s *Session) childRekeyInCooldown() bool {
	s.mu.RLock()
	last := s.lastRekeyTime
	s.mu.RUnlock()
	return !last.IsZero() && time.Since(last) < rekeyCooldown
}

func (s *Session) markIKERekeyComplete() {
	s.mu.Lock()
	s.lastIKERekeyTime = time.Now()
	channel := s.rekeyResetCh
	s.mu.Unlock()
	s.signalRekeyReset(channel)
}

func (s *Session) markChildRekeyComplete() {
	s.mu.Lock()
	s.lastRekeyTime = time.Now()
	channel := s.childRekeyResetCh
	s.mu.Unlock()
	s.signalRekeyReset(channel)
}

func (s *Session) handleIncomingCreateChildSAPacket(packet *ikev2.IKEPacket) error {
	payloads, err := s.decryptAndParse(packet)
	if err != nil {
		return err
	}
	// Before the rekeyMu check: a retransmission gets its first answer, not
	// TEMPORARY_FAILURE.
	if handled, err := s.answerRetransmittedCreateChildSA(packetIKEHeader(packet)); handled {
		return err
	}
	return s.handleIncomingCreateChildSAParsed(packetIKEHeader(packet), payloads)
}

func (s *Session) handleIncomingCreateChildSAParsed(request *ikev2.IKEHeader, payloads []ikev2.Payload) error {
	msgID := request.MessageID
	s.Logger.Info("peer CREATE_CHILD_SA request", zap.Uint32("msg_id", msgID),
		zap.String("offer", createChildSAOffer(payloads)))
	if !s.rekeyMu.TryLock() {
		s.Logger.Warn("answered peer CREATE_CHILD_SA with TEMPORARY_FAILURE", zap.String("reason", "local rekey in progress"))
		return s.sendRekeyCollisionResponse(request)
	}
	defer s.rekeyMu.Unlock()
	protocolID, err := createChildSAProtocol(payloads)
	if err != nil {
		return err
	}
	packet := &ikev2.IKEPacket{Header: newIKEHeader(
		s.spiI, s.spiR, ikev2.CREATE_CHILD_SA, s.localIKEFlags(false)^ikeInitiatorFlag, msgID,
	)}
	if protocolID == ikev2.ProtoIKE {
		if s.hasRetiredIKESA() {
			s.Logger.Warn("answered peer CREATE_CHILD_SA with TEMPORARY_FAILURE", zap.String("reason", "previous IKE SA still retiring"))
			return s.sendRekeyCollisionResponse(request)
		}
		return s.HandleRekeyIKESARequest(msgID, payloads)
	}
	return s.handlePeerChildSARekeyPayloads(packet, payloads)
}

// RFC 7296 2.1: a retransmission of this request gets the same refusal. It is
// recorded under the IKE SA the request came on, never one installed since.
func (s *Session) sendRekeyCollisionResponse(request *ikev2.IKEHeader) error {
	payloads := []ikev2.Payload{&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.TEMPORARY_FAILURE}}
	s.recordCreateChildSAAnswer(request.MessageID, request.SPIi, request.SPIr, payloads)
	return s.sendEncryptedResponseWithMsgID(payloads, ikev2.CREATE_CHILD_SA, request.MessageID)
}

func createChildSAProtocol(payloads []ikev2.Payload) (ikev2.ProtocolID, error) {
	offered := false
	for _, payload := range payloads {
		sa, ok := payload.(*ikev2.EncryptedPayloadSA)
		if !ok {
			continue
		}
		// RFC 7296 2.7: unacceptable proposals do not stop the others from
		// being processed, so the first IKE or ESP proposal decides.
		for _, proposal := range sa.Proposals {
			if proposal == nil {
				continue
			}
			offered = true
			if proposal.ProtocolID == ikev2.ProtoIKE || proposal.ProtocolID == ikev2.ProtoESP {
				return proposal.ProtocolID, nil
			}
		}
	}
	if offered {
		// Only unsupported protocols: the CHILD_SA path refuses it with a
		// notify and keeps the session.
		return ikev2.ProtoESP, nil
	}
	return 0, fmt.Errorf("swu: CREATE_CHILD_SA request has no SA proposal (payloads: %s)", createChildSAOffer(payloads))
}

func (s *Session) handleIncomingInformationalPacket(packet *ikev2.IKEPacket) error {
	s.rekeyMu.Lock()
	defer s.rekeyMu.Unlock()
	return s.handlePeerInformational(packet)
}

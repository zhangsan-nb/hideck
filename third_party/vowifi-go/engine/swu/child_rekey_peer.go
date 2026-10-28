package swu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	enginecrypto "github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"github.com/iniwex5/vowifi-go/engine/ipsec"
	"go.uber.org/zap"
)

func (s *Session) handlePeerChildSARekey(packet *ikev2.IKEPacket) error {
	payloads, err := s.decryptAndParse(packet)
	if err != nil {
		return err
	}
	return s.handlePeerChildSARekeyPayloads(packet, payloads)
}

func (s *Session) handlePeerChildSARekeyPayloads(packet *ikev2.IKEPacket, payloads []ikev2.Payload) error {
	if handled, err := s.answerRetransmittedCreateChildSA(packetIKEHeader(packet)); handled {
		return err
	}
	// After validation: a REKEY_SA naming the answered SA adopts it first.
	notify, refused := s.validatePeerRekeyNotify(payloads)
	s.logUnusedPeerRekey("peer sent another CREATE_CHILD_SA")
	if refused != nil {
		// RFC 7296 2.25.1 and 1.3: refuse with a notify and keep the session.
		s.Logger.Warn("peer CHILD_SA request not accepted; answered with notify",
			zap.Uint16("notify", notify.NotifyType), zap.Error(refused), zap.String("offer", createChildSAOffer(payloads)))
		return s.sendEstablishedIKEResponse(packet, []ikev2.Payload{notify})
	}
	offer := payloads
	group := s.currentChildDHGroup()
	narrowed, proposalNum, ok := narrowPeerRekeyOffer(payloads, ikev2.ProtoESP, s.peerRekeyWant(group), s.espEncKeyBits)
	// RFC 7296 2.8: a rekeyed CHILD_SA SHOULD keep the current algorithms.
	// A CHILD_SA without PFS may also gain PFS in the IKE SA group, as our
	// own rekey offers; any other change is refused and the current SA stays.
	if group == 0 && s.dhGroup != 0 {
		// Our choice, not an RFC rule: PFS first when KEi is in the IKE SA
		// group, otherwise NONE first.
		order := []uint16{0, s.dhGroup}
		if peerKEGroup(payloads) == s.dhGroup {
			order = []uint16{s.dhGroup, 0}
		}
		for _, candidate := range order {
			narrowed, proposalNum, ok = narrowPeerRekeyOffer(payloads, ikev2.ProtoESP, s.peerRekeyWant(candidate), s.espEncKeyBits)
			if ok {
				group = candidate
				break
			}
		}
		if ok && group != 0 {
			s.Logger.Info("peer CHILD_SA rekey adds PFS in the IKE SA group",
				zap.Uint16("dh_group", group), zap.String("offer", createChildSAOffer(offer)))
		}
	}
	if !ok {
		reason := fmt.Sprintf("no proposal matches the current CHILD_SA suite %s", s.childSuiteDescription())
		return s.rejectPeerRekeyOffer(packet, offer, errors.New(reason))
	}
	payloads, ok, err := s.checkPeerRekeyKE(packet, narrowed, group)
	if !ok || err != nil {
		return err
	}
	currentTSi, currentTSr := s.currentChildSelectors()
	peerTSi := retypeTrafficSelectorPayload(currentTSr, ikev2.PayloadTSi)
	peerTSr := retypeTrafficSelectorPayload(currentTSi, ikev2.PayloadTSr)
	selection, err := validateChildSAResponse(payloads, childSAOffer{
		encryption: s.espCipher, encryptionKeyBits: s.espEncKeyBits, integrity: s.espInteg,
		dhGroup: group, esn: s.espESN, tsi: peerTSi, tsr: peerTSr,
		requireSA: true, requireNonce: true,
	})
	if err != nil {
		return s.rejectPeerRekeyOffer(packet, offer, err)
	}
	if !selectorsContainAnyIP(selection.tsr, configuredInnerIPs(s)) {
		return s.rejectPeerRekeyOffer(packet, offer,
			errors.New("peer CHILD_SA TSr does not contain an assigned inner address"))
	}
	return s.answerPeerChildSARekey(packet, offer, payloads, selection, proposalNum)
}

// peerKEGroup is the group of the first KE payload, 0 without one.
func peerKEGroup(payloads []ikev2.Payload) uint16 {
	for _, payload := range payloads {
		if payload != nil && payload.Type() == ikev2.PayloadKE {
			group, _, err := parseKEPayload(payload)
			if err != nil {
				return 0
			}
			return group
		}
	}
	return 0
}

func (s *Session) peerRekeyWant(group uint16) map[ikev2.TransformType]ikev2.AlgorithmType {
	esn := ikev2.AlgorithmType(0)
	if s.espESN {
		esn = 1
	}
	want := map[ikev2.TransformType]ikev2.AlgorithmType{
		ikev2.TransformTypeEncr: ikev2.AlgorithmType(s.espCipher),
		ikev2.TransformTypeDH:   ikev2.AlgorithmType(group),
		ikev2.TransformTypeESN:  esn,
	}
	if s.espInteg != 0 {
		want[ikev2.TransformTypeInteg] = ikev2.AlgorithmType(s.espInteg)
	}
	return want
}

// childSuiteDescription uses the createChildSAOffer notation (type=id/bits).
func (s *Session) childSuiteDescription() string {
	esn := 0
	if s.espESN {
		esn = 1
	}
	return fmt.Sprintf("1=%d/%d,3=%d,4=%d,5=%d",
		s.espCipher, s.espEncKeyBits, s.espInteg, s.currentChildDHGroup(), esn)
}

// narrowPeerRekeyOffer implements the responder side of RFC 7296 2.7: from a
// peer CREATE_CHILD_SA request that may carry several proposals and several
// transforms per type, pick the first proposal for protocol that contains the
// suite this SA already runs and reduce it to one transform per type. The
// returned payloads carry that single proposal renumbered to 1 for validation;
// proposalNum is the peer's original number, which the response MUST echo
// (RFC 7296 3.3.1).
func narrowPeerRekeyOffer(
	payloads []ikev2.Payload,
	protocol ikev2.ProtocolID,
	want map[ikev2.TransformType]ikev2.AlgorithmType,
	keyBits uint16,
) ([]ikev2.Payload, uint8, bool) {
	for index, payload := range payloads {
		sa, ok := payload.(*ikev2.EncryptedPayloadSA)
		if !ok {
			continue
		}
		for _, proposal := range sa.Proposals {
			if proposal == nil || proposal.ProtocolID != protocol {
				continue
			}
			narrowed, ok := narrowPeerProposal(proposal, want, keyBits)
			if !ok {
				continue
			}
			proposalNum := narrowed.ProposalNum
			narrowed.ProposalNum = 1
			out := append([]ikev2.Payload(nil), payloads...)
			out[index] = &ikev2.EncryptedPayloadSA{Proposals: []*ikev2.Proposal{narrowed}}
			return out, proposalNum, true
		}
		return payloads, 0, false
	}
	return payloads, 0, true
}

// narrowPeerProposal accepts a proposal only when every transform type it
// carries has an acceptable entry and every type of the current suite is
// present. D-H NONE stands for no PFS and INTEG NONE for an AEAD cipher; both
// are equivalent to leaving the type out and are dropped (RFC 7296 3.3.3).
func narrowPeerProposal(
	proposal *ikev2.Proposal,
	want map[ikev2.TransformType]ikev2.AlgorithmType,
	keyBits uint16,
) (*ikev2.Proposal, bool) {
	if proposal == nil {
		return nil, false
	}
	narrowed := *proposal
	narrowed.Transforms = nil
	picked := make(map[ikev2.TransformType]bool)
	none := make(map[ikev2.TransformType]bool)
	for _, transform := range proposal.Transforms {
		if transform == nil {
			return nil, false
		}
		id, known := want[transform.Type]
		if transform.ID == 0 && (transform.Type == ikev2.TransformTypeDH && known && id == 0 ||
			transform.Type == ikev2.TransformTypeInteg && !known) {
			none[transform.Type] = true
			continue
		}
		if !known {
			if transform.Type == ikev2.TransformTypeInteg {
				continue
			}
			return nil, false
		}
		if picked[transform.Type] || transform.ID != id || len(transform.Attributes) > 0 && transform.Type != ikev2.TransformTypeEncr {
			continue
		}
		if transform.Type == ikev2.TransformTypeEncr && validateEncryptionKeyLength(transform, keyBits) != nil {
			continue
		}
		narrowed.Transforms = append(narrowed.Transforms, transform)
		picked[transform.Type] = true
	}
	for _, transform := range proposal.Transforms {
		if !picked[transform.Type] && !none[transform.Type] {
			return nil, false
		}
	}
	for transformType, id := range want {
		if !picked[transformType] && !(transformType == ikev2.TransformTypeDH && id == 0) {
			return nil, false
		}
	}
	narrowed.NumTransforms = uint8(len(narrowed.Transforms))
	return &narrowed, len(narrowed.Transforms) > 0
}

// rejectPeerRekeyOffer answers an unacceptable peer rekey with
// NO_PROPOSAL_CHOSEN and keeps the current SA (RFC 7296 2.7, 2.8).
func (s *Session) rejectPeerRekeyOffer(packet *ikev2.IKEPacket, payloads []ikev2.Payload, reason error) error {
	s.Logger.Warn("peer rekey not accepted; answered NO_PROPOSAL_CHOSEN and kept the current SA",
		zap.String("offer", createChildSAOffer(payloads)), zap.Error(reason))
	return s.sendEstablishedIKEResponse(packet, []ikev2.Payload{
		&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.NO_PROPOSAL_CHOSEN},
	})
}

// checkPeerRekeyKE matches the KE payload against the selected D-H group.
// With no group the KE is ignored. A missing or different KE is answered
// with INVALID_KE_PAYLOAD naming the group, keeping the current SA (RFC 7296
// 1.3, 2.7). ok is false when such an answer was sent.
func (s *Session) checkPeerRekeyKE(
	packet *ikev2.IKEPacket,
	payloads []ikev2.Payload,
	group uint16,
) ([]ikev2.Payload, bool, error) {
	if group == 0 {
		kept := make([]ikev2.Payload, 0, len(payloads))
		for _, payload := range payloads {
			if payload == nil || payload.Type() != ikev2.PayloadKE {
				kept = append(kept, payload)
			}
		}
		return kept, true, nil
	}
	got := uint16(0)
	for _, payload := range payloads {
		if payload == nil || payload.Type() != ikev2.PayloadKE {
			continue
		}
		var err error
		got, _, err = parseKEPayload(payload)
		if err == nil && got == group {
			return payloads, true, nil
		}
		break
	}
	s.Logger.Warn("peer rekey KE does not use the selected D-H group; answered INVALID_KE_PAYLOAD",
		zap.Uint16("selected", group), zap.Uint16("ke", got), zap.String("ke_payloads", describeKEPayloads(payloads)))
	return payloads, false, s.sendEstablishedIKEResponse(packet, []ikev2.Payload{
		&ikev2.EncryptedPayloadNotify{NotifyType: ikev2.INVALID_KE_PAYLOAD, NotifyData: []byte{byte(group >> 8), byte(group)}},
	})
}

func (s *Session) answerPeerChildSARekey(
	packet *ikev2.IKEPacket,
	offer []ikev2.Payload,
	payloads []ikev2.Payload,
	selection *childSASelection,
	proposalNum uint8,
) error {
	localNonce, localSPI, err := s.newChildSAInitiatorMaterial()
	if err != nil {
		return err
	}
	dh, sharedSecret, err := preparePeerChildRekeyDH(payloads, selection.dhGroup)
	if err != nil {
		// A bad KE is the peer's problem: refuse it and keep the SA.
		return s.rejectPeerRekeyOffer(packet, offer,
			fmt.Errorf("peer CHILD_SA KE unusable (%s): %w", describeKEPayloads(payloads), err))
	}
	runtime, err := s.prepareChildSARuntime(childSARuntimeSpec{
		localSPI: localSPI, remoteSPI: selection.remoteSPI,
		initiatorNonce: selection.nonce, responderNonce: localNonce,
		sharedSecret: sharedSecret, dh: dh,
		tsi: retypeTrafficSelectorPayload(selection.tsr, ikev2.PayloadTSi),
		tsr: retypeTrafficSelectorPayload(selection.tsi, ikev2.PayloadTSr), localInitiator: false,
	})
	if err != nil {
		if dh != nil {
			enginecrypto.Wipe(dh.SharedKey)
		}
		return err
	}
	proposals := buildESPProposalsForSession(s, localSPI)
	if dh != nil {
		proposals[0].AddTransform(ikev2.TransformTypeDH, ikev2.AlgorithmType(dh.Group), 0)
	}
	if proposalNum != 0 {
		proposals[0].ProposalNum = proposalNum
	}
	response := []ikev2.Payload{
		&ikev2.EncryptedPayloadSA{Proposals: proposals},
		&ikev2.EncryptedPayloadNonce{NonceData: append([]byte(nil), localNonce...)},
	}
	if dh != nil {
		response = append(response, &ikev2.EncryptedPayloadKE{
			DHGroup: ikev2.AlgorithmType(dh.Group), KEData: dh.PublicKeyBytes(),
		})
	}
	response = append(response, cloneTrafficSelectorPayload(selection.tsi), cloneTrafficSelectorPayload(selection.tsr))
	s.childSAMu.RLock()
	oldLocalSPI, oldRemoteSPI := s.espLocalSPI, s.espRemoteSPI
	s.childSAMu.RUnlock()
	trigger := "immediate"
	if s.defersPeerChildRekeySwitch() {
		if s.currentKernelDataPlane() == nil {
			return s.answerPeerChildSARekeyDeferred(packet, response, &pendingPeerChildRekey{
				runtime: runtime, oldLocalSPI: oldLocalSPI, oldRemoteSPI: oldRemoteSPI,
				msgID: packetIKEHeader(packet).MessageID, dhGroup: selection.dhGroup,
				response: createChildSAOffer(response),
			})
		}
		// ponytail: XFRM Rekey switches both directions at once, so the kernel
		// plane still sends on the new SA right away. Split Rekey into an
		// inbound install and an outbound switch to defer it there too.
		trigger = "kernel data plane"
	}
	if err := s.sendEstablishedIKEResponse(packet, response); err != nil {
		wipeChildSARuntime(runtime)
		return err
	}
	if err := s.activateChildSARuntime(runtime); err != nil {
		return err
	}
	s.Logger.Info("peer CHILD_SA rekey installed",
		zap.Uint32("old_local_spi", oldLocalSPI), zap.Uint32("old_remote_spi", oldRemoteSPI),
		zap.Uint32("new_local_spi", runtime.localSPI), zap.Uint32("new_remote_spi", runtime.remoteSPI),
		zap.Uint16("dh_group", selection.dhGroup), zap.String("trigger", trigger),
		zap.String("response", createChildSAOffer(response)))
	s.markChildRekeyComplete()
	s.recordRetiredChildSA(oldRemoteSPI, oldLocalSPI)
	return nil
}

func preparePeerChildRekeyDH(
	payloads []ikev2.Payload,
	group uint16,
) (*enginecrypto.DiffieHellman, []byte, error) {
	if group == 0 {
		_, err := childRekeySharedSecret(payloads, nil)
		return nil, nil, err
	}
	dh, err := enginecrypto.NewDiffieHellman(group)
	if err != nil {
		return nil, nil, err
	}
	if err := dh.GenerateKey(); err != nil {
		return nil, nil, err
	}
	secret, err := childRekeySharedSecret(payloads, dh)
	if err != nil {
		return nil, nil, err
	}
	return dh, secret, nil
}

// describeKEPayloads lists every KE as group/length, or "none".
func describeKEPayloads(payloads []ikev2.Payload) string {
	parts := []string{}
	for _, payload := range payloads {
		if payload == nil || payload.Type() != ikev2.PayloadKE {
			continue
		}
		group, data, err := parseKEPayload(payload)
		if err != nil {
			parts = append(parts, "invalid: "+err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("%d/%d", group, len(data)))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

func retypeTrafficSelectorPayload(payload *ikev2.EncryptedPayloadTS, payloadType ikev2.PayloadType) *ikev2.EncryptedPayloadTS {
	cloned := cloneTrafficSelectorPayload(payload)
	if cloned != nil {
		cloned.IsInitiator = payloadType == ikev2.TSI
	}
	return cloned
}

// validatePeerRekeyNotify checks the REKEY_SA notify of a peer CHILD_SA
// request; on error it also returns the notify type to answer with.
func (s *Session) validatePeerRekeyNotify(payloads []ikev2.Payload) (*ikev2.EncryptedPayloadNotify, error) {
	s.childSAMu.RLock()
	expectedSPI := s.espRemoteSPI
	s.childSAMu.RUnlock()
	for _, payload := range payloads {
		notify, ok := payload.(*ikev2.EncryptedPayloadNotify)
		if !ok || notify.ProtocolID != ikev2.ProtoESP || len(notify.SPI) != 4 ||
			notify.NotifyType != ikev2.NotifyTypeRekeySA {
			continue
		}
		got := binary.BigEndian.Uint32(notify.SPI)
		if got == expectedSPI {
			return nil, nil
		}
		if pending := s.currentPendingPeerRekey(); pending != nil && got == pending.runtime.remoteSPI {
			// Rekeying the SA we answered shows the peer installed it (RFC 7296 2.8).
			s.adoptPendingPeerRekey("peer rekeyed the answered SA", pending.runtime.localSPI)
		}
		// Read again: the data path may have adopted the answered SA meanwhile.
		if s.currentChildRemoteSPI() == got {
			return nil, nil
		}
		if s.retiredChildRemoteSPI(got) {
			// RFC 7296 2.25.1: the SA still exists but is being closed.
			return &ikev2.EncryptedPayloadNotify{NotifyType: ikev2.TEMPORARY_FAILURE},
				fmt.Errorf("swu: peer REKEY_SA spi %08x names a CHILD_SA being closed", got)
		}
		// RFC 7296 2.25: the SPI is copied from the REKEY_SA notification.
		return &ikev2.EncryptedPayloadNotify{
			ProtocolID: ikev2.ProtoESP, SPI: append([]byte(nil), notify.SPI...), NotifyType: ikev2.CHILD_SA_NOT_FOUND,
		}, fmt.Errorf("swu: peer REKEY_SA spi %08x is not the current %08x", got, expectedSPI)
	}
	return &ikev2.EncryptedPayloadNotify{NotifyType: ikev2.NO_ADDITIONAL_SAS},
		errors.New("swu: peer CREATE_CHILD_SA without REKEY_SA asks for an additional CHILD_SA")
}

func (s *Session) sendEstablishedIKEResponse(request *ikev2.IKEPacket, payloads []ikev2.Payload) error {
	header := packetIKEHeader(request)
	if header.ExchangeType == ikev2.CREATE_CHILD_SA {
		s.recordCreateChildSAAnswer(header.MessageID, header.SPIi, header.SPIr, payloads)
	}
	return s.sendEncryptedResponseWithMsgID(payloads, header.ExchangeType, header.MessageID)
}

// createChildSAAnswer is our last response to a peer CREATE_CHILD_SA request.
// Guarded by childSAMu.
type createChildSAAnswer struct {
	msgID      uint32
	spiI, spiR uint64
	payloads   []ikev2.Payload
}

func (s *Session) recordCreateChildSAAnswer(msgID uint32, spiI, spiR uint64, payloads []ikev2.Payload) {
	s.childSAMu.Lock()
	s.lastCreateChildSAAnswer = &createChildSAAnswer{msgID: msgID, spiI: spiI, spiR: spiR, payloads: payloads}
	s.childSAMu.Unlock()
}

// answerRetransmittedCreateChildSA applies RFC 7296 2.1 to a peer request on
// the IKE SA of our last answer: the same message ID gets that answer again,
// an older one is ignored. A new answer would name SAs the peer never saw.
func (s *Session) answerRetransmittedCreateChildSA(header *ikev2.IKEHeader) (bool, error) {
	s.childSAMu.RLock()
	last := s.lastCreateChildSAAnswer
	s.childSAMu.RUnlock()
	if header == nil || last == nil || header.SPIi != last.spiI || header.SPIr != last.spiR {
		return false, nil
	}
	if current := newIKEHeader(s.spiI, s.spiR, ikev2.CREATE_CHILD_SA, 0, 0); current.SPIi != last.spiI || current.SPIr != last.spiR {
		// The IKE SA was rekeyed after the request arrived; its answer is gone.
		return true, nil
	}
	switch {
	case header.MessageID == last.msgID:
		s.Logger.Info("peer retransmitted its CREATE_CHILD_SA request; resent our answer",
			zap.Uint32("msg_id", header.MessageID), zap.String("response", createChildSAOffer(last.payloads)))
		return true, s.sendEncryptedResponseWithMsgID(last.payloads, ikev2.CREATE_CHILD_SA, header.MessageID)
	case header.MessageID < last.msgID: // message IDs never wrap (RFC 7296 2.2)
		s.Logger.Warn("ignored a peer CREATE_CHILD_SA request older than our last answer",
			zap.Uint32("msg_id", header.MessageID), zap.Uint32("last_answered_msg_id", last.msgID))
		return true, nil
	}
	return false, nil
}

func (s *Session) currentChildRemoteSPI() uint32 {
	s.childSAMu.RLock()
	defer s.childSAMu.RUnlock()
	return s.espRemoteSPI
}

func (s *Session) retiredChildRemoteSPI(spi uint32) bool {
	s.childSAMu.RLock()
	defer s.childSAMu.RUnlock()
	for _, remote := range s.retiredChildSAs {
		if remote == spi {
			return true
		}
	}
	return false
}

func (s *Session) handlePeerInformational(packet *ikev2.IKEPacket) error {
	header := packetIKEHeader(packet)
	context, retiredIKE, err := s.ikeContextForHeader(header)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnauthenticatedIKE, err)
	}
	if err := validateIKEContextRole(context, header); err != nil {
		return fmt.Errorf("%w: %w", errUnauthenticatedIKE, err)
	}
	payloads, err := s.decryptAndParseWithKeys(packet, context.keys)
	if err != nil {
		return err
	}
	redirectAddress := s.runtimeRedirectAddress(payloads)
	responsePayloads, err := s.peerMOBIKEResponse(payloads, retiredIKE)
	if err != nil {
		return err
	}
	var activeChildDelete, ikeDelete bool
	identityReply, err := s.informationalDeviceIdentityReply(payloads)
	if err != nil {
		return err
	}
	responsePayloads = append(responsePayloads, identityReply...)
	var responseSPIs []uint32
	for _, payload := range payloads {
		deletion, ok := payload.(*ikev2.EncryptedPayloadDelete)
		if !ok {
			continue
		}
		if deletion.ProtocolID == ikev2.ProtoIKE {
			ikeDelete = true
			continue
		}
		if deletion.ProtocolID != ikev2.ProtoESP {
			continue
		}
		responseSPIs = append(responseSPIs, s.resolvePendingPeerRekeyDelete(deletion.SPIs)...)
		activeChildDelete = activeChildDelete || s.deleteContainsCurrentChildSA(deletion.SPIs)
		responseSPIs = append(responseSPIs, s.retireDeletedChildSAs(deletion.SPIs)...)
	}
	responsePayloads = append(responsePayloads, childSADeleteResponse(responseSPIs)...)
	var retiredResponse []byte
	if retiredIKE {
		retiredResponse, err = s.sendIKEContextResponse(packet, responsePayloads, context)
	} else {
		err = s.sendEstablishedIKEResponse(packet, responsePayloads)
	}
	if err != nil {
		return err
	}
	s.handleRuntimeRedirect(redirectAddress)
	if ikeDelete {
		s.logUnusedPeerRekey("peer deleted the IKE_SA")
		if retiredIKE {
			return s.completeRetiredIKESADelete(context, packet, retiredResponse)
		}
		return errors.New("swu: peer deleted the active IKE_SA")
	}
	if activeChildDelete {
		return errors.New("swu: peer deleted the active CHILD_SA")
	}
	return nil
}

func childSADeleteResponse(spis []uint32) []ikev2.Payload {
	if len(spis) == 0 {
		return nil
	}
	responses := make([]ikev2.Payload, 0, (len(spis)+maxDeleteSPIs-1)/maxDeleteSPIs)
	for start := 0; start < len(spis); start += maxDeleteSPIs {
		end := min(start+maxDeleteSPIs, len(spis))
		encoded := make([]byte, 0, 4*(end-start))
		for _, spi := range spis[start:end] {
			encoded = append(encoded, spiBytes(spi)...)
		}
		responses = append(responses, &ikev2.EncryptedPayloadDelete{
			ProtocolID: ikev2.ProtoESP, SPISize: 4,
			NumSPIs: uint16(end - start), SPIs: encoded,
		})
	}
	return responses
}

func (s *Session) retireDeletedChildSAs(spis []byte) []uint32 {
	var retired []*ipsec.SecurityAssociation
	var retiredLocalSPIs []uint32
	var responseSPIs []uint32
	// RFC 7296 1.4.1: the peer lists its inbound SPIs (our outbound/remote
	// ones); we answer with our inbound halves of the same pairs.
	s.childSAMu.Lock()
	for len(spis) >= 4 {
		remoteSPI := binary.BigEndian.Uint32(spis[:4])
		if localSPI, ok := s.retiredLocalSPIForRemoteLocked(remoteSPI); ok {
			responseSPIs = append(responseSPIs, localSPI)
			retired = append(retired, s.espInboundSAs[localSPI])
			retiredLocalSPIs = append(retiredLocalSPIs, localSPI)
			delete(s.espInboundSAs, localSPI)
			delete(s.retiredChildSAs, localSPI)
		} else if remoteSPI == s.espRemoteSPI {
			responseSPIs = append(responseSPIs, s.espLocalSPI)
		}
		spis = spis[4:]
	}
	s.childSAMu.Unlock()
	for _, association := range retired {
		wipeESPAssociation(association)
	}
	for _, localSPI := range retiredLocalSPIs {
		s.retireKernelInboundChildSA(localSPI)
	}
	return responseSPIs
}

func (s *Session) retiredLocalSPIForRemoteLocked(remoteSPI uint32) (uint32, bool) {
	for localSPI, retiredRemoteSPI := range s.retiredChildSAs {
		if retiredRemoteSPI == remoteSPI {
			return localSPI, true
		}
	}
	return 0, false
}

func (s *Session) deleteContainsCurrentChildSA(spis []byte) bool {
	s.childSAMu.RLock()
	current := s.espRemoteSPI
	s.childSAMu.RUnlock()
	for len(spis) >= 4 {
		if binary.BigEndian.Uint32(spis[:4]) == current {
			return true
		}
		spis = spis[4:]
	}
	return false
}

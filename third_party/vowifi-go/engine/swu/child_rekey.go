package swu

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	enginecrypto "github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"github.com/iniwex5/vowifi-go/engine/logger"
	"go.uber.org/zap"
)

type initiatedChildRekey struct {
	payloads     []ikev2.Payload
	nonce        []byte
	localSPI     uint32
	oldLocalSPI  uint32
	oldRemoteSPI uint32
	newDH        *enginecrypto.DiffieHellman
	offersNone   bool
}

type childSARekeyRequest struct {
	localSPI uint32
	nonce    []byte
	tsi      *ikev2.EncryptedPayloadTS
	tsr      *ikev2.EncryptedPayloadTS
	dh       *enginecrypto.DiffieHellman
	// dhGroups lists the offered D-H transforms in order; 0 is NONE. Empty
	// with a dh offers dh.Group alone.
	dhGroups []uint16
}

// childRekeyDHOffer is the D-H part of a UE-initiated CHILD_SA rekey offer.
type childRekeyDHOffer struct {
	keGroup uint16   // group of KEi, 0 without KEi
	groups  []uint16 // offered D-H transforms, 0 is NONE
}

func (o childRekeyDHOffer) offersNone() bool {
	for _, group := range o.groups {
		if group == 0 {
			return true
		}
	}
	return false
}

func (s *Session) performChildSARekey(ctx context.Context) error {
	s.ikeExchangeMu.Lock()
	defer s.ikeExchangeMu.Unlock()
	if s.transport() == nil || s.ikeKeys == nil || s.State() != stateEstablished {
		return errors.New("swu: session not established")
	}
	offer := s.childRekeyDHOffer()
	if !offer.offersNone() {
		return s.attemptChildSARekey(offer)
	}
	// RFC 7296 3.3.2 lets an initiator propose D-H as optional ([G, NONE]) or
	// leave it out. Some responders refuse the optional form, others the
	// omitted one: try the form that last worked, the other on
	// NO_PROPOSAL_CHOSEN.
	forms := []childRekeyDHOffer{offer, {}}
	if s.childRekeyOmitDH.Load() {
		forms[0], forms[1] = forms[1], forms[0]
	}
	err := s.attemptChildSARekey(forms[0])
	if err == nil || !isNoProposalChosen(err) {
		if err == nil {
			s.childRekeyOmitDH.Store(len(forms[0].groups) == 0)
		}
		return err
	}
	s.Logger.Info("CHILD_SA rekey offer refused; retrying with the other D-H form",
		zap.Uint16s("refused_dh", forms[0].groups), zap.Uint16s("retry_dh", forms[1].groups), zap.Error(err))
	retryErr := s.attemptChildSARekey(forms[1])
	if retryErr != nil {
		return fmt.Errorf("%w (after retrying offered_dh=%v; first attempt: %v)", retryErr, forms[1].groups, err)
	}
	s.childRekeyOmitDH.Store(len(forms[1].groups) == 0)
	return nil
}

func isNoProposalChosen(err error) bool {
	var rejection *createChildSARejectError
	return errors.As(err, &rejection) && rejection.NotifyType == ikev2.NO_PROPOSAL_CHOSEN
}

func (s *Session) attemptChildSARekey(offer childRekeyDHOffer) error {
	ni, localSPI, err := s.newChildSAInitiatorMaterial()
	if err != nil {
		return err
	}
	tsi, tsr := s.currentChildSelectors()
	newDH, err := s.newChildSARekeyDH(offer.keGroup)
	if err != nil {
		return err
	}
	payloads := s.buildChildSARekeyPayloads(childSARekeyRequest{
		localSPI: localSPI, nonce: ni, tsi: tsi, tsr: tsr, dh: newDH, dhGroups: offer.groups,
	})
	response, err := s.sendEncryptedWithRetry(payloads, ikev2.CREATE_CHILD_SA)
	if err != nil {
		newDH.Wipe()
		return fmt.Errorf("%w (offer: %s)", err, createChildSAOffer(payloads))
	}
	selected, err := s.handleCreateChildSARespOffer(response, ni, localSPI, newDH, offer)
	if err != nil {
		return fmt.Errorf("%w (offer: %s; offered_dh=%v ike_dh=%d)",
			err, createChildSAOffer(payloads), offer.groups, s.dhGroup)
	}
	s.Logger.Info("CHILD_SA rekey negotiated",
		zap.Uint16("dh_group", selected), zap.Uint16s("offered_dh", offer.groups),
		zap.Uint16("ike_dh_group", s.dhGroup))
	return nil
}

// childRekeyDHOffer keeps the D-H group of a CHILD_SA that has PFS. Without
// PFS it offers the IKE SA group and NONE with KEi in the IKE SA group, so the
// responder picks PFS or not (RFC 7296 3.3.2, 3.3.6).
func (s *Session) childRekeyDHOffer() childRekeyDHOffer {
	if group := s.currentChildDHGroup(); group != 0 {
		return childRekeyDHOffer{keGroup: group, groups: []uint16{group}}
	}
	if s.dhGroup == 0 {
		return childRekeyDHOffer{}
	}
	return childRekeyDHOffer{keGroup: s.dhGroup, groups: []uint16{s.dhGroup, 0}}
}

func (s *Session) completeInitiatedChildSARekey(
	payloads []ikev2.Payload,
	initiatorNonce []byte,
	localSPI uint32,
) error {
	s.childSAMu.RLock()
	oldLocalSPI, oldRemoteSPI := s.espLocalSPI, s.espRemoteSPI
	s.childSAMu.RUnlock()
	return s.completeInitiatedChildSARekeyWithOld(initiatedChildRekey{
		payloads: payloads, nonce: initiatorNonce, localSPI: localSPI,
		oldLocalSPI: oldLocalSPI, oldRemoteSPI: oldRemoteSPI,
	})
}

func (s *Session) completeInitiatedChildSARekeyWithOld(rekey initiatedChildRekey) error {
	_, err := s.completeInitiatedChildSARekeySelected(rekey)
	return err
}

// completeInitiatedChildSARekeySelected installs the answered CHILD_SA and
// returns the selected D-H group, 0 when the responder chose NONE.
func (s *Session) completeInitiatedChildSARekeySelected(rekey initiatedChildRekey) (uint16, error) {
	installed := false
	offeredKE := rekey.newDH
	defer func() {
		if !installed {
			offeredKE.Wipe()
		}
	}()
	tsi, tsr := s.currentChildSelectors()
	selection, err := validateChildSAResponse(rekey.payloads, childSAOffer{
		encryption: s.espCipher, encryptionKeyBits: s.espEncKeyBits, integrity: s.espInteg,
		dhGroup: childDHGroup(rekey.newDH), dhNoneOffered: rekey.offersNone, esn: s.espESN,
		tsi: tsi, tsr: tsr, localIPs: configuredInnerIPs(s),
		requireSA: true, requireNonce: true,
	})
	if err != nil {
		return 0, err
	}
	if selection.dhGroup == 0 && rekey.newDH != nil {
		// RFC 7296 3.3.6: with NONE selected the responder ignores KEi and
		// omits KEr; a KEr sent anyway is not used.
		if hasKEPayload(rekey.payloads) {
			s.Logger.Warn("CHILD_SA rekey selected D-H NONE but answered with KE; ignoring it",
				zap.String("ke_payloads", describeKEPayloads(rekey.payloads)))
			rekey.payloads = withoutKEPayloads(rekey.payloads)
		}
		rekey.newDH = nil
	}
	sharedSecret, err := childRekeySharedSecret(rekey.payloads, rekey.newDH)
	if err != nil {
		return 0, err
	}
	runtime, err := s.prepareChildSARuntime(
		childSARuntimeSpec{
			localSPI: rekey.localSPI, remoteSPI: selection.remoteSPI,
			initiatorNonce: rekey.nonce, responderNonce: selection.nonce,
			sharedSecret: sharedSecret, tsi: selection.tsi, tsr: selection.tsr,
			dh: rekey.newDH, localInitiator: true,
		},
	)
	if err != nil {
		return 0, err
	}
	if err := s.activateChildSARuntime(runtime); err != nil {
		return 0, err
	}
	installed = true
	if offeredKE != rekey.newDH {
		offeredKE.Wipe()
	}
	if err := s.deleteOldChildSA(rekey.oldRemoteSPI, rekey.oldLocalSPI); err != nil {
		logger.Warn("CHILD_SA rekey switched but old SA delete failed", zap.Error(err))
		s.recordRetiredChildSA(rekey.oldRemoteSPI, rekey.oldLocalSPI)
	} else {
		s.retireInboundChildSA(rekey.oldLocalSPI)
	}
	s.markChildRekeyComplete()
	return selection.dhGroup, nil
}

// moveESNLast keeps the IKE_AUTH transform order ENCR, INTEG, D-H, ESN.
func moveESNLast(proposal *ikev2.Proposal) {
	var esn []*ikev2.Transform
	kept := proposal.Transforms[:0]
	for _, transform := range proposal.Transforms {
		if transform != nil && transform.Type == ikev2.TransformTypeESN {
			esn = append(esn, transform)
			continue
		}
		kept = append(kept, transform)
	}
	proposal.Transforms = append(kept, esn...)
}

func withoutKEPayloads(payloads []ikev2.Payload) []ikev2.Payload {
	kept := make([]ikev2.Payload, 0, len(payloads))
	for _, payload := range payloads {
		if payload == nil || payload.Type() != ikev2.PayloadKE {
			kept = append(kept, payload)
		}
	}
	return kept
}

func hasKEPayload(payloads []ikev2.Payload) bool {
	for _, payload := range payloads {
		if payload != nil && payload.Type() == ikev2.PayloadKE {
			return true
		}
	}
	return false
}

func childRekeySharedSecret(
	payloads []ikev2.Payload,
	dh *enginecrypto.DiffieHellman,
) ([]byte, error) {
	var sharedSecret []byte
	for _, payload := range payloads {
		if payload == nil || payload.Type() != ikev2.PayloadKE {
			continue
		}
		if sharedSecret != nil {
			return nil, errors.New("swu: duplicate CHILD_SA rekey KE payload")
		}
		if dh == nil {
			return nil, errors.New("swu: CHILD_SA rekey contains unoffered KE payload")
		}
		group, peerKey, err := parseKEPayload(payload)
		if err != nil {
			return nil, err
		}
		if group != dh.Group {
			return nil, fmt.Errorf("swu: CHILD_SA rekey DH group %d does not match %d", group, dh.Group)
		}
		sharedSecret, err = dh.ComputeSharedSecret(peerKey)
		if err != nil {
			return nil, fmt.Errorf("swu: compute CHILD_SA rekey DH secret: %w", err)
		}
	}
	if dh != nil && sharedSecret == nil {
		return nil, errors.New("swu: PFS CHILD_SA rekey response missing KE payload")
	}
	return sharedSecret, nil
}

func (s *Session) newChildSAInitiatorMaterial() ([]byte, uint32, error) {
	nonce := make([]byte, s.nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, fmt.Errorf("generate CHILD_SA nonce: %w", err)
	}
	spi, err := randomChildSPI()
	if err != nil {
		return nil, 0, err
	}
	return nonce, spi, nil
}

func (s *Session) newChildSARekeyDH(group uint16) (*enginecrypto.DiffieHellman, error) {
	if group == 0 {
		return nil, nil
	}
	dh, err := enginecrypto.NewDiffieHellman(group)
	if err != nil {
		return nil, fmt.Errorf("swu: create CHILD_SA rekey PFS group %d: %w", group, err)
	}
	if err := dh.GenerateKey(); err != nil {
		return nil, fmt.Errorf("swu: generate CHILD_SA rekey PFS key: %w", err)
	}
	return dh, nil
}

func (s *Session) currentChildDHGroup() uint16 {
	s.childSAMu.RLock()
	defer s.childSAMu.RUnlock()
	return childDHGroup(s.childDH)
}

func childDHGroup(dh *enginecrypto.DiffieHellman) uint16 {
	if dh == nil {
		return 0
	}
	return dh.Group
}

func (s *Session) buildChildSARekeyRequest(localSPI uint32, nonce []byte, tsi, tsr *ikev2.EncryptedPayloadTS) *ikev2.IKEPacket {
	return &ikev2.IKEPacket{
		Header: newIKEHeader(
			s.spiI, s.spiR, ikev2.CREATE_CHILD_SA, s.localIKEFlags(false), s.nextMessageID(),
		),
		Payloads: s.buildChildSARekeyPayloads(childSARekeyRequest{
			localSPI: localSPI, nonce: nonce, tsi: tsi, tsr: tsr,
		}),
	}
}

func (s *Session) buildChildSARekeyPayloads(request childSARekeyRequest) []ikev2.Payload {
	// RFC 7296 1.3.3: REKEY_SA carries the SPI the initiator expects on
	// inbound ESP. Sending the peer's SPI gets CHILD_SA_NOT_FOUND (44).
	s.childSAMu.RLock()
	oldInboundSPI := s.espLocalSPI
	s.childSAMu.RUnlock()
	proposals := buildESPProposalsForSession(s, request.localSPI)
	groups := request.dhGroups
	if len(groups) == 0 && request.dh != nil {
		groups = []uint16{request.dh.Group}
	}
	for _, group := range groups {
		proposals[0].AddTransform(ikev2.TransformTypeDH, ikev2.AlgorithmType(group), 0)
	}
	if len(groups) > 0 {
		moveESNLast(proposals[0])
	}
	payloads := []ikev2.Payload{
		&ikev2.EncryptedPayloadSA{Proposals: proposals},
		&ikev2.EncryptedPayloadNonce{NonceData: append([]byte(nil), request.nonce...)},
	}
	if request.dh != nil {
		payloads = append(payloads, &ikev2.EncryptedPayloadKE{
			DHGroup: ikev2.AlgorithmType(request.dh.Group), KEData: request.dh.PublicKeyBytes(),
		})
	}
	return append(payloads,
		&ikev2.EncryptedPayloadNotify{
			ProtocolID: ikev2.ProtoESP,
			NotifyType: ikev2.NotifyTypeRekeySA, SPI: spiBytes(oldInboundSPI),
		},
		cloneTrafficSelectorPayload(request.tsi),
		cloneTrafficSelectorPayload(request.tsr),
	)
}

func (s *Session) exchangeEstablishedIKE(ctx context.Context, packet *ikev2.IKEPacket) ([]ikev2.Payload, error) {
	raw, err := s.encryptAndWrap(packet)
	if err != nil {
		return nil, err
	}
	responseData, err := s.exchangeEstablishedRaw(ctx, packet, [][]byte{raw})
	if err != nil {
		return nil, err
	}
	response, err := ikev2.DecodePacket(responseData)
	if err != nil {
		return nil, fmt.Errorf("swu: decode established IKE response: %w", err)
	}
	if !validIKEResponseHeader(response, packet) {
		return nil, errors.New("swu: established IKE response header mismatch")
	}
	payloads, err := s.decryptAndParse(response)
	if err != nil {
		return nil, err
	}
	if err := ikeAuthenticationError(payloads); err != nil {
		return nil, err
	}
	return payloads, nil
}

func (s *Session) currentChildSelectors() (*ikev2.EncryptedPayloadTS, *ikev2.EncryptedPayloadTS) {
	s.childSAMu.RLock()
	defer s.childSAMu.RUnlock()
	return cloneTrafficSelectorPayload(s.childTSi), cloneTrafficSelectorPayload(s.childTSr)
}

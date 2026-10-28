package swu

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	enginecrypto "github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
)

// childRekeyAnswer builds the responder payloads (before TSi/TSr) for a
// CHILD_SA rekey request; nil means answer with notify instead.
type childRekeyAnswer func(t *testing.T, session *Session, request []ikev2.Payload) []ikev2.Payload

func answerChildRekeyWith(
	t *testing.T,
	session *Session,
	transport *testIKETransport,
	answer childRekeyAnswer,
) []ikev2.Payload {
	t.Helper()
	select {
	case raw := <-transport.sentIKE:
		request, err := ikev2.DecodePacket(raw)
		if err != nil {
			t.Errorf("decode rekey request: %v", err)
			return nil
		}
		payloads, err := session.decryptAndParse(request)
		if err != nil {
			t.Errorf("decrypt rekey request: %v", err)
			return nil
		}
		response := answer(t, session, payloads)
		if response[0].Type() == ikev2.PayloadSA {
			_, _, tsi, tsr, err := collectChildSAPayloads(payloads)
			if err != nil {
				t.Errorf("collect rekey request: %v", err)
				return nil
			}
			response = append(response, cloneTrafficSelectorPayload(tsi), cloneTrafficSelectorPayload(tsr))
		}
		encoded, err := session.encryptAndWrap(&ikev2.IKEPacket{
			InitiatorSPI: request.InitiatorSPI, ResponderSPI: request.ResponderSPI,
			Version: 0x20, ExchangeType: request.ExchangeType,
			Flags: ikeResponseFlag, MessageID: request.MessageID, Payloads: response,
		})
		if err != nil {
			t.Errorf("encrypt rekey response: %v", err)
			return nil
		}
		transport.ike <- encoded
		return payloads
	case <-time.After(time.Second):
		t.Error("timed out waiting for rekey request")
		return nil
	}
}

// selectDH answers with the session suite, D-H transform group (0 = NONE)
// and, when withKE, a KEr in kePayloadGroup.
func selectDH(group uint16, withKE bool) childRekeyAnswer {
	return func(t *testing.T, session *Session, request []ikev2.Payload) []ikev2.Payload {
		proposals := buildESPProposalsForSession(session, 0xa1b2c3d4)
		proposals[0].AddTransform(ikev2.TransformTypeDH, ikev2.AlgorithmType(group), 0)
		response := []ikev2.Payload{
			&ikev2.EncryptedPayloadSA{Proposals: proposals},
			&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x92}, 32)},
		}
		if !withKE {
			return response
		}
		keGroup := group
		if keGroup == 0 {
			keGroup = requestKEGroup(t, request)
		}
		dh, err := enginecrypto.NewDiffieHellman(keGroup)
		if err != nil || dh.GenerateKey() != nil {
			t.Errorf("responder DH %d: %v", keGroup, err)
		}
		return append(response, &ikev2.EncryptedPayloadKE{DHGroup: ikev2.AlgorithmType(keGroup), KEData: dh.PublicKeyBytes()})
	}
}

func answerNotify(notifyType uint16, data []byte) childRekeyAnswer {
	return func(*testing.T, *Session, []ikev2.Payload) []ikev2.Payload {
		return []ikev2.Payload{&ikev2.EncryptedPayloadNotify{NotifyType: notifyType, NotifyData: data}}
	}
}

// omitDH answers with the session suite and no D-H transform at all.
func omitDH() childRekeyAnswer {
	return func(t *testing.T, session *Session, request []ikev2.Payload) []ikev2.Payload {
		return []ikev2.Payload{
			&ikev2.EncryptedPayloadSA{Proposals: buildESPProposalsForSession(session, 0xa1b2c3d4)},
			&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x92}, 32)},
		}
	}
}

// withDHAttribute adds a KEY_LENGTH attribute to the D-H transform of answer.
func withDHAttribute(answer childRekeyAnswer) childRekeyAnswer {
	return func(t *testing.T, session *Session, request []ikev2.Payload) []ikev2.Payload {
		response := answer(t, session, request)
		for _, transform := range response[0].(*ikev2.EncryptedPayloadSA).Proposals[0].Transforms {
			if transform.Type == ikev2.TransformTypeDH {
				transform.Attributes = []*ikev2.TransformAttribute{{Type: ikev2.AttributeKeyLength, Val: 128}}
			}
		}
		return response
	}
}

// allowNextChildRekey clears the cooldown so a test can run another rekey.
func allowNextChildRekey(session *Session) {
	session.mu.Lock()
	session.lastRekeyTime = time.Time{}
	session.mu.Unlock()
}

func requestKEGroup(t *testing.T, request []ikev2.Payload) uint16 {
	t.Helper()
	for _, payload := range request {
		if payload != nil && payload.Type() == ikev2.PayloadKE {
			group, _, err := parseKEPayload(payload)
			if err != nil {
				t.Errorf("parse KEi: %v", err)
			}
			return group
		}
	}
	return 0
}

func requestDHTransforms(request []ikev2.Payload) []uint16 {
	var groups []uint16
	for _, payload := range request {
		sa, ok := payload.(*ikev2.EncryptedPayloadSA)
		if !ok {
			continue
		}
		for _, transform := range sa.Proposals[0].Transforms {
			if transform.Type == ikev2.TransformTypeDH {
				groups = append(groups, uint16(transform.ID))
			}
		}
	}
	return groups
}

func equalGroups(got, want []uint16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// newNoPFSRekeySession is an established session whose IKE SA uses MODP1024
// and whose CHILD_SA was negotiated without PFS.
func newNoPFSRekeySession(t *testing.T) (*Session, *testIKETransport) {
	t.Helper()
	session, transport := newEstablishedControlSession(t)
	session.dhGroup = 2
	session.childDH = nil
	return session, transport
}

// RFC 7296 3.3.2: NONE among the D-H transforms makes PFS optional; 3.3.6:
// a responder taking NONE ignores KEi and omits KEr.
func TestChildSARekeyWithoutPFSOffersIKEGroupOrNone(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI := session.espLocalSPI
	done := make(chan []ikev2.Payload, 1)
	go func() {
		request := answerChildRekeyWith(t, session, transport, selectDH(0, false))
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
		done <- request
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	request := <-done
	if got := requestDHTransforms(request); !equalGroups(got, []uint16{2, 0}) {
		t.Fatalf("offered D-H transforms = %v, want [2 0]", got)
	}
	if got := requestKEGroup(t, request); got != 2 {
		t.Fatalf("KEi group = %d, want the IKE SA group 2", got)
	}
	if session.childDH != nil || len(session.childDHSecret) != 0 {
		t.Fatal("NONE selection installed PFS state")
	}
	if session.espRemoteSPI != 0xa1b2c3d4 {
		t.Fatalf("remote SPI = %08x, rekey not installed", session.espRemoteSPI)
	}
}

// RFC 7296 3.3.6: a responder selecting NONE ignores KEi, so no PFS secret
// exists even if it still sends KEr.
func TestChildSARekeyNoneSelectionInstallsWithoutPFS(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI := session.espLocalSPI
	go func() {
		answerChildRekeyWith(t, session, transport, selectDH(0, true))
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	if session.childDH != nil || len(session.childDHSecret) != 0 || session.espRemoteSPI != 0xa1b2c3d4 {
		t.Fatal("NONE selection did not install a CHILD_SA without PFS")
	}
}

// An answer to [G, NONE] that leaves the D-H transform out is taken as NONE.
func TestChildSARekeyOmittedDHAnswerInstallsWithoutPFS(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI := session.espLocalSPI
	go func() {
		answerChildRekeyWith(t, session, transport, omitDH())
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	if session.childDH != nil || len(session.childDHSecret) != 0 || session.espRemoteSPI != 0xa1b2c3d4 {
		t.Fatal("omitted D-H answer did not install a CHILD_SA without PFS")
	}
}

func TestChildSARekeyWithoutPFSInstallsSelectedGroup(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI := session.espLocalSPI
	go func() {
		answerChildRekeyWith(t, session, transport, selectDH(2, true))
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	if session.childDH == nil || session.childDH.Group != 2 || len(session.childDHSecret) == 0 {
		t.Fatal("selected group 2 did not install PFS state")
	}
}

func TestChildSARekeyRejectsInconsistentDHAnswers(t *testing.T) {
	cases := map[string]childRekeyAnswer{
		"group without KEr":    selectDH(2, false),
		"unoffered group":      selectDH(14, true),
		"group with attribute": withDHAttribute(selectDH(2, true)),
		"NONE with attribute":  withDHAttribute(selectDH(0, false)),
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			session, transport := newNoPFSRekeySession(t)
			defer stopControlTestSession(session)
			oldLocalSPI, oldRemoteSPI := session.espLocalSPI, session.espRemoteSPI
			go answerChildRekeyWith(t, session, transport, answer)
			if err := session.RekeyChildSA(); err == nil {
				t.Fatal("RekeyChildSA accepted an answer inconsistent with the offer")
			}
			if session.espLocalSPI != oldLocalSPI || session.espRemoteSPI != oldRemoteSPI {
				t.Fatal("rejected answer replaced the CHILD_SA")
			}
		})
	}
}

// RFC 7296 1.3: a responder picks its group from the offer, and every offer
// has a single non-NONE group, so INVALID_KE_PAYLOAD is not retried in a
// group we did not propose.
func TestChildSARekeyDoesNotRetryInvalidKE(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI := session.espLocalSPI
	go answerChildRekeyWith(t, session, transport, answerNotify(ikev2.INVALID_KE_PAYLOAD, []byte{0, 14}))
	err := session.RekeyChildSA()
	var rejection *createChildSARejectError
	if !errors.As(err, &rejection) || rejection.NotifyType != ikev2.INVALID_KE_PAYLOAD {
		t.Fatalf("err = %v, want INVALID_KE_PAYLOAD rejection", err)
	}
	if !strings.Contains(err.Error(), "data=000e") {
		t.Fatalf("err = %v, want the requested group in the error", err)
	}
	if session.espLocalSPI != oldLocalSPI {
		t.Fatal("rejected answer replaced the CHILD_SA")
	}
}

// RFC 7296 3.3.2 allows proposing D-H as optional or leaving it out; a
// responder that refuses one form gets the other in a new exchange.
func TestChildSARekeyFallsBackToOmittedDHAndKeepsUsingIt(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	requests := make(chan []ikev2.Payload, 3)
	go func() {
		requests <- answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
		oldLocalSPI := session.espLocalSPI
		requests <- answerChildRekeyWith(t, session, transport, omitDH())
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	first, retry := <-requests, <-requests
	if len(requestDHTransforms(first)) == 0 {
		t.Fatal("first offer left D-H out")
	}
	if len(requestDHTransforms(retry)) != 0 || requestKEGroup(t, retry) != 0 {
		t.Fatalf("retry offer D-H = %v KE = %d, want neither", requestDHTransforms(retry), requestKEGroup(t, retry))
	}
	if session.childDH != nil || session.espRemoteSPI != 0xa1b2c3d4 {
		t.Fatal("fallback rekey did not install the CHILD_SA without PFS")
	}
	if !session.childRekeyOmitDH.Load() {
		t.Fatal("the working D-H form was not kept for the next rekey")
	}
	allowNextChildRekey(session)
	next := make(chan []ikev2.Payload, 1)
	go func() {
		oldLocalSPI := session.espLocalSPI
		next <- answerChildRekeyWith(t, session, transport, omitDH())
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607081)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("next RekeyChildSA: %v", err)
	}
	if got := requestDHTransforms(<-next); len(got) != 0 {
		t.Fatalf("next rekey offered D-H %v, want the omitted form that worked", got)
	}
}

func TestChildSARekeyFallsBackToOptionalPFS(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	session.childRekeyOmitDH.Store(true)
	requests := make(chan []ikev2.Payload, 2)
	go func() {
		requests <- answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
		oldLocalSPI := session.espLocalSPI
		requests <- answerChildRekeyWith(t, session, transport, selectDH(2, true))
		respondToChildSADelete(t, session, transport, oldLocalSPI, 0x50607080)
	}()
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	first, retry := <-requests, <-requests
	if len(requestDHTransforms(first)) != 0 {
		t.Fatalf("first offer D-H = %v, want the omitted form", requestDHTransforms(first))
	}
	if got := requestDHTransforms(retry); !equalGroups(got, []uint16{2, 0}) || requestKEGroup(t, retry) != 2 {
		t.Fatalf("retry offer D-H = %v KE = %d, want [2 0] with KEi 2", got, requestKEGroup(t, retry))
	}
	if session.childDH == nil || session.childDH.Group != 2 || session.childRekeyOmitDH.Load() {
		t.Fatal("fallback to optional PFS did not install group 2 and switch the remembered form")
	}
}

// The second answer decides how main's rekey timer treats the attempt.
func TestChildSARekeyFallbackKeepsTheSecondRejection(t *testing.T) {
	for name, notify := range map[string]uint16{
		"NO_ADDITIONAL_SAS": ikev2.NO_ADDITIONAL_SAS, "CHILD_SA_NOT_FOUND": ikev2.CHILD_SA_NOT_FOUND,
	} {
		t.Run(name, func(t *testing.T) {
			session, transport := newNoPFSRekeySession(t)
			defer stopControlTestSession(session)
			session.cfg.KeepChildSAOnRekeyDecline = true
			go func() {
				answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
				answerChildRekeyWith(t, session, transport, answerNotify(notify, nil))
			}()
			err := session.RekeyChildSA()
			declined := session.childRekeyDeclined()(err)
			if declined != (notify == ikev2.NO_ADDITIONAL_SAS) || isChildSANotFoundError(err) != (notify == ikev2.CHILD_SA_NOT_FOUND) {
				t.Fatalf("err = %v classified declined=%v not_found=%v", err, declined, isChildSANotFoundError(err))
			}
		})
	}
}

func TestChildSARekeyOfferKeepsDHBeforeESN(t *testing.T) {
	session := NewSession(&Config{})
	dh, err := enginecrypto.NewDiffieHellman(2)
	if err != nil || dh.GenerateKey() != nil {
		t.Fatalf("DH: %v", err)
	}
	tsi, tsr := buildTrafficSelectorsForIPStack([]byte{10, 0, 0, 2})
	payloads := session.buildChildSARekeyPayloads(childSARekeyRequest{
		localSPI: 0x10203040, nonce: bytes.Repeat([]byte{0x41}, 32), tsi: tsi, tsr: tsr, dh: dh, dhGroups: []uint16{2, 0},
	})
	transforms := payloads[0].(*ikev2.EncryptedPayloadSA).Proposals[0].Transforms
	if last := transforms[len(transforms)-1]; last.Type != ikev2.TransformTypeESN {
		t.Fatalf("last transform type = %d, want ESN after D-H", last.Type)
	}
}

func TestChildSARekeyRefusingBothDHFormsReportsBothAttempts(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	go func() {
		answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
		answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
	}()
	err := session.RekeyChildSA()
	if !isNoProposalChosen(err) || !strings.Contains(err.Error(), "first attempt") {
		t.Fatalf("err = %v, want NO_PROPOSAL_CHOSEN naming both attempts", err)
	}
}

func TestChildSARekeyStartsWithTheFormThatLastWorked(t *testing.T) {
	session, transport := newNoPFSRekeySession(t)
	defer stopControlTestSession(session)
	session.childRekeyOmitDH.Store(true)
	first := make(chan []ikev2.Payload, 1)
	go func() {
		first <- answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
		answerChildRekeyWith(t, session, transport, answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil))
	}()
	_ = session.RekeyChildSA()
	if got := requestDHTransforms(<-first); len(got) != 0 {
		t.Fatalf("first offer D-H = %v, want the omitted form that last worked", got)
	}
}

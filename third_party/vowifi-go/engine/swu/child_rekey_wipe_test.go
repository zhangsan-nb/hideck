package swu

import (
	"bytes"
	"testing"

	"github.com/iniwex5/vowifi-go/engine/ikev2"
)

// encryptChildRekeyAnswer encrypts answer as the responder's CREATE_CHILD_SA
// response, with the session's traffic selectors after an SA answer.
func encryptChildRekeyAnswer(t *testing.T, session *Session, answer childRekeyAnswer, request []ikev2.Payload) []byte {
	t.Helper()
	response := answer(t, session, request)
	if response[0].Type() == ikev2.PayloadSA {
		tsi, tsr := session.currentChildSelectors()
		response = append(response, tsi, tsr)
	}
	encoded, err := session.encryptAndWrap(&ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.CREATE_CHILD_SA,
		Flags: ikeResponseFlag, MessageID: 1, Payloads: response,
	})
	if err != nil {
		t.Fatalf("encrypt rekey response: %v", err)
	}
	return encoded
}

// A KE that no installed CHILD_SA uses is wiped: refused offers, answers that
// do not match the offer, and a KEi the responder ignored by selecting NONE.
func TestChildSARekeyWipesUnusedKE(t *testing.T) {
	cases := map[string]struct {
		answer   childRekeyAnswer
		installs bool
	}{
		"refused":           {answer: answerNotify(ikev2.NO_PROPOSAL_CHOSEN, nil)},
		"group without KEr": {answer: selectDH(2, false)},
		"NONE selected":     {answer: selectDH(0, false), installs: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			session, transport := newNoPFSRekeySession(t)
			defer stopControlTestSession(session)
			offer := session.childRekeyDHOffer()
			ke, err := session.newChildSARekeyDH(offer.keGroup)
			if err != nil || ke == nil {
				t.Fatalf("create KE for group %d: %v", offer.keGroup, err)
			}
			nonce, localSPI, err := session.newChildSAInitiatorMaterial()
			if err != nil {
				t.Fatalf("initiator material: %v", err)
			}
			request := []ikev2.Payload{&ikev2.EncryptedPayloadKE{
				DHGroup: ikev2.AlgorithmType(offer.keGroup), KEData: ke.PublicKeyBytes(),
			}}
			data := encryptChildRekeyAnswer(t, session, tc.answer, request)
			if tc.installs {
				go respondToChildSADelete(t, session, transport, session.espLocalSPI, 0x50607080)
			}
			_, err = session.handleCreateChildSARespOffer(data, nonce, localSPI, ke, offer)
			if tc.installs != (err == nil) {
				t.Fatalf("handleCreateChildSARespOffer err = %v, want installed = %v", err, tc.installs)
			}
			if ke.PrivateKey != nil {
				t.Fatal("the unused KE kept its private key")
			}
		})
	}
}

func TestChildSARekeyKeepsTheInstalledKE(t *testing.T) {
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
	installed := session.childDH
	if installed == nil || installed.Group != 2 || len(session.childDHSecret) == 0 {
		t.Fatal("selected group 2 did not install its KE")
	}
	if installed.PrivateKey == nil || !bytes.Equal(installed.SharedKey, session.childDHSecret) {
		t.Fatal("the installed KE was wiped")
	}
}

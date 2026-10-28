package swu

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	enginecrypto "github.com/iniwex5/vowifi-go/engine/crypto"
	"github.com/iniwex5/vowifi-go/engine/ikev2"
	"github.com/iniwex5/vowifi-go/engine/ipsec"
	"go.uber.org/zap"
)

func TestEstablishedChildSARekeyValidatesResponseAndSwitchesSAs(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	const remoteSPI = uint32(0xa1b2c3d4)
	responderNonce := bytes.Repeat([]byte{0x92}, 32)
	go respondToChildSARekey(t, session, transport, remoteSPI, responderNonce)

	oldLocalSPI := session.espLocalSPI
	oldInbound := session.espInboundSA
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	if session.espLocalSPI == oldLocalSPI || session.espRemoteSPI != remoteSPI {
		t.Fatalf("rekeyed SPIs local=%08x remote=%08x", session.espLocalSPI, session.espRemoteSPI)
	}
	if !bytes.Equal(session.childNr, responderNonce) {
		t.Fatalf("responder nonce = %x", session.childNr)
	}
	if !allZero(oldInbound.EncryptionKey) || !allZero(oldInbound.IntegrityKey) {
		t.Fatal("retired inbound CHILD_SA keys were not wiped")
	}
}

func TestChildSARekeyKeepsOldInboundSAWhenDeleteTimesOut(t *testing.T) {
	session, transport := newEstablishedControlSessionWithConfig(t, &Config{Retransmit: &RetransmitConfig{
		MaxRetries: 0, InitialDelay: 50 * time.Millisecond, Backoff: 1, PollInterval: time.Millisecond,
	}})
	defer stopControlTestSession(session)
	oldLocalSPI, oldRemoteSPI := session.espLocalSPI, session.espRemoteSPI
	oldInbound := session.espInboundSA
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		respondToChildSARekeyResponse(t, session, transport, 0xa1b2c3d4, bytes.Repeat([]byte{0x92}, 32))
		receiveFragmentPacket(t, transport.sentIKE)
	}()
	err := session.RekeyChildSA()
	<-peerDone
	if err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	assertRetiredChildSA(t, session, oldRemoteSPI, oldLocalSPI, true)
	if allZero(oldInbound.EncryptionKey) {
		t.Fatal("overlap CHILD_SA was wiped before peer Delete")
	}
}

func TestEstablishedChildSARekeyPreservesPFS(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	oldDH, err := enginecrypto.NewDiffieHellman(14)
	if err != nil || oldDH.GenerateKey() != nil {
		t.Fatalf("initial child DH: %v", err)
	}
	session.childDH = oldDH
	go respondToChildSARekey(t, session, transport, 0xa1b2c3d4, bytes.Repeat([]byte{0x92}, 32))
	if err := session.RekeyChildSA(); err != nil {
		t.Fatalf("RekeyChildSA: %v", err)
	}
	if session.childDH == nil || session.childDH == oldDH || len(session.childDHSecret) == 0 {
		t.Fatal("PFS rekey did not install fresh DH state and shared secret")
	}
}

func TestPeerChildSARekeyIsValidatedAndAnswered(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	oldLocalSPI, oldRemoteSPI := session.espLocalSPI, session.espRemoteSPI
	oldInbound := session.espInboundSA

	currentTSi, currentTSr := session.currentChildSelectors()
	const peerSPI = uint32(0xb1c2d3e4)
	request := &ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.ExchangeCreateChildSA,
		MessageID: 11,
		Payloads: []ikev2.Payload{
			&ikev2.EncryptedPayloadNotify{
				ProtocolID: ikev2.ProtoESP, SPISize: 4,
				NotifyType: ikev2.NotifyTypeRekeySA, SPI: spiBytes(session.espRemoteSPI),
			},
			&ikev2.EncryptedPayloadSA{Proposals: buildESPProposalsForSession(session, peerSPI)},
			&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x83}, 32)},
			retypeTrafficSelectorPayload(currentTSr, ikev2.PayloadTSi),
			retypeTrafficSelectorPayload(currentTSi, ikev2.PayloadTSr),
		},
	}
	raw, err := session.encryptAndWrap(request)
	if err != nil {
		t.Fatalf("encrypt peer rekey: %v", err)
	}
	decoded, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode peer rekey: %v", err)
	}
	if err := session.handlePeerChildSARekey(decoded); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	// Without the Spark preset key the answered SA is used at once.
	if session.espRemoteSPI != peerSPI || pendingRemoteSPI(session) != 0 {
		t.Fatalf("sending SPI %08x pending %08x, want %08x at once",
			session.espRemoteSPI, pendingRemoteSPI(session), peerSPI)
	}
	select {
	case response := <-transport.sentIKE:
		packet, err := ikev2.DecodePacket(response)
		if err != nil || packet.Flags&(ikeInitiatorFlag|ikeResponseFlag) != ikeInitiatorFlag|ikeResponseFlag {
			t.Fatalf("peer rekey response flags=%02x err=%v", packet.Flags, err)
		}
	default:
		t.Fatal("peer rekey response was not sent")
	}
	assertRetiredChildSA(t, session, oldRemoteSPI, oldLocalSPI, true)
	request = &ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.ExchangeInformational, MessageID: 12,
		Payloads: []ikev2.Payload{&ikev2.EncryptedPayloadDelete{
			ProtocolID: ikev2.ProtoESP, SPISize: 4, NumSPIs: 1, SPIs: spiBytes(oldRemoteSPI),
		}},
	}
	raw, err = session.encryptAndWrap(request)
	if err != nil {
		t.Fatalf("encrypt peer Delete: %v", err)
	}
	decoded, err = ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode peer Delete: %v", err)
	}
	if err := session.handlePeerInformational(decoded); err != nil {
		t.Fatalf("handle peer Delete: %v", err)
	}
	assertRetiredChildSA(t, session, oldRemoteSPI, oldLocalSPI, false)
	if !allZero(oldInbound.EncryptionKey) || !allZero(oldInbound.IntegrityKey) {
		t.Fatal("peer-deleted CHILD_SA keys were not wiped")
	}
	assertChildSADeleteResponse(t, session, transport, oldLocalSPI)
}

func pendingRemoteSPI(session *Session) uint32 {
	if pending := session.currentPendingPeerRekey(); pending != nil {
		return pending.runtime.remoteSPI
	}
	return 0
}

func pendingLocalSPI(session *Session) uint32 {
	if pending := session.currentPendingPeerRekey(); pending != nil {
		return pending.runtime.localSPI
	}
	return 0
}

// sparkRekeyOffer is Spark's field offer: SHA1 without PFS, or the current
// suite with D-H 14. It returns the request and the peer's D-H key.
func sparkRekeyOffer(t *testing.T, session *Session, messageID, peerSPI uint32) (*ikev2.IKEPacket, *enginecrypto.DiffieHellman) {
	t.Helper()
	sha1 := ikev2.NewProposal(1, ikev2.ProtoESP, spiBytes(peerSPI))
	sha1.AddTransform(ikev2.TransformTypeEncr, 12, 256)
	sha1.AddTransform(ikev2.TransformTypeInteg, 2)
	sha1.AddTransform(ikev2.TransformTypeESN, 0)
	pfs := ikev2.NewProposal(2, ikev2.ProtoESP, spiBytes(peerSPI))
	pfs.AddTransform(ikev2.TransformTypeEncr, 12, 256)
	pfs.AddTransform(ikev2.TransformTypeInteg, 12)
	pfs.AddTransform(ikev2.TransformTypeDH, 14)
	pfs.AddTransform(ikev2.TransformTypeESN, 0)
	peerDH, err := enginecrypto.NewDiffieHellman(14)
	if err != nil {
		t.Fatal(err)
	}
	if err := peerDH.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	return peerChildRekeyRequestWithID(t, session, messageID, []*ikev2.Proposal{sha1, pfs},
		&ikev2.EncryptedPayloadKE{DHGroup: 14, KEData: peerDH.PublicKeyBytes()}), peerDH
}

func newSparkRekeySession(t *testing.T) (*Session, *testIKETransport) {
	t.Helper()
	session, transport := newEstablishedControlSession(t)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	// Spark picks the SHA1 suite in IKE_AUTH, so proposal #1 of its offer matches.
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 2,
	}); err != nil {
		t.Fatal(err)
	}
	session.dhGroup = 14
	session.cfg.DeferPeerChildRekeySwitch = true
	return session, transport
}

func peerESPDelete(t *testing.T, session *Session, messageID uint32, spi uint32) *ikev2.IKEPacket {
	t.Helper()
	raw, err := session.encryptAndWrap(&ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.ExchangeInformational, MessageID: messageID,
		Payloads: []ikev2.Payload{&ikev2.EncryptedPayloadDelete{
			ProtocolID: ikev2.ProtoESP, SPISize: 4, NumSPIs: 1, SPIs: spiBytes(spi),
		}},
	})
	if err != nil {
		t.Fatalf("encrypt peer Delete: %v", err)
	}
	decoded, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode peer Delete: %v", err)
	}
	return decoded
}

// Spark's field sequence: it rekeys the old SA again right after our answer.
// Sending must stay on the old SA, and the latest answer is the one kept.
func TestPeerRekeyOfOldSAAgainKeepsSendingOnOldSA(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI, oldRemoteSPI := session.espLocalSPI, session.espRemoteSPI
	first, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(first); err != nil {
		t.Fatalf("first peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	firstLocal := pendingLocalSPI(session)
	second, _ := sparkRekeyOffer(t, session, 1, 0xc1c2d3e4)
	if err := session.handlePeerChildSARekey(second); err != nil {
		t.Fatalf("second peer rekey of the old SA: %v", err)
	}
	payloads := sentPeerResponsePayloads(t, session, transport)
	if _, ok := payloads[0].(*ikev2.EncryptedPayloadSA); !ok {
		t.Fatalf("second rekey response = %s, want an accepted SA", ikePayloadTypes(payloads))
	}
	session.childSAMu.RLock()
	_, firstStillAccepted := session.espInboundSAs[firstLocal]
	session.childSAMu.RUnlock()
	if session.espRemoteSPI != oldRemoteSPI || pendingRemoteSPI(session) != 0xc1c2d3e4 || firstStillAccepted {
		t.Fatalf("sending %08x pending %08x first accepted %v, want old SA with only the second answer pending",
			session.espRemoteSPI, pendingRemoteSPI(session), firstStillAccepted)
	}
	if err := session.handlePeerInformational(peerESPDelete(t, session, 2, oldRemoteSPI)); err != nil {
		t.Fatalf("peer Delete of the old SA: %v", err)
	}
	if session.espRemoteSPI != 0xc1c2d3e4 || session.currentChildDHGroup() != 0 {
		t.Fatalf("after Delete sending %08x group %d, want c1c2d3e4 without PFS",
			session.espRemoteSPI, session.currentChildDHGroup())
	}
	assertChildSADeleteResponse(t, session, transport, oldLocalSPI)
}

// RFC 7296 2.8: a valid packet on the new SA shows the peer installed it.
func TestPeerRekeyAdoptedOnValidESPOnNewSA(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldRemoteSPI := session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	if _, _, err := session.decapsulateOuterESP([]byte{0, 0, 0, 1, 0, 0, 0, 1}); err == nil {
		t.Fatal("unknown SPI decapsulated")
	}
	if session.espRemoteSPI != oldRemoteSPI {
		t.Fatal("an invalid packet moved sending to the new SA")
	}
	pending := session.currentPendingPeerRekey()
	esp, err := ipsec.Encapsulate([]byte{0x45, 0, 0, 20}, pending.runtime.inbound)
	if err != nil {
		t.Fatal(err)
	}
	session.decapsulateOuterESP(esp)
	if session.espRemoteSPI != 0xb1c2d3e4 || pendingRemoteSPI(session) != 0 {
		t.Fatalf("after ESP on the new SA sending %08x pending %08x", session.espRemoteSPI, pendingRemoteSPI(session))
	}
	assertRetiredChildSA(t, session, oldRemoteSPI, pending.oldLocalSPI, true)
}

// The peer may delete the SA it was answered with; the old SA stays.
func TestPeerDeleteOfAnsweredSAKeepsOldSA(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldRemoteSPI := session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	newLocal := pendingLocalSPI(session)
	if err := session.handlePeerInformational(peerESPDelete(t, session, 2, 0xb1c2d3e4)); err != nil {
		t.Fatalf("peer Delete of the answered SA ended the session: %v", err)
	}
	session.childSAMu.RLock()
	_, accepted := session.espInboundSAs[newLocal]
	session.childSAMu.RUnlock()
	if session.espRemoteSPI != oldRemoteSPI || pendingRemoteSPI(session) != 0 || accepted {
		t.Fatalf("sending %08x pending %08x new accepted %v, want only the old SA",
			session.espRemoteSPI, pendingRemoteSPI(session), accepted)
	}
	assertChildSADeleteResponse(t, session, transport, newLocal)
}

// RFC 7296 2.1: a retransmitted request gets the answer already given.
func TestPeerRekeyRetransmissionResendsTheSameAnswer(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	first := sentPeerResponsePayloads(t, session, transport)
	pending := session.currentPendingPeerRekey()
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("retransmitted peer rekey: %v", err)
	}
	second := sentPeerResponsePayloads(t, session, transport)
	if session.currentPendingPeerRekey() != pending {
		t.Fatal("a retransmission replaced the pending answer")
	}
	if got, want := createChildSAOffer(second), createChildSAOffer(first); got != want {
		t.Fatalf("resent answer %s, want %s", got, want)
	}
	firstSA, _ := first[0].(*ikev2.EncryptedPayloadSA)
	secondSA, _ := second[0].(*ikev2.EncryptedPayloadSA)
	if firstSA == nil || secondSA == nil || !bytes.Equal(firstSA.Proposals[0].SPI, secondSA.Proposals[0].SPI) {
		t.Fatal("resent answer carries a different SPI")
	}
}

// ESP validated on an answer that has since been replaced must not adopt the
// replacement, which the peer may not have received yet.
func TestAdoptionRequiresTheValidatedAnswer(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldRemoteSPI := session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	session.adoptPendingPeerRekey("test", pendingLocalSPI(session)+1)
	if session.espRemoteSPI != oldRemoteSPI || pendingRemoteSPI(session) != 0xb1c2d3e4 {
		t.Fatalf("adopted on another SPI: sending %08x pending %08x", session.espRemoteSPI, pendingRemoteSPI(session))
	}
}

// An unused answer is never dropped on age: the peer may still adopt it, and a
// later Delete of the old SA must not end the session.
func TestUnusedPeerRekeyAnswerStaysAndHoldsLocalRekeys(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldLocalSPI, oldRemoteSPI := session.espLocalSPI, session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	session.childSAMu.Lock()
	session.pendingPeerRekey.answeredAt = time.Now().Add(-time.Hour)
	session.childSAMu.Unlock()
	if !session.deferLocalRekeyForPeerAnswer() {
		t.Fatal("local rekey not held back by an unused answer")
	}
	if pendingRemoteSPI(session) != 0xb1c2d3e4 || session.espRemoteSPI != oldRemoteSPI {
		t.Fatalf("pending %08x sending %08x, want the answer kept and the old SA in use",
			pendingRemoteSPI(session), session.espRemoteSPI)
	}
	if err := session.handlePeerInformational(peerESPDelete(t, session, 2, oldRemoteSPI)); err != nil {
		t.Fatalf("late peer Delete of the old SA ended the session: %v", err)
	}
	if session.espRemoteSPI != 0xb1c2d3e4 {
		t.Fatalf("after the late Delete sending %08x, want the answered SA", session.espRemoteSPI)
	}
	assertChildSADeleteResponse(t, session, transport, oldLocalSPI)
}

// RFC 7296 2.1: without deferral sending moves to the new SA at once, so a
// retransmission must get the first answer, not CHILD_SA_NOT_FOUND.
func TestImmediatePeerRekeyRetransmissionResendsTheSameAnswer(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	session.cfg.DeferPeerChildRekeySwitch = false
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	first := sentPeerResponsePayloads(t, session, transport)
	if session.espRemoteSPI != 0xb1c2d3e4 {
		t.Fatalf("sending %08x, want the new SA at once", session.espRemoteSPI)
	}
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("retransmitted peer rekey: %v", err)
	}
	second := sentPeerResponsePayloads(t, session, transport)
	if got, want := createChildSAOffer(second), createChildSAOffer(first); got != want {
		t.Fatalf("resent answer %s, want %s", got, want)
	}
	if session.espRemoteSPI != 0xb1c2d3e4 {
		t.Fatalf("retransmission changed sending to %08x", session.espRemoteSPI)
	}
}

// The resend comes before the local-rekey collision answer.
func TestPeerRekeyRetransmissionDuringLocalRekeyResendsTheSameAnswer(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handleIncomingCreateChildSAPacket(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	first := sentPeerResponsePayloads(t, session, transport)
	session.rekeyMu.Lock()
	err := session.handleIncomingCreateChildSAPacket(request)
	session.rekeyMu.Unlock()
	if err != nil {
		t.Fatalf("retransmitted peer rekey: %v", err)
	}
	second := sentPeerResponsePayloads(t, session, transport)
	if got, want := createChildSAOffer(second), createChildSAOffer(first); got != want {
		t.Fatalf("resent answer %s, want %s", got, want)
	}
}

// RFC 7296 2.1: a request older than the last answer is ignored.
func TestOlderPeerCreateChildSARequestIsIgnored(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	first, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	second, _ := sparkRekeyOffer(t, session, 1, 0xc1c2d3e4)
	for _, request := range []*ikev2.IKEPacket{first, second} {
		if err := session.handlePeerChildSARekey(request); err != nil {
			t.Fatalf("peer rekey: %v", err)
		}
		sentPeerResponsePayloads(t, session, transport)
	}
	if err := session.handlePeerChildSARekey(first); err != nil {
		t.Fatalf("old request: %v", err)
	}
	select {
	case raw := <-transport.sentIKE:
		t.Fatalf("answered an old request: %d bytes", len(raw))
	default:
	}
	if pendingRemoteSPI(session) != 0xc1c2d3e4 {
		t.Fatalf("pending %08x, want the second answer", pendingRemoteSPI(session))
	}
}

// An answer is only resent on the IKE SA it was given on.
func TestCreateChildSAAnswerIsNotResentOnAnotherIKESA(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	header := *packetIKEHeader(request)
	header.SPIr ^= 1
	if handled, _ := session.answerRetransmittedCreateChildSA(&header); handled {
		t.Fatal("answer matched a request from another IKE SA")
	}
	session.spiR[7] ^= 1
	defer func() { session.spiR[7] ^= 1 }()
	if handled, err := session.answerRetransmittedCreateChildSA(packetIKEHeader(request)); !handled || err != nil {
		t.Fatalf("retransmission after an IKE rekey handled=%v err=%v, want ignored", handled, err)
	}
	select {
	case raw := <-transport.sentIKE:
		t.Fatalf("resent an answer on the new IKE SA: %d bytes", len(raw))
	default:
	}
}

// RFC 7296 2.8: rekeying the SA we answered shows the peer installed it.
func TestPeerRekeyOfAnsweredSAAdoptsItFirst(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	next, _ := sparkRekeyOffer(t, session, 1, 0xc1c2d3e4)
	setRekeySANotifySPI(t, session, next, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(next); err != nil {
		t.Fatalf("rekey of the answered SA: %v", err)
	}
	payloads := sentPeerResponsePayloads(t, session, transport)
	if _, ok := payloads[0].(*ikev2.EncryptedPayloadSA); !ok {
		t.Fatalf("response = %s, want an accepted SA", ikePayloadTypes(payloads))
	}
	if pendingRemoteSPI(session) != 0xc1c2d3e4 || session.espRemoteSPI != 0xb1c2d3e4 {
		t.Fatalf("sending %08x pending %08x, want b1c2d3e4 adopted and c1c2d3e4 pending",
			session.espRemoteSPI, pendingRemoteSPI(session))
	}
}

// RFC 7296 2.25.1: a retired SA still exists, so it is TEMPORARY_FAILURE.
func TestPeerRekeyOfRetiredSAAnswersTemporaryFailure(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	oldRemoteSPI := session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	sentPeerResponsePayloads(t, session, transport)
	session.adoptPendingPeerRekey("test", pendingLocalSPI(session))
	next, _ := sparkRekeyOffer(t, session, 1, 0xc1c2d3e4)
	setRekeySANotifySPI(t, session, next, oldRemoteSPI)
	if err := session.handlePeerChildSARekey(next); err != nil {
		t.Fatalf("rekey of the retired SA ended the session: %v", err)
	}
	payloads := sentPeerResponsePayloads(t, session, transport)
	notify, ok := payloads[0].(*ikev2.EncryptedPayloadNotify)
	if len(payloads) != 1 || !ok || notify.NotifyType != ikev2.TEMPORARY_FAILURE {
		t.Fatalf("response = %#v, want TEMPORARY_FAILURE", payloads)
	}
}

// setRekeySANotifySPI rewrites the REKEY_SA SPI of an encrypted request.
func setRekeySANotifySPI(t *testing.T, session *Session, request *ikev2.IKEPacket, spi uint32) {
	t.Helper()
	payloads, err := session.decryptAndParse(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range payloads {
		if notify, ok := payload.(*ikev2.EncryptedPayloadNotify); ok && notify.NotifyType == ikev2.NotifyTypeRekeySA {
			notify.SPI = spiBytes(spi)
		}
	}
	raw, err := session.encryptAndWrap(&ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.CREATE_CHILD_SA, MessageID: packetIKEHeader(request).MessageID,
		Payloads: payloads,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	*request = *decoded
}

func assertChildSADeleteResponse(
	t *testing.T,
	session *Session,
	transport *testIKETransport,
	wantSPI uint32,
) {
	t.Helper()
	raw := receiveFragmentPacket(t, transport.sentIKE)
	packet, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode peer Delete response: %v", err)
	}
	payloads, err := session.decryptAndParse(packet)
	if err != nil || len(payloads) != 1 {
		t.Fatalf("peer Delete response payloads=%d err=%v", len(payloads), err)
	}
	deletion, ok := payloads[0].(*ikev2.EncryptedPayloadDelete)
	if !ok || len(deletion.SPIs) != 4 || binary.BigEndian.Uint32(deletion.SPIs) != wantSPI {
		t.Fatalf("peer Delete response = %#v, want SPI %08x", payloads[0], wantSPI)
	}
}

func assertRetiredChildSA(t *testing.T, session *Session, remoteSPI, localSPI uint32, present bool) {
	t.Helper()
	session.childSAMu.RLock()
	mapped, mappedOK := session.retiredChildSAs[localSPI]
	_, inboundOK := session.espInboundSAs[localSPI]
	session.childSAMu.RUnlock()
	if mappedOK != present || inboundOK != present || present && mapped != remoteSPI {
		t.Fatalf("retired mapping=%08x/%t inbound=%t, want present=%t", mapped, mappedOK, inboundOK, present)
	}
}

func newEstablishedControlSession(t *testing.T, loggers ...*zap.Logger) (*Session, *testIKETransport) {
	t.Helper()
	return newEstablishedControlSessionWithConfig(t, &Config{Retransmit: &RetransmitConfig{
		MaxRetries: 0, InitialDelay: 200 * time.Millisecond, Backoff: 1,
	}}, loggers...)
}

func newEstablishedControlSessionWithConfig(t *testing.T, config *Config, loggers ...*zap.Logger) (*Session, *testIKETransport) {
	t.Helper()
	session := NewSession(config, loggers...)
	transport := newTestIKETransport()
	session.socket = transport
	copy(session.spiI[:], []byte("init-spi"))
	copy(session.spiR[:], []byte("resp-spi"))
	session.ikeKeys = testIKEKeys()
	session.ikeKeys.SK_d = bytes.Repeat([]byte{0x31}, enginecrypto.PRFOutputSize(session.prf))
	session.innerIP = []byte{10, 0, 0, 2}
	session.innerPrefix = 32
	session.espLocalSPI = 0x10203040
	session.espRemoteSPI = 0x50607080
	session.childNi = bytes.Repeat([]byte{0x41}, 32)
	session.childNr = bytes.Repeat([]byte{0x42}, 32)
	session.childTSi, session.childTSr = buildTrafficSelectorsForIPStack(session.innerIP)
	if err := session.setupDataPlane(); err != nil {
		t.Fatalf("setupDataPlane: %v", err)
	}
	session.setState(stateEstablished)
	if err := session.startIKEControl(); err != nil {
		t.Fatalf("ensureIKEDispatcher: %v", err)
	}
	return session, transport
}

func respondToChildSARekey(
	t *testing.T,
	session *Session,
	transport *testIKETransport,
	remoteSPI uint32,
	responderNonce []byte,
) {
	t.Helper()
	session.childSAMu.RLock()
	oldLocalSPI := session.espLocalSPI
	session.childSAMu.RUnlock()
	oldRemoteSPI := respondToChildSARekeyResponse(t, session, transport, remoteSPI, responderNonce)
	// Our Delete names our old inbound SPI; the ePDG answers with its own.
	respondToChildSADelete(t, session, transport, oldLocalSPI, oldRemoteSPI)
}

func respondToChildSARekeyResponse(
	t *testing.T,
	session *Session,
	transport *testIKETransport,
	remoteSPI uint32,
	responderNonce []byte,
) uint32 {
	t.Helper()
	session.childSAMu.RLock()
	oldRemoteSPI := session.espRemoteSPI
	session.childSAMu.RUnlock()
	select {
	case raw := <-transport.sentIKE:
		request, err := ikev2.DecodePacket(raw)
		if err != nil {
			t.Errorf("decode rekey request: %v", err)
			return oldRemoteSPI
		}
		payloads, err := session.decryptAndParse(request)
		if err != nil {
			t.Errorf("decrypt rekey request: %v", err)
			return oldRemoteSPI
		}
		_, _, tsi, tsr, err := collectChildSAPayloads(payloads)
		if err != nil {
			t.Errorf("collect rekey request: %v", err)
			return oldRemoteSPI
		}
		responsePayloads := []ikev2.Payload{
			&ikev2.EncryptedPayloadSA{Proposals: buildESPProposalsForSession(session, remoteSPI)},
			&ikev2.EncryptedPayloadNonce{Data: append([]byte(nil), responderNonce...)},
		}
		responsePayloads, err = addChildSARekeyPFSResponse(payloads, responsePayloads)
		if err != nil {
			t.Errorf("build PFS rekey response: %v", err)
			return oldRemoteSPI
		}
		responsePayloads = append(responsePayloads, cloneTrafficSelectorPayload(tsi), cloneTrafficSelectorPayload(tsr))
		response := &ikev2.IKEPacket{
			InitiatorSPI: request.InitiatorSPI, ResponderSPI: request.ResponderSPI,
			Version: 0x20, ExchangeType: request.ExchangeType,
			Flags: ikeResponseFlag, MessageID: request.MessageID,
			Payloads: responsePayloads,
		}
		encoded, err := session.encryptAndWrap(response)
		if err != nil {
			t.Errorf("encrypt rekey response: %v", err)
			return oldRemoteSPI
		}
		transport.ike <- encoded
	case <-time.After(time.Second):
		t.Error("timed out waiting for rekey request")
	}
	return oldRemoteSPI
}

func addChildSARekeyPFSResponse(
	request []ikev2.Payload,
	response []ikev2.Payload,
) ([]ikev2.Payload, error) {
	for _, payload := range request {
		if payload == nil || payload.Type() != ikev2.PayloadKE {
			continue
		}
		group, _, err := parseKEPayload(payload)
		if err != nil {
			return nil, err
		}
		dh, err := enginecrypto.NewDiffieHellman(group)
		if err != nil {
			return nil, err
		}
		if err := dh.GenerateKey(); err != nil {
			return nil, err
		}
		sa := response[0].(*ikev2.EncryptedPayloadSA)
		sa.Proposals[0].AddTransform(ikev2.TransformTypeDH, ikev2.AlgorithmType(group), 0)
		return append(response, &ikev2.EncryptedPayloadKE{
			DHGroup: ikev2.AlgorithmType(group), KEData: dh.PublicKeyBytes(),
		}), nil
	}
	return response, nil
}

func respondToChildSADelete(
	t *testing.T,
	session *Session,
	transport *testIKETransport,
	wantRequestSPI uint32,
	responseSPI uint32,
) {
	t.Helper()
	deleteRaw := receiveFragmentPacket(t, transport.sentIKE)
	request, err := ikev2.DecodePacket(deleteRaw)
	if err != nil {
		t.Errorf("decode CHILD_SA delete request: %v", err)
		return
	}
	payloads, err := session.decryptAndParse(request)
	if err != nil || len(payloads) != 1 {
		t.Errorf("decrypt CHILD_SA delete payloads=%d err=%v", len(payloads), err)
		return
	}
	deletion, ok := payloads[0].(*ikev2.EncryptedPayloadDelete)
	if !ok || len(deletion.SPIs) != 4 || binary.BigEndian.Uint32(deletion.SPIs) != wantRequestSPI {
		t.Errorf("CHILD_SA delete = %#v, want SPI %08x", payloads[0], wantRequestSPI)
		return
	}
	response := &ikev2.IKEPacket{
		InitiatorSPI: request.InitiatorSPI, ResponderSPI: request.ResponderSPI,
		Version: 0x20, ExchangeType: request.ExchangeType,
		Flags: ikeResponseFlag, MessageID: request.MessageID,
		Payloads: childSADeleteResponse([]uint32{responseSPI}),
	}
	encoded, err := session.encryptAndWrap(response)
	if err != nil {
		t.Errorf("encrypt CHILD_SA delete response: %v", err)
		return
	}
	transport.ike <- encoded
}

func stopControlTestSession(session *Session) {
	session.cancel()
	session.controlWG.Wait()
	session.stopDataPlane()
}

func TestDPDWaitsForMatchingEmptyResponse(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	go func() {
		raw := <-transport.sentIKE
		request, _ := ikev2.DecodePacket(raw)
		response := &ikev2.IKEPacket{
			InitiatorSPI: request.InitiatorSPI, ResponderSPI: request.ResponderSPI,
			Version: 0x20, ExchangeType: request.ExchangeType,
			Flags: ikeResponseFlag, MessageID: request.MessageID,
		}
		encoded, _ := session.encryptAndWrap(response)
		transport.ike <- encoded
	}()
	if err := session.DPDProbe(); err != nil {
		t.Fatalf("DPDProbe: %v", err)
	}
}

func TestEstablishedIKESARekeySwitchesKeysAndResetsMessageID(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	oldSPI := session.spiI
	oldKeys := session.ikeKeys
	oldSKd := append([]byte(nil), session.ikeKeys.SK_d...)
	go respondToIKESARekey(t, session, transport)
	if err := session.RekeyIKESA(); err != nil {
		t.Fatalf("RekeyIKESA: %v", err)
	}
	if session.spiI == oldSPI || bytes.Equal(session.ikeKeys.SK_d, oldSKd) {
		t.Fatal("IKE SA rekey did not replace SPI and keys")
	}
	if session.nextOutboundID != 0 || !session.localIKEInitiator {
		t.Fatalf("new IKE SA message ID=%d local initiator=%t", session.nextOutboundID, session.localIKEInitiator)
	}
	for name, key := range map[string][]byte{
		"SKEYSEED": oldKeys.SKEYSEED, "SK_d": oldKeys.SK_d, "SK_ai": oldKeys.SK_ai,
		"SK_ar": oldKeys.SK_ar, "SK_ei": oldKeys.SK_ei, "SK_er": oldKeys.SK_er,
		"SK_pi": oldKeys.SK_pi, "SK_pr": oldKeys.SK_pr,
	} {
		if !allZero(key) {
			t.Fatalf("old %s was not wiped", name)
		}
	}
}

func TestInitiatedIKESARekeyRetainsOldKeysWhenDeleteTimesOut(t *testing.T) {
	session, transport := newEstablishedControlSessionWithConfig(t, &Config{Retransmit: &RetransmitConfig{
		MaxRetries: 0, InitialDelay: 50 * time.Millisecond, Backoff: 1, PollInterval: time.Millisecond,
	}})
	defer stopControlTestSession(session)
	oldSPIi, oldSPIr := session.spiI, session.spiR
	oldKeys := session.ikeKeys
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		respondToIKESARekeyResponse(t, session, transport)
		receiveFragmentPacket(t, transport.sentIKE)
	}()
	err := session.RekeyIKESA()
	<-peerDone
	if err != nil {
		t.Fatalf("RekeyIKESA: %v", err)
	}
	session.mu.RLock()
	retired := session.retiredIKESA
	retained := retired != nil && retired.spiI == oldSPIi && retired.spiR == oldSPIr &&
		retired.keys == oldKeys && !allZero(oldKeys.SK_d)
	session.mu.RUnlock()
	if !retained {
		t.Fatal("timed-out old IKE SA Delete did not retain the old protection context")
	}
}

func TestIKESARekeyDropsStaleRetiredSA(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	staleKeys := cloneIKEKeysForTest(session.ikeKeys)
	session.retiredIKESA = &ikeSAContext{keys: staleKeys, localInitiator: true}
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		respondToIKESARekey(t, session, transport)
	}()
	err := session.RekeyIKESA()
	<-peerDone
	if err != nil {
		t.Fatalf("RekeyIKESA with a stale retired SA: %v", err)
	}
	if !retiredIKEKeysCleared(session, staleKeys) {
		t.Fatal("stale retired IKE SA was not dropped and wiped")
	}
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func TestPeerIKESARekeyChangesOriginalInitiatorRole(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	oldSPIi, oldSPIr := session.spiI, session.spiR
	oldKeys := session.ikeKeys
	peerDH, err := enginecrypto.NewDiffieHellman(session.dhGroup)
	if err != nil || peerDH.GenerateKey() != nil {
		t.Fatalf("peer DH: %v", err)
	}
	var peerSPI [8]byte
	copy(peerSPI[:], []byte("peer-new"))
	proposals := buildIKEProposalsForSession(session)
	proposals[0].SPI, proposals[0].SPISize = append([]byte(nil), peerSPI[:]...), 8
	request := &ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.ExchangeCreateChildSA, MessageID: 15,
		Payloads: []ikev2.Payload{
			&ikev2.EncryptedPayloadSA{Proposals: proposals},
			&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x73}, 32)},
			&ikev2.EncryptedPayloadKE{DHGroupNum: session.dhGroup, KeyData: peerDH.PublicKeyBytes()},
		},
	}
	raw, err := session.encryptAndWrap(request)
	if err != nil {
		t.Fatalf("encrypt peer IKE rekey: %v", err)
	}
	decoded, _ := ikev2.DecodePacket(raw)
	if err := session.handleIncomingCreateChildSAPacket(decoded); err != nil {
		t.Fatalf("handle peer IKE rekey: %v", err)
	}
	if session.localIKEInitiator || session.spiI != peerSPI || session.nextOutboundID != 0 {
		t.Fatalf("peer-rekeyed role=%t SPIi=%x messageID=%d", session.localIKEInitiator, session.spiI, session.nextOutboundID)
	}
	select {
	case response := <-transport.sentIKE:
		packet, err := ikev2.DecodePacket(response)
		if err != nil || packet.Flags != ikeInitiatorFlag|ikeResponseFlag {
			t.Fatalf("peer IKE rekey response flags=%02x err=%v", packet.Flags, err)
		}
	default:
		t.Fatal("peer IKE rekey response was not sent")
	}
	session.mu.RLock()
	retained := session.retiredIKESA != nil && session.retiredIKESA.keys == oldKeys && !allZero(oldKeys.SK_d)
	session.mu.RUnlock()
	if !retained {
		t.Fatal("old IKE SA was not retained until peer Delete")
	}
	deleteKeys := cloneIKEKeysForTest(oldKeys)
	deleteRequest := &ikev2.IKEPacket{
		Header: newIKEHeader(oldSPIi, oldSPIr, ikev2.INFORMATIONAL, 0, 16),
		Payloads: []ikev2.Payload{&ikev2.EncryptedPayloadDelete{
			ProtocolID: ikev2.ProtoIKE,
		}},
	}
	raw, err = session.encryptAndWrapWithKeys(deleteRequest, 16, oldKeys)
	if err != nil {
		t.Fatalf("encrypt old IKE Delete: %v", err)
	}
	deleteHeader, err := ikev2.DecodeHeader(raw)
	if err != nil {
		t.Fatalf("decode old IKE Delete header: %v", err)
	}
	if _, retired, err := session.ikeContextForHeader(deleteHeader); err != nil || !retired {
		t.Fatalf("old IKE Delete context retired=%t err=%v header=%x/%x", retired, err, deleteHeader.SPIi, deleteHeader.SPIr)
	}
	transport.ike <- raw
	var deleteResponse []byte
	select {
	case deleteResponse = <-transport.sentIKE:
	case <-time.After(time.Second):
		t.Fatalf("old IKE Delete response timeout: %v", session.TerminalError())
	}
	responsePacket, err := ikev2.DecodePacket(deleteResponse)
	if err != nil {
		t.Fatalf("decode old IKE Delete response: %v", err)
	}
	responsePayloads, err := session.decryptAndParseWithKeys(responsePacket, deleteKeys)
	if err != nil || len(responsePayloads) != 0 {
		t.Fatalf("old IKE Delete response payloads=%d err=%v", len(responsePayloads), err)
	}
	deadline := time.Now().Add(time.Second)
	for !retiredIKEKeysCleared(session, oldKeys) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !retiredIKEKeysCleared(session, oldKeys) {
		t.Fatal("old IKE SA keys were not wiped after Delete")
	}
	transport.ike <- raw
	retransmittedResponse := receiveFragmentPacket(t, transport.sentIKE)
	if !bytes.Equal(retransmittedResponse, deleteResponse) {
		t.Fatal("retransmitted old IKE Delete did not reuse the authenticated response")
	}
}

func retiredIKEKeysCleared(session *Session, keys *IKEKeys) bool {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.retiredIKESA == nil && allZero(keys.SK_d)
}

func cloneIKEKeysForTest(keys *IKEKeys) *IKEKeys {
	return &IKEKeys{
		SKEYSEED: append([]byte(nil), keys.SKEYSEED...),
		SK_d:     append([]byte(nil), keys.SK_d...), SK_ai: append([]byte(nil), keys.SK_ai...),
		SK_ar: append([]byte(nil), keys.SK_ar...), SK_ei: append([]byte(nil), keys.SK_ei...),
		SK_er: append([]byte(nil), keys.SK_er...), SK_pi: append([]byte(nil), keys.SK_pi...),
		SK_pr: append([]byte(nil), keys.SK_pr...),
	}
}

func respondToIKESARekey(t *testing.T, session *Session, transport *testIKETransport) {
	t.Helper()
	respondToIKESARekeyResponse(t, session, transport)
	respondToOldIKESADelete(t, session, transport)
}

func respondToIKESARekeyResponse(t *testing.T, session *Session, transport *testIKETransport) {
	t.Helper()
	raw := <-transport.sentIKE
	request, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Errorf("decode IKE rekey request: %v", err)
		return
	}
	payloads, err := session.decryptAndParse(request)
	if err != nil {
		t.Errorf("decrypt IKE rekey request: %v", err)
		return
	}
	var nonce []byte
	var peerKey []byte
	for _, payload := range payloads {
		switch payload.Type() {
		case ikev2.PayloadNi:
			nonce = childSANonceData(payload)
		case ikev2.PayloadKE:
			_, peerKey, err = parseKEPayload(payload)
		}
	}
	if err != nil || len(nonce) == 0 || len(peerKey) == 0 {
		t.Errorf("parse IKE rekey request nonce=%d key=%d err=%v", len(nonce), len(peerKey), err)
		return
	}
	responderDH, _ := enginecrypto.NewDiffieHellman(session.dhGroup)
	_ = responderDH.GenerateKey()
	var responderSPI [8]byte
	copy(responderSPI[:], []byte("new-resp"))
	proposals := buildIKEProposalsForSession(session)
	proposals[0].SPI, proposals[0].SPISize = append([]byte(nil), responderSPI[:]...), 8
	response := &ikev2.IKEPacket{
		InitiatorSPI: request.InitiatorSPI, ResponderSPI: request.ResponderSPI,
		Version: 0x20, ExchangeType: request.ExchangeType,
		Flags: ikeResponseFlag, MessageID: request.MessageID,
		Payloads: []ikev2.Payload{
			&ikev2.EncryptedPayloadSA{Proposals: proposals},
			&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x74}, 32)},
			&ikev2.EncryptedPayloadKE{DHGroupNum: session.dhGroup, KeyData: responderDH.PublicKeyBytes()},
		},
	}
	encoded, _ := session.encryptAndWrap(response)
	transport.ike <- encoded

}

func respondToOldIKESADelete(t *testing.T, session *Session, transport *testIKETransport) {
	t.Helper()
	deleteRaw := <-transport.sentIKE
	deleteRequest, _ := ikev2.DecodePacket(deleteRaw)
	deleteResponse := &ikev2.IKEPacket{
		InitiatorSPI: deleteRequest.InitiatorSPI, ResponderSPI: deleteRequest.ResponderSPI,
		Version: 0x20, ExchangeType: deleteRequest.ExchangeType,
		Flags: ikeResponseFlag, MessageID: deleteRequest.MessageID,
	}
	encoded, _ := session.encryptAndWrap(deleteResponse)
	transport.ike <- encoded
}

func peerChildRekeyRequest(t *testing.T, session *Session, proposals []*ikev2.Proposal, extra ...ikev2.Payload) *ikev2.IKEPacket {
	t.Helper()
	return peerChildRekeyRequestWithID(t, session, 11, proposals, extra...)
}

func peerChildRekeyRequestWithID(
	t *testing.T, session *Session, messageID uint32, proposals []*ikev2.Proposal, extra ...ikev2.Payload,
) *ikev2.IKEPacket {
	t.Helper()
	currentTSi, currentTSr := session.currentChildSelectors()
	payloads := []ikev2.Payload{
		&ikev2.EncryptedPayloadNotify{
			ProtocolID: ikev2.ProtoESP, SPISize: 4,
			NotifyType: ikev2.NotifyTypeRekeySA, SPI: spiBytes(session.espRemoteSPI),
		},
		&ikev2.EncryptedPayloadSA{Proposals: proposals},
		&ikev2.EncryptedPayloadNonce{Data: bytes.Repeat([]byte{0x83}, 32)},
	}
	payloads = append(payloads, extra...)
	payloads = append(payloads,
		retypeTrafficSelectorPayload(currentTSr, ikev2.PayloadTSi),
		retypeTrafficSelectorPayload(currentTSi, ikev2.PayloadTSr))
	raw, err := session.encryptAndWrap(&ikev2.IKEPacket{
		InitiatorSPI: session.spiI, ResponderSPI: session.spiR,
		Version: 0x20, ExchangeType: ikev2.ExchangeCreateChildSA, MessageID: messageID,
		Payloads: payloads,
	})
	if err != nil {
		t.Fatalf("encrypt peer rekey: %v", err)
	}
	decoded, err := ikev2.DecodePacket(raw)
	if err != nil {
		t.Fatalf("decode peer rekey: %v", err)
	}
	return decoded
}

func sentPeerResponsePayloads(t *testing.T, session *Session, transport *testIKETransport) []ikev2.Payload {
	t.Helper()
	packet, err := ikev2.DecodePacket(receiveFragmentPacket(t, transport.sentIKE))
	if err != nil {
		t.Fatalf("decode peer rekey response: %v", err)
	}
	payloads, err := session.decryptAndParse(packet)
	if err != nil {
		t.Fatalf("decrypt peer rekey response: %v", err)
	}
	return payloads
}

func unsupportedESPProposal(number uint8, spi uint32) *ikev2.Proposal {
	proposal := ikev2.NewProposal(number, ikev2.ProtoESP, spiBytes(spi))
	proposal.AddTransform(ikev2.TransformTypeEncr, 3, 0) // 3DES
	proposal.AddTransform(ikev2.TransformTypeInteg, 1, 0)
	proposal.AddTransform(ikev2.TransformTypeESN, 0, 0)
	return proposal
}

// RFC 7296 2.7: the responder picks one suite from a multi-proposal offer and
// echoes the chosen proposal number (3.3.1).
func TestPeerChildSARekeySelectsMatchingProposalFromMultipleOffers(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	const peerSPI = uint32(0xb1c2d3e4)
	current := buildESPProposalsForSession(session, peerSPI)[0]
	current.ProposalNum = 2
	current.AddTransform(ikev2.TransformTypeEncr, 3, 0) // extra ENCR alternative
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{unsupportedESPProposal(1, peerSPI), current})
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	if session.espRemoteSPI != peerSPI {
		t.Fatalf("peer SPI = %08x", session.espRemoteSPI)
	}
	for _, payload := range sentPeerResponsePayloads(t, session, transport) {
		if sa, ok := payload.(*ikev2.EncryptedPayloadSA); ok {
			if len(sa.Proposals) != 1 || sa.Proposals[0].ProposalNum != 2 {
				t.Fatalf("response SA = %#v, want single proposal #2", sa.Proposals)
			}
			return
		}
	}
	t.Fatal("peer rekey response carried no SA")
}

func TestPeerChildSARekeyWithNoAcceptableProposalKeepsSA(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	oldRemoteSPI := session.espRemoteSPI
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{unsupportedESPProposal(1, 0xb1c2d3e4)})
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("unacceptable peer rekey ended the SA: %v", err)
	}
	if session.espRemoteSPI != oldRemoteSPI {
		t.Fatal("unacceptable peer rekey replaced the CHILD_SA")
	}
	payloads := sentPeerResponsePayloads(t, session, transport)
	notify, ok := payloads[0].(*ikev2.EncryptedPayloadNotify)
	if len(payloads) != 1 || !ok || notify.NotifyType != ikev2.NO_PROPOSAL_CHOSEN {
		t.Fatalf("response = %#v, want NO_PROPOSAL_CHOSEN", payloads)
	}
}

func TestPeerRekeyOfUnknownChildSAAnswersChildSANotFound(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	oldRemoteSPI := session.espRemoteSPI
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{unsupportedESPProposal(1, 0xb1c2d3e4)})
	payloads, err := session.decryptAndParse(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range payloads {
		if notify, ok := payload.(*ikev2.EncryptedPayloadNotify); ok && notify.NotifyType == ikev2.NotifyTypeRekeySA {
			notify.SPI = spiBytes(oldRemoteSPI + 1)
		}
	}
	if err := session.handlePeerChildSARekeyPayloads(request, payloads); err != nil {
		t.Fatalf("rekey of unknown CHILD_SA ended the session: %v", err)
	}
	if session.espRemoteSPI != oldRemoteSPI {
		t.Fatal("rekey of unknown CHILD_SA replaced the CHILD_SA")
	}
	response := sentPeerResponsePayloads(t, session, transport)
	notify, ok := response[0].(*ikev2.EncryptedPayloadNotify)
	if len(response) != 1 || !ok || notify.NotifyType != ikev2.CHILD_SA_NOT_FOUND {
		t.Fatalf("response = %#v, want CHILD_SA_NOT_FOUND", response)
	}
	// RFC 7296 2.25: the notify names the SA from the REKEY_SA notification.
	if notify.ProtocolID != ikev2.ProtoESP || !bytes.Equal(notify.SPI, spiBytes(oldRemoteSPI+1)) {
		t.Fatalf("CHILD_SA_NOT_FOUND protocol %d spi %x, want ESP %x", notify.ProtocolID, notify.SPI, spiBytes(oldRemoteSPI+1))
	}
}

func TestPeerRekeyKEGroupMismatchAnswersInvalidKEPayload(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{unsupportedESPProposal(1, 0xb1c2d3e4)})
	payloads := []ikev2.Payload{&ikev2.EncryptedPayloadKE{DHGroup: 19, KEData: make([]byte, 64)}}
	if _, ok, err := session.checkPeerRekeyKE(request, payloads, 14); ok || err != nil {
		t.Fatalf("checkPeerRekeyKE ok=%v err=%v, want an INVALID_KE_PAYLOAD answer", ok, err)
	}
	response := sentPeerResponsePayloads(t, session, transport)
	notify, ok := response[0].(*ikev2.EncryptedPayloadNotify)
	if len(response) != 1 || !ok || notify.NotifyType != ikev2.INVALID_KE_PAYLOAD || !bytes.Equal(notify.NotifyData, []byte{0, 14}) {
		t.Fatalf("response = %#v, want INVALID_KE_PAYLOAD naming group 14", response)
	}
	if kept, ok, err := session.checkPeerRekeyKE(request, payloads, 0); !ok || err != nil || len(kept) != 0 {
		t.Fatalf("no-PFS KE handling kept=%v ok=%v err=%v", kept, ok, err)
	}
}

// RFC 7296 1.3.3 lets a rekey carry KEi. On the SHA256 suite Spark's offer
// only matches with PFS in group 14, the IKE SA group, so it is taken.
func TestPeerChildSARekeyAddingPFSInIKEGroupIsAccepted(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 12,
	}); err != nil {
		t.Fatal(err)
	}
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	response := sentPeerResponsePayloads(t, session, transport)
	sa, ok := response[0].(*ikev2.EncryptedPayloadSA)
	if !ok || proposalDHGroup(sa.Proposals[0]) != 14 || requestKEGroup(t, response) != 14 {
		t.Fatalf("response = %#v, want the PFS proposal with KEr in group 14", response)
	}
}

// A PFS group other than the IKE SA group is not one we offer ourselves;
// the request is refused and the current SA stays.
func TestPeerChildSARekeyAddingPFSInOtherGroupIsRefused(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 12,
	}); err != nil {
		t.Fatal(err)
	}
	session.dhGroup = 2
	oldRemoteSPI := session.espRemoteSPI
	request, _ := sparkRekeyOffer(t, session, 0, 0xb1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	response := sentPeerResponsePayloads(t, session, transport)
	notify, ok := response[0].(*ikev2.EncryptedPayloadNotify)
	if len(response) != 1 || !ok || notify.NotifyType != ikev2.NO_PROPOSAL_CHOSEN {
		t.Fatalf("response = %#v, want NO_PROPOSAL_CHOSEN", response)
	}
	if session.espRemoteSPI != oldRemoteSPI || pendingRemoteSPI(session) != 0 || session.currentChildDHGroup() != 0 {
		t.Fatal("refused PFS rekey changed the CHILD_SA")
	}
}

// RFC 7296 2.8: a KE that cannot be used is refused and the SA is kept.
func TestPeerChildSARekeyWithUnusableKEKeepsSA(t *testing.T) {
	session, transport := newEstablishedControlSession(t)
	defer stopControlTestSession(session)
	session.controlMu.Lock()
	session.controlRunning = false
	session.controlMu.Unlock()
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 12,
	}); err != nil {
		t.Fatal(err)
	}
	session.dhGroup = 14
	childDH, err := enginecrypto.NewDiffieHellman(14)
	if err != nil || childDH.GenerateKey() != nil {
		t.Fatalf("child DH: %v", err)
	}
	session.childDH = childDH
	pfs := ikev2.NewProposal(1, ikev2.ProtoESP, spiBytes(0xb1c2d3e4))
	pfs.AddTransform(ikev2.TransformTypeEncr, 12, 256)
	pfs.AddTransform(ikev2.TransformTypeInteg, 12)
	pfs.AddTransform(ikev2.TransformTypeDH, 14)
	pfs.AddTransform(ikev2.TransformTypeESN, 0)
	badKE := make([]byte, 256)
	badKE[255] = 1
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{pfs},
		&ikev2.EncryptedPayloadKE{DHGroup: 14, KEData: badKE})
	oldRemote := session.espRemoteSPI
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	if session.espRemoteSPI != oldRemote || session.childDH != childDH {
		t.Fatalf("SA changed to %08x group %d", session.espRemoteSPI, session.currentChildDHGroup())
	}
	response := sentPeerResponsePayloads(t, session, transport)
	notify, ok := response[0].(*ikev2.EncryptedPayloadNotify)
	if len(response) != 1 || !ok || notify.NotifyType != ikev2.NO_PROPOSAL_CHOSEN {
		t.Fatalf("response = %#v, want NO_PROPOSAL_CHOSEN", response)
	}
}

// With SHA1 negotiated in IKE_AUTH, Spark's first proposal is the current
// suite, so the rekey is answered without PFS and without a KE.
func TestSparkRekeyOnSHA1SuitePicksFirstProposalWithoutPFS(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 2,
	}); err != nil {
		t.Fatal(err)
	}
	request, _ := sparkRekeyOffer(t, session, 0, 0xd1c2d3e4)
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("peer rekey: %v", err)
	}
	payloads := sentPeerResponsePayloads(t, session, transport)
	sa, ok := payloads[0].(*ikev2.EncryptedPayloadSA)
	if !ok || len(sa.Proposals) != 1 || sa.Proposals[0].ProposalNum != 1 {
		t.Fatalf("response = %s, want proposal 1 accepted", ikePayloadTypes(payloads))
	}
	for _, transform := range sa.Proposals[0].Transforms {
		if transform.Type == ikev2.TransformTypeDH {
			t.Fatalf("response carries D-H %d, want no PFS", transform.ID)
		}
		if transform.Type == ikev2.TransformTypeInteg && transform.ID != 2 {
			t.Fatalf("response integrity = %d, want HMAC_SHA1_96", transform.ID)
		}
	}
	for _, payload := range payloads {
		if payload.Type() == ikev2.PayloadKE {
			t.Fatalf("response carries a KE, want none")
		}
	}
	if pendingRemoteSPI(session) != 0xd1c2d3e4 || session.currentChildDHGroup() != 0 {
		t.Fatalf("pending %08x dh %d, want the SHA1 answer pending without PFS",
			pendingRemoteSPI(session), session.currentChildDHGroup())
	}
}

// RFC 7296 3.3.6: from [14, NONE] with KEi in 14 the responder may take
// either; we take 14 because KEi is in the IKE SA group.
func TestPeerChildSARekeyOfferingOptionalPFSTakesKEiGroup(t *testing.T) {
	session, transport := newSparkRekeySession(t)
	defer stopControlTestSession(session)
	if err := session.applySelectedESPAlgorithms(&childSASelection{
		encryption: 12, encryptionKeyBits: 256, integrity: 12,
	}); err != nil {
		t.Fatal(err)
	}
	optional := ikev2.NewProposal(1, ikev2.ProtoESP, spiBytes(0xb1c2d3e4))
	optional.AddTransform(ikev2.TransformTypeEncr, 12, 256)
	optional.AddTransform(ikev2.TransformTypeInteg, 12)
	optional.AddTransform(ikev2.TransformTypeDH, 14)
	optional.AddTransform(ikev2.TransformTypeDH, 0)
	optional.AddTransform(ikev2.TransformTypeESN, 0)
	peerDH, err := enginecrypto.NewDiffieHellman(14)
	if err != nil || peerDH.GenerateKey() != nil {
		t.Fatalf("peer DH: %v", err)
	}
	request := peerChildRekeyRequest(t, session, []*ikev2.Proposal{optional},
		&ikev2.EncryptedPayloadKE{DHGroup: 14, KEData: peerDH.PublicKeyBytes()})
	if err := session.handlePeerChildSARekey(request); err != nil {
		t.Fatalf("handlePeerChildSARekey: %v", err)
	}
	response := sentPeerResponsePayloads(t, session, transport)
	sa, ok := response[0].(*ikev2.EncryptedPayloadSA)
	if !ok || proposalDHGroup(sa.Proposals[0]) != 14 || requestKEGroup(t, response) != 14 {
		t.Fatalf("response = %#v, want group 14 with KEr", response)
	}
}

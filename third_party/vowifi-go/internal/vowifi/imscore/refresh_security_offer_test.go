package imscore

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/ipsec3gpp"
	"github.com/iniwex5/vowifi-go/internal/vowifi/policy"
)

// loopbackIPSecNetwork binds reservations on loopback so the refresh offer can
// reserve a real port, and counts IPsec installs and removals.
type loopbackIPSecNetwork struct {
	*SystemIMSNetwork
	installs, removals int
	oneSASet           bool
	listenErr          error
}

func (n *loopbackIPSecNetwork) KeepsTwoIPSec3GPPSets() bool { return !n.oneSASet }

func (n *loopbackIPSecNetwork) ListenTCP(*net.TCPAddr) (net.Listener, error) {
	if n.listenErr != nil {
		return nil, n.listenErr
	}
	return net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
}

func (n *loopbackIPSecNetwork) ListenProtectedUDP(addr *net.UDPAddr) (net.PacketConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: addr.Port})
}

func (n *loopbackIPSecNetwork) InstallIPSec3GPP(ipsec3gpp.Policy) error { n.installs++; return nil }
func (n *loopbackIPSecNetwork) RemoveIPSec3GPP() error                  { n.removals++; return nil }

// TS 24.229 §5.1.1.4.2: the refresh keeps Authorization (a) and
// Security-Verify (e), and its Security-Client carries new SPIs and port-c
// with port-s unchanged (d; TS 33.203 §7.4.0).
func establishedRefreshService(t *testing.T, offerNew bool) (*Service, *loopbackIPSecNetwork, *registerSession) {
	t.Helper()
	network := &loopbackIPSecNetwork{SystemIMSNetwork: NewSystemIMSNetwork(testLocalIP)}
	svc := newSecurityAgreementTestService(t, network)
	template := policy.DefaultIMSRegisterTemplate()
	template.RefreshOffersNewSecurityClient = offerNew
	svc.cfg.IMSRegisterTemplate = template
	client := securityMechanism{
		Name: "ipsec-3gpp", Auth: ipsec3gpp.AuthHMACSHA196, Encryption: ipsec3gpp.EncryptionAES,
		Protocol: ipsec3gpp.ProtocolESP, Mode: ipsec3gpp.ModeTransport,
		SPIC: 1001, SPIS: 1002, PortC: 41002, PortS: 41001,
	}
	server := &securityMechanism{Name: "ipsec-3gpp", SPIC: 2001, SPIS: 2002, PortC: 16381, PortS: 15060}
	session := &registerSession{
		callID: "refresh", fromTag: "tag", contactUser: "contact", cseq: 3,
		authHeader: `Digest username="user@ims.example", realm="ims.example", nonce="abc", response="def"`,
		expires:    time.Hour, template: template,
		security: &securityAgreement{
			client: client, server: server, clientHeader: buildTemplateSecurityClient(client, template),
			verifyHeader: "ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;spi-c=2001;spi-s=2002;port-c=16381;port-s=15060",
		},
	}
	svc.mu.Lock()
	svc.regSession = session
	svc.mu.Unlock()
	return svc, network, session
}

func TestRefreshOffersNewSecurityClientAndKeepsActiveSAs(t *testing.T) {
	svc, network, previous := establishedRefreshService(t, true)
	var sent []string
	var offers []*refreshSecurityOffer
	captureRegisterRequests(svc, func(request string, _ int) *sipResponse {
		sent = append(sent, request)
		svc.mu.RLock()
		offer := svc.regSession.refreshOffer
		svc.mu.RUnlock()
		offers = append(offers, offer)
		if offer != nil {
			if probe, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(int(offer.client.PortC))); err == nil {
				probe.Close()
				t.Errorf("port-c %d was not reserved while the refresh was in flight", offer.client.PortC)
			}
		}
		return registerResponseForRequest(request, 200, nil)
	})
	for range 2 {
		if _, _, err := svc.runRegisterAttempt(context.Background()); err != nil {
			t.Fatalf("refresh: %v", err)
		}
	}
	if len(sent) != 2 || len(offers) != 2 || offers[0] == nil || offers[1] == nil {
		t.Fatalf("sent %d refreshes with offers %v", len(sent), offers)
	}
	active := previous.security.client
	seenSPIs := map[uint32]bool{active.SPIC: true, active.SPIS: true,
		previous.security.server.SPIC: true, previous.security.server.SPIS: true}
	for index, request := range sent {
		offer := offers[index]
		if _, err := offer.tcp.Accept(); err == nil {
			t.Fatalf("refresh %d kept its TCP port-c reservation", index)
		}
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(offer.client.PortC)})
		if err != nil {
			t.Fatalf("refresh %d kept its UDP port-c reservation: %v", index, err)
		}
		udp.Close()
		if offer.client.PortS != active.PortS || offer.client.PortC == active.PortC {
			t.Fatalf("refresh %d offered port-c %d port-s %d", index, offer.client.PortC, offer.client.PortS)
		}
		if seenSPIs[offer.client.SPIC] || seenSPIs[offer.client.SPIS] || offer.client.SPIC == offer.client.SPIS {
			t.Fatalf("refresh %d reused an active SPI: %d/%d", index, offer.client.SPIC, offer.client.SPIS)
		}
		if got := sipHeaderValue(request, "Security-Client"); got != offer.header || got == previous.security.clientHeader {
			t.Fatalf("refresh %d Security-Client = %q", index, got)
		}
		if got := sipHeaderValue(request, "Security-Verify"); got != previous.security.verifyHeader {
			t.Fatalf("refresh %d Security-Verify = %q", index, got)
		}
		if got := sipHeaderValue(request, "Authorization"); got != previous.authHeader {
			t.Fatalf("refresh %d Authorization = %q", index, got)
		}
	}
	if network.installs != 0 || network.removals != 0 {
		t.Fatalf("refresh touched IPsec: installs %d removals %d", network.installs, network.removals)
	}
}

// TS 24.229 5.1.1.5.1 item 3: a Security-Server without the parameters for
// the new SAs ends the authentication. Refusing an unchanged port_pc or a
// moved port_ps (TS 33.203 7.4.0 obligations of the P-CSCF), checking before
// AKA, and keeping the active SAs and binding rather than sending a new
// REGISTER with a new Call-ID are our choices.
func TestChallengedRefreshIsRefusedBeforeAKA(t *testing.T) {
	for _, test := range []struct{ name, securityServer string }{
		{"malformed", "malformed"},
		{"P-CSCF kept port-c", strings.Replace(switchedSecurityServer, "port-c=16382", "port-c=16381", 1)},
		{"P-CSCF moved port-s", strings.Replace(switchedSecurityServer, "port-s=15060", "port-s=15062", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, network, previous, _, sm1 := switchRefreshService(t, 200, test.securityServer)
			_, _, err := svc.runRegisterAttempt(context.Background())
			var failed *refreshSwitchFailed
			if !errors.As(err, &failed) || failed.phase != "negotiate" {
				t.Fatalf("err = %v", err)
			}
			if added, aborts, commits, _ := network.counts(); len(*sm1) != 1 || added != 0 || aborts != 0 || commits != 0 {
				t.Fatalf("sent %d added %d aborts %d commits %d", len(*sm1), added, aborts, commits)
			}
			svc.mu.RLock()
			session := svc.regSession
			svc.mu.RUnlock()
			if session.challenge != previous.challenge || session.authHeader != previous.authHeader ||
				session.security != previous.security {
				t.Fatal("a refused challenge was answered or changed the session")
			}
			if session.refreshOffer == nil || !session.refreshOffer.released {
				t.Fatal("offer not released")
			}
			if !svc.keepRegistrationAfterFailedRefresh(true, err) {
				t.Fatalf("a refused challenge dropped the binding: %v", err)
			}
		})
	}
}

func TestRefreshKeepsActiveSecurityClientWithoutTwoSASets(t *testing.T) {
	for _, test := range []struct {
		name     string
		oneSet   bool
		external bool
	}{
		{name: "one SA set", oneSet: true},
		{name: "external transport", external: true},
		{name: "port-c reservation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, network, session := establishedRefreshService(t, true)
			network.oneSASet = test.oneSet
			if !test.oneSet && !test.external {
				network.listenErr = errors.New("no port")
			}
			svc.mu.Lock()
			svc.externalTransport = test.external
			svc.mu.Unlock()
			active := session.security.clientHeader
			svc.attachRefreshSecurityOffer(session)
			if session.refreshOffer != nil || session.gatedRefresh {
				t.Fatalf("offer = %+v gated = %t, want the refresh as on main", session.refreshOffer, session.gatedRefresh)
			}
			if session.security.clientHeader != active {
				t.Fatalf("Security-Client changed to %q", session.security.clientHeader)
			}
		})
	}
}

func TestRefreshWithoutPresetKeyReusesActiveSecurityClient(t *testing.T) {
	svc, _, previous := establishedRefreshService(t, false)
	var request string
	captureRegisterRequests(svc, func(sent string, _ int) *sipResponse {
		request = sent
		return registerResponseForRequest(sent, 200, nil)
	})
	_, _, _ = svc.runRegisterAttempt(context.Background())
	if got := sipHeaderValue(request, "Security-Client"); got != previous.security.clientHeader {
		t.Fatalf("key off changed Security-Client: %q", got)
	}
}

func TestDeclinedRefreshChallengeKeepsBinding(t *testing.T) {
	svc, _, _ := establishedRefreshService(t, true)
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	svc.mu.Lock()
	svc.registrationTransport, svc.registrationTCP = "tcp", registration
	svc.mu.Unlock()
	if !svc.keepRegistrationAfterFailedRefresh(true, &refreshChallengeDeclined{status: 401}) {
		t.Fatal("a declined refresh challenge dropped the binding (TS 24.229 5.4.1.2.3 NOTE 2)")
	}
	if svc.keepRegistrationAfterFailedRefresh(false, &refreshChallengeDeclined{status: 401}) {
		t.Fatal("kept a binding that did not exist")
	}
}

func TestDeclinedRefreshChallengeKeepsRegistrationThroughRegister(t *testing.T) {
	svc, network, _ := establishedRefreshService(t, true)
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	svc.mu.Lock()
	svc.registrationTransport, svc.registrationTCP, svc.registrationTCPProtected = "tcp", registration, true
	svc.regState, svc.signalingReady = regRegistered, true
	svc.lastRegisterOKAt = time.Now().Add(-time.Hour)
	svc.mu.Unlock()
	captureRegisterRequests(svc, func(request string, _ int) *sipResponse {
		return akaChallengeResponse(request, securityServerOffer("hmac-sha-1-96", "aes-cbc", "q=1"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Register(ctx); err != nil {
		t.Fatalf("Register after a declined refresh challenge: %v", err)
	}
	if svc.RegState() != regRegistered || network.installs != 0 || network.removals != 0 {
		t.Fatalf("state %v installs %d removals %d", svc.RegState(), network.installs, network.removals)
	}
	svc.mu.RLock()
	next := svc.regSession
	svc.mu.RUnlock()
	if next == nil || next.expires <= 0 {
		t.Fatal("the next attempt would not be a refresh")
	}
}

func TestOutboundCompletionIsNotARefreshOffer(t *testing.T) {
	svc, _, previous := establishedRefreshService(t, true)
	svc.mu.Lock()
	svc.sipOutbound, svc.outboundContactRegistered = true, false
	svc.mu.Unlock()
	var request string
	captureRegisterRequests(svc, func(sent string, _ int) *sipResponse {
		request = sent
		return registerResponseForRequest(sent, 200, nil)
	})
	_, _, _ = svc.runRegisterAttempt(context.Background())
	if got := sipHeaderValue(request, "Security-Client"); got != previous.security.clientHeader {
		t.Fatalf("outbound completion offered new parameters: %q", got)
	}
}

// TS 33.203 §7.4.0: a challenge without Security-Server on a protected refresh
// is answered over the active SAs, which the existing reuse path already does.
func TestRefreshChallengeWithoutSecurityServerIsAnsweredOverActiveSAs(t *testing.T) {
	svc, network, previous := establishedRefreshService(t, true)
	registration, peer := net.Pipe()
	t.Cleanup(func() { registration.Close(); peer.Close() })
	svc.mu.Lock()
	svc.registrationTCP, svc.registrationTCPProtected, svc.signalingReady = registration, true, true
	svc.mu.Unlock()
	var sent []string
	captureRegisterRequests(svc, func(request string, number int) *sipResponse {
		sent = append(sent, request)
		if number == 1 {
			return akaChallengeResponse(request, "")
		}
		return registerResponseForRequest(request, 200, nil)
	})
	if _, _, err := svc.runRegisterAttempt(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(sent) != 2 || network.installs != 0 || network.removals != 0 {
		t.Fatalf("sent %d installs %d removals %d", len(sent), network.installs, network.removals)
	}
	if sipHeaderValue(sent[1], "Security-Client") != sipHeaderValue(sent[0], "Security-Client") ||
		sipHeaderValue(sent[1], "Security-Client") == previous.security.clientHeader {
		t.Fatal("the answer to the challenge changed or dropped the offered Security-Client")
	}
}

// The initial-reject fallback still runs on a refresh with an offer. It
// rewrites the offered Security-Client and leaves the active SAs' own.
func TestRefreshInitialRejectFallbackRewritesOnlyTheOffer(t *testing.T) {
	svc, _, previous := establishedRefreshService(t, true)
	template := svc.cfg.IMSRegisterTemplate
	template.EnableInitialRejectFallback = true
	template.SecurityClientIncludesServerParams = true
	template.FallbackIncludesServerParamsInSecCl = false
	svc.cfg.IMSRegisterTemplate = template
	previous.template = template
	active := previous.security.clientHeader
	var sent []string
	captureRegisterRequests(svc, func(request string, _ int) *sipResponse {
		sent = append(sent, request)
		if len(sent) == 1 {
			return registerResponseForRequest(request, 400, nil)
		}
		return registerResponseForRequest(request, 200, nil)
	})
	if _, _, err := svc.runRegisterAttempt(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d REGISTERs, want the fallback retry", len(sent))
	}
	first, retried := sipHeaderValue(sent[0], "Security-Client"), sipHeaderValue(sent[1], "Security-Client")
	if first == retried || first == active || retried == active {
		t.Fatalf("Security-Client first %q retried %q active %q", first, retried, active)
	}
	svc.mu.RLock()
	session := svc.regSession
	svc.mu.RUnlock()
	if session.security.clientHeader != active {
		t.Fatalf("fallback rewrote the active Security-Client to %q", session.security.clientHeader)
	}
	if !strings.Contains(retried, "port-c="+strconv.Itoa(int(session.refreshOffer.client.PortC))) {
		t.Fatalf("fallback dropped the offered port-c: %q", retried)
	}
}

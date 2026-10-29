package imscore

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

// TS 24.229 §5.1.1.4.2 d): a refresh REGISTER sent over established SAs
// carries a Security-Client "set to specify ... the new parameter values
// needed for the setup of two new pairs of security associations", and a
// P-CSCF expects them (§5.2.2.2 b). TS 33.203 §7.4.0 keeps port_us and
// changes port_uc when SAs are replaced. The refresh keeps sending over the
// old SAs (§7.4.1a); the offer only matters if the S-CSCF re-authenticates.
//
// A challenge with a Security-Server switches to new SAs built from this
// offer (refresh_security_switch.go). A second challenge during the switch
// is declined before any AKA processing and the existing binding, SAs and
// flow are kept: an S-CSCF whose challenge goes unanswered leaves an already
// registered identity registered (§5.4.1.2.3 NOTE 2). Keeping them instead
// of a new REGISTER with a new Call-ID (§5.1.1.5.1) is our choice.
type refreshSecurityOffer struct {
	client   securityMechanism
	header   string
	tcp      net.Listener
	udp      net.PacketConn
	released bool
}

// refreshChallengeDeclined marks a re-authentication of a refresh that is not
// answered. It keeps the received status and Security-Server.
type refreshChallengeDeclined struct {
	status         int
	securityServer string
}

func (err *refreshChallengeDeclined) Error() string {
	return fmt.Sprintf("imscore: refresh REGISTER challenged with status %d (Security-Server %q) declined; the active SAs are kept",
		err.status, err.securityServer)
}

func isRefreshChallengeDeclined(err error) bool {
	var declined *refreshChallengeDeclined
	return errors.As(err, &declined)
}

func refreshOffersNewSecurityClient(session *registerSession) bool {
	return session != nil && session.template.RefreshOffersNewSecurityClient &&
		session.security != nil && session.security.server != nil
}

// prepareRefreshSecurityOffer reserves a new protected client port on both
// protocols and draws SPIs that no active SA uses. port_us stays as it is.
func (s *Service) prepareRefreshSecurityOffer(session *registerSession) (*refreshSecurityOffer, error) {
	active := session.security.client
	server := session.security.server
	if s.cfg == nil || s.cfg.IMSNetwork == nil {
		return nil, errors.New("imscore: refresh offer needs the IMS network")
	}
	excludedPorts := map[int]struct{}{int(active.PortC): {}, int(active.PortS): {}, 5060: {}, 5061: {}}
	udpNetwork, protectedUDP := s.cfg.IMSNetwork.(interface {
		ListenProtectedUDP(*net.UDPAddr) (net.PacketConn, error)
	})
	var listener net.Listener
	var udp net.PacketConn
	var lastErr error
	for attempt := 0; attempt < 4 && listener == nil; attempt++ {
		candidate, err := s.cfg.IMSNetwork.ListenTCP(&net.TCPAddr{IP: s.cfg.LocalIP})
		if err != nil {
			return nil, fmt.Errorf("imscore: reserve refresh port-c: %w", err)
		}
		// Rejected candidates stay bound until return so the next pick differs.
		port := tcpPort(candidate.Addr())
		if _, used := excludedPorts[port]; used {
			defer candidate.Close()
			continue
		}
		if protectedUDP {
			conn, err := udpNetwork.ListenProtectedUDP(&net.UDPAddr{IP: s.cfg.LocalIP, Port: port})
			if err != nil {
				lastErr = fmt.Errorf("imscore: reserve refresh UDP port-c %d: %w", port, err)
				defer candidate.Close()
				continue
			}
			udp = conn
		}
		listener = candidate
	}
	if listener == nil {
		return nil, errors.Join(errors.New("imscore: no unused port for the refresh port-c"), lastErr)
	}
	port := tcpPort(listener.Addr())
	excludedSPIs := map[uint32]struct{}{active.SPIC: {}, active.SPIS: {}, server.SPIC: {}, server.SPIS: {}}
	spiC, err := randomUint32(excludedSPIs)
	if err == nil {
		excludedSPIs[spiC] = struct{}{}
	}
	spiS, spiErr := randomUint32(excludedSPIs)
	if err = errors.Join(err, spiErr); err != nil {
		closeRefreshReservations(listener, udp)
		return nil, err
	}
	client := active
	client.Raw = ""
	client.SPIC, client.SPIS, client.PortC = spiC, spiS, uint16(port)
	return &refreshSecurityOffer{
		client: client, header: buildTemplateSecurityClient(client, session.template), tcp: listener, udp: udp,
	}, nil
}

func closeRefreshReservations(listener net.Listener, udp net.PacketConn) error {
	var err error
	if listener != nil {
		err = listener.Close()
	}
	if udp != nil {
		err = errors.Join(err, udp.Close())
	}
	return err
}

// keepsTwoIPSec3GPPSets reports an IMS network that can hold a new SA set
// beside the active one (TS 33.203 7.4.1a).
func (s *Service) keepsTwoIPSec3GPPSets() bool {
	if s == nil || s.cfg == nil || s.cfg.IMSNetwork == nil {
		return false
	}
	network, ok := s.cfg.IMSNetwork.(interface{ KeepsTwoIPSec3GPPSets() bool })
	return ok && network.KeepsTwoIPSec3GPPSets()
}

// attachRefreshSecurityOffer runs once per refresh attempt, outside mu.
func (s *Service) attachRefreshSecurityOffer(session *registerSession) {
	// The REGISTER that completes an outbound flow right after the initial
	// registration is not a refresh of a confirmed binding.
	if !refreshOffersNewSecurityClient(session) || s.needsOutboundBindingRefresh() {
		return
	}
	// New parameters invite a re-authentication that needs both SA sets at
	// once; without that the refresh stays as on main.
	s.mu.RLock()
	external := s.externalTransport
	s.mu.RUnlock()
	if ok := s.keepsTwoIPSec3GPPSets(); !ok || external {
		logging.WarnRate("ims-refresh-offer-unsupported-"+s.DeviceID(), time.Hour,
			"IMS refresh Security-Client offer skipped; this registration cannot keep two SA sets",
			"device", s.DeviceID(), "cseq", session.cseq, "two_sa_sets", ok, "external_transport", external)
		return
	}
	offer, err := s.prepareRefreshSecurityOffer(session)
	if err != nil {
		// Without an offer the refresh, and any challenge to it, stays as on main.
		logging.WarnRate("ims-refresh-offer-skipped-"+s.DeviceID(), 30*time.Second,
			"IMS refresh Security-Client offer skipped; reusing the active parameters",
			"device", s.DeviceID(), "cseq", session.cseq, "err", err)
		return
	}
	session.gatedRefresh = true
	session.refreshOffer = offer
	active := session.security.client
	logging.Info("IMS refresh Security-Client offers new parameters",
		"device", s.DeviceID(), "cseq", session.cseq,
		"active_port_c", active.PortC, "offered_port_c", offer.client.PortC, "port_s", active.PortS,
		"active_spi_c", active.SPIC, "offered_spi_c", offer.client.SPIC,
		"active_spi_s", active.SPIS, "offered_spi_s", offer.client.SPIS)
}

// releaseRefreshSecurityOffer is idempotent. The offer's SAs are never
// installed, so only its port reservations are released.
func (s *Service) releaseRefreshSecurityOffer(session *registerSession, result registerAttemptResult, attemptErr error) {
	if session == nil || !session.gatedRefresh {
		return
	}
	switchAttempted := session.switching != nil
	s.abortRefreshSecuritySwitch(session, attemptErr)
	offer := session.refreshOffer
	var closeErr error
	if offer != nil && !offer.released {
		offer.released = true
		closeErr = closeRefreshReservations(offer.tcp, offer.udp)
	}
	disposition := "accepted_without_challenge"
	var switchFailed *refreshSwitchFailed
	switch {
	case switchAttempted && session.switching != nil:
		// Committed; a later failure is reported in err.
		disposition = "switched"
	case errors.As(attemptErr, &switchFailed):
		disposition = "switch_failed_" + switchFailed.phase
	case switchAttempted:
		disposition = "switch_aborted"
	case isRefreshChallengeDeclined(attemptErr):
		disposition = "challenge_declined"
	case attemptErr != nil:
		disposition = "refresh_failed"
	}
	logging.Info("IMS refresh Security-Client offer released",
		"device", s.DeviceID(), "cseq", session.cseq, "offered", offer != nil,
		"status", result.statusCode, "disposition", disposition, "switch_attempted", switchAttempted || switchFailed != nil,
		"err", errorString(attemptErr), "close_err", errorString(closeErr))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

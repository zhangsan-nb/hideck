package imscore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

const defaultSIPPort = 5060

func registerTransportDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if ctx == nil {
		return deadline
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func (s *Service) ensureRegistrationTransport(ctx context.Context) error {
	if s.transport == nil {
		return errors.New("imscore: no SIP transport")
	}
	if reconnect, client, server := s.protectedReconnectParameters(); reconnect {
		return s.dialProtectedRegistrationTCP(ctx, client, server)
	}
	if s.transport.hasSendFn() {
		candidates := registerTransportCandidates(s.cfg.Transport)
		s.mu.Lock()
		if s.registrationIO == nil && s.registrationTCP == nil {
			s.externalTransport = true
			s.registrationTransport = candidates[0]
		}
		s.mu.Unlock()
		return nil
	}
	if s.cfg.IMSNetwork == nil {
		return errors.New("imscore: no IMS network")
	}
	serverListener, clientReservation, err := s.reserveProtectedTCPPorts()
	if err != nil {
		return err
	}
	if err := s.openInitialRegistrationTransport(ctx, serverListener, clientReservation); err != nil {
		closeRegistrationReservations(serverListener, clientReservation)
		return err
	}
	return nil
}

func (s *Service) reserveProtectedTCPPorts() (net.Listener, net.Listener, error) {
	if effectiveSecAgreeMode(s.cfg, resolveActiveIMSRegisterTemplate(s.cfg)) == "disabled" {
		return nil, nil, nil
	}
	server, err := s.cfg.IMSNetwork.ListenTCP(&net.TCPAddr{IP: s.cfg.LocalIP})
	if err != nil {
		return nil, nil, fmt.Errorf("imscore: reserve protected server port: %w", err)
	}
	configuredServer, err := applyIPSec3GPPPortSListenerTCPMSSWithError(server)
	if err != nil {
		_ = server.Close()
		return nil, nil, err
	}
	server = configuredServer
	client, err := s.cfg.IMSNetwork.ListenTCP(&net.TCPAddr{IP: s.cfg.LocalIP})
	if err != nil {
		_ = server.Close()
		return nil, nil, fmt.Errorf("imscore: reserve protected client port: %w", err)
	}
	reserved, err := s.reserveProtectedUDPPorts(server, client)
	if err != nil {
		_ = server.Close()
		_ = client.Close()
		return nil, nil, err
	}
	return reserved, client, nil
}

func tcpPort(address net.Addr) int {
	if tcpAddress, ok := address.(*net.TCPAddr); ok {
		return tcpAddress.Port
	}
	return 0
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func (s *Service) currentRegistrationRemote() *net.UDPAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneUDPAddr(s.registrationRemote)
}

func (s *Service) setProtectedRegistrarPort(port uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registrationRemote == nil {
		return errors.New("imscore: registrar address is unavailable")
	}
	s.registrationRemote.Port = int(port)
	return nil
}

func (s *Service) connectProtectedRegistrationTCP(ctx context.Context, client, server securityMechanism) error {
	s.mu.Lock()
	reservation := s.clientPortReserve
	s.clientPortReserve = nil
	s.mu.Unlock()
	if reservation == nil {
		return errors.New("imscore: protected client port was not reserved")
	}
	if err := reservation.Close(); err != nil {
		return fmt.Errorf("imscore: release protected client port: %w", err)
	}
	return s.dialProtectedRegistrationTCP(ctx, client, server)
}

func (s *Service) dialProtectedRegistrationTCP(ctx context.Context, client, server securityMechanism) error {
	conn, err := s.dialProtectedTCP(ctx, client, server)
	if err != nil {
		return err
	}
	s.activateProtectedRegistrationTCP(conn)
	return nil
}

// dialProtectedTCP opens the flow port_uc -> port_ps without publishing it.
func (s *Service) dialProtectedTCP(ctx context.Context, client, server securityMechanism) (net.Conn, error) {
	registrationRemote := s.currentRegistrationRemote()
	if registrationRemote == nil || registrationRemote.IP == nil {
		return nil, errors.New("imscore: registrar IP unavailable for protected TCP")
	}
	local := &net.TCPAddr{IP: s.cfg.LocalIP, Port: int(client.PortC)}
	remote := &net.TCPAddr{IP: registrationRemote.IP, Port: int(server.PortS)}
	conn, attempts, err := dialSecureChannelWithFallback(func(network string) (net.Conn, error) {
		if !ipsec3gppTCPNetwork(network) {
			return nil, fmt.Errorf("imscore: unsupported secure signaling network %q", network)
		}
		return s.cfg.IMSNetwork.DialTCPContext(ctx, local, remote)
	})
	logSecureChannelAttemptResult(attempts, err)
	if err != nil {
		return nil, err
	}
	if err := setIPSec3GPPTCPMSS(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imscore: set protected REGISTER TCP MSS: %w", err)
	}
	logging.Info("IMS protected REGISTER TCP connected",
		"local_port", local.Port, "remote_port", remote.Port)
	logSecureChannelEstablished(conn)
	return conn, nil
}

func (s *Service) protectedReconnectParameters() (bool, securityMechanism, securityMechanism) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.externalTransport || (s.registrationTCP != nil && s.registrationTCPProtected) || s.regSession == nil || s.regSession.security == nil || s.regSession.security.server == nil {
		return false, securityMechanism{}, securityMechanism{}
	}
	return true, s.regSession.security.client, *s.regSession.security.server
}

func (s *Service) protectedTransportState() (external, connected bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.externalTransport, s.registrationTCP != nil && s.registrationTCPProtected
}

func (s *Service) activateProtectedRegistrationTCP(conn net.Conn) {
	s.configureRegistrationTCPKeepalive(conn)
	conn = s.newInboundCountingConn(conn)
	s.mu.Lock()
	previous := s.registrationTCP
	s.registrationTCP = conn
	if previous != nil && previous != conn {
		s.registrationPreviousTCP = previous
	}
	s.registrationTCPProtected = true
	s.registrationTransport = "tcp"
	s.transport.SetSendFn(func(request string) error {
		return s.writeSIPStream(conn, request)
	})
	s.mu.Unlock()
	s.networkDone.Add(1)
	go s.readRegistrationStream(conn)
}

func (s *Service) finalizeRegistrationTransportSwitch() {
	s.mu.Lock()
	previous := s.registrationPreviousTCP
	s.registrationPreviousTCP = nil
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

func (s *Service) readRegistrationResponses(conn net.PacketConn) {
	defer s.networkDone.Done()
	s.receiverStarted()
	defer s.receiverStopped()
	buffer := make([]byte, 64*1024)
	for {
		n, remote, err := conn.ReadFrom(buffer)
		if err != nil {
			s.handleRegistrationPacketReadError(conn, err)
			return
		}
		if isSTUNMessage(buffer[:n]) {
			s.handleInboundSTUN(append([]byte(nil), buffer[:n]...))
			continue
		}
		err = s.dispatchInboundSIP(string(buffer[:n]), func(response string) error {
			if _, writeErr := conn.WriteTo([]byte(response), remote); writeErr != nil {
				return fmt.Errorf("imscore: write SIP datagram: %w", writeErr)
			}
			return nil
		})
		if err != nil {
			logging.WarnRate("ims-udp-inbound", "IMS UDP inbound handling failed", "err", err)
		}
	}
}

func (s *Service) handleRegistrationPacketReadError(conn net.PacketConn, readErr error) {
	if s.stopped() {
		return
	}
	err := fmt.Errorf("imscore: registration SIP packet socket closed: %w", readErr)
	if !s.markRegistrationPacketSignalingDead(conn, err) {
		return
	}
	logging.WarnRate("ims-registration-packet", "IMS registration SIP packet socket closed", "err", err)
}

func (s *Service) acceptProtectedSIP(listener net.Listener) {
	var acceptErr error
	defer func() {
		// StopCurrent may be waiting for receivers while REGISTER owns its
		// lock. Retire this receiver before entering serialized recovery.
		s.networkDone.Done()
		if acceptErr != nil {
			s.handleProtectedListenerFailure(listener, acceptErr)
		}
	}()
	s.receiverStarted()
	defer s.receiverStopped()
	for {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr = err
			return
		}
		logging.Info("IPSec portS accepted server push connection",
			"device", s.DeviceID(), "remote", conn.RemoteAddr(), "local", conn.LocalAddr())
		s.inboundTCPAccept.Add(1)
		s.configureRegistrationTCPKeepalive(conn)
		conn = s.newPortSInboundConn(conn)
		if !s.trackProtectedPortSConnection(conn, time.Now()) {
			_ = conn.Close()
			return
		}
		s.resetPortSReadClock()
		s.networkDone.Add(1)
		go s.serveProtectedSIPConnection(conn)
	}
}

func (s *Service) serveProtectedSIPConnection(conn net.Conn) {
	defer s.networkDone.Done()
	defer conn.Close()
	err := s.readRegistrationStreamSync(conn)
	recoverFlow := s.recordPortSClosed(conn, err, time.Now())
	s.untrackProtectedConnection(conn)
	if err != nil && !s.stopped() {
		logging.WarnRate("ims-protected-server-stream-"+s.DeviceID(),
			"IMS protected server push connection closed", "err", err)
		if recoverFlow {
			s.handleProtectedServerPushClosed()
		}
	}
}

// handleProtectedServerPushClosed waits for the P-CSCF to reopen port-s and
// falls back to a REGISTER refresh if it does not. A failed refresh is not
// evidence of an on-demand flow. A later peer reconnect permits waiting after
// clean EOF only; it does not establish reachability after transport errors.
// A timeout on a listed carrier waits only after a successful REGISTER
// (canAwaitPeerAfterTimeout).
func (s *Service) handleProtectedServerPushClosed() {
	if s == nil || s.stopped() {
		return
	}
	if s.capturePortSSession().lastCloseKind == portSCloseLocal {
		return
	}
	s.protectedConnMu.Lock()
	remaining := len(s.protectedConns)
	if remaining > 0 {
		s.protectedConnMu.Unlock()
		return
	}
	s.portSReconnectWaiting.Store(true)
	// A new close starts a new episode: only a REGISTER response after this
	// close can show the P-CSCF is still there (canAwaitPeerAfterTimeout).
	s.portSRecoveryAwaitingFlow.Store(false)
	staleAwait := s.portSTimeoutAwaitEntered.Swap(false)
	s.protectedConnMu.Unlock()
	if staleAwait {
		// The close was published while the await still reported ready.
		s.notifySMSReadiness()
		logging.WarnRate("ims-ports-stale-await-"+s.DeviceID(), 30*time.Second,
			"IMS port-s closed while still marked awaiting peer after timeout; restart recovery",
			"device", s.DeviceID(), "close_kind", s.capturePortSSession().lastCloseKind)
	}
	if s.canAwaitOnDemandPortS() {
		logging.Info("IMS protected server push closed; await peer port-s reconnect",
			"device", s.DeviceID(), "since_last_read", s.portSSinceLastRead(),
			"close_kind", s.capturePortSSession().lastCloseKind,
			"peer_reconnect_observed", s.portSOnDemandObserved.Load(),
			"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
		return
	}
	now := time.Now()
	if retryAt, waiting := s.portSRecoveryDeadline(now); waiting {
		logging.Info("IMS protected server push closed during recovery backoff",
			"device", s.DeviceID(), "since_last_read", s.portSSinceLastRead(),
			"retry_in", retryAt.Sub(now))
		// No probe REGISTER follows this close until the backoff ends.
		if s.endTimeoutProbeGrace("recovery backoff", nil) {
			s.notifySMSReadiness()
		}
		s.schedulePortSReconnectWatchAt(retryAt)
		return
	}
	grace := s.portSReconnectWait()
	logging.Info("IMS protected server push closed; schedule port-s recovery",
		"device", s.DeviceID(), "grace", grace,
		"since_last_read", s.portSSinceLastRead())
	s.schedulePortSReconnectWatchAt(now.Add(grace))
}

func (s *Service) portSReconnectWait() time.Duration {
	if s != nil && s.portSReconnectGrace > 0 {
		return s.portSReconnectGrace
	}
	if s.usesVodafoneUKPeerResetGrace() {
		return vodafoneUKPortSResetReconnectGrace
	}
	if s != nil && usesVodafoneUKPortSResetRecovery(s.cfg) {
		return vodafoneUKPortSReconnectGrace
	}
	if s != nil && s.capturePortSSession().lastCloseKind == portSCloseEOF {
		return defaultPortSReconnectGrace
	}
	// RFC 5626 section 4.5 recovers failed flows without this carrier-specific
	// grace. Failed attempts are still gated by the existing recovery backoff.
	return 0
}

func (s *Service) portSRecoveryValidationWait() time.Duration {
	if grace := s.portSReconnectWait(); grace > 0 {
		return grace
	}
	return defaultPortSDownlinkValidationWait
}

func (s *Service) schedulePortSReconnectWatchAt(wakeAt time.Time) {
	if s == nil || s.stopped() {
		return
	}
	delay := time.Until(wakeAt)
	if delay < 0 {
		delay = 0
	}
	registrar := s.currentPortSRecoveryRegistrar()
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	s.portSWatchGeneration++
	generation := s.portSWatchGeneration
	if s.portSWatchTimer != nil {
		s.portSWatchTimer.Stop()
	}
	s.portSWatchTimer = time.AfterFunc(delay, func() {
		s.portSReconnectWatchFired(generation, registrar)
	})
}

func (s *Service) cancelPortSReconnectWatch() {
	if s == nil {
		return
	}
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	s.portSWatchGeneration++
	if s.portSWatchTimer != nil {
		s.portSWatchTimer.Stop()
		s.portSWatchTimer = nil
	}
}

func (s *Service) portSReconnectWatchFired(generation uint64, registrar string) {
	if s == nil {
		return
	}
	// REGISTER owns recovery completion and the periodic schedule until it
	// releases registerMu. Revalidate the watch afterwards: completion or a
	// peer reconnect may have replaced it while this callback was waiting.
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	if !s.consumePortSReconnectWatch(generation, registrar) {
		return
	}
	if s.stopped() || s.RegState() != regRegistered {
		return
	}
	s.protectedConnMu.Lock()
	remaining := len(s.protectedConns)
	if remaining > 0 || s.awaitPortSAfterRegister() || s.portSTimeoutDownlinkProven() {
		s.protectedConnMu.Unlock()
		return
	}
	if s.awaitingPeerAfterTimeout() {
		s.protectedConnMu.Unlock()
		// enterPortSTimeoutAwait bumps the watch generation via
		// resetPortSRecoveryBackoff, so this is a bug.
		logging.WarnRate("ims-ports-await-watch-"+s.DeviceID(), 30*time.Second,
			"IMS port-s watch fired while awaiting peer after timeout; ignored",
			"device", s.DeviceID(), "generation", generation, "registrar", registrar)
		return
	}
	portSGeneration := s.capturePortSSession().generation
	s.protectedConnMu.Unlock()
	if s.portSRecoveryAwaitingFlow.Swap(false) {
		s.backoffMissingPortSAfterRegister(portSGeneration)
		return
	}
	if retryAt, waiting := s.portSRecoveryDeadline(time.Now()); waiting {
		s.schedulePortSReconnectWatchAt(retryAt)
		return
	}
	if s.recoverTimedOutPortSLocked() {
		return
	}
	if !s.portSRecoveryPending.CompareAndSwap(false, true) {
		return
	}
	s.portSRecoveryRegisterGen.Store(portSGeneration)
	s.markPortSResetRecoveryAttempt(registrar)
	s.triggerRegisterImmediate("port-s flow failed")
}

// A historical peer reconnect is a compatibility hint for clean EOF, not a
// guarantee that a reset or timed-out downlink can recover without REGISTER.
// One NZ resets port-s on its own cycle and reopens it minutes later or when
// it has a request to deliver; the idle time before the reset drifts (17-19
// minutes on 10-15, 12.5-14.9 minutes on 10-16), so it is not a signal.
// Globe PH closes port-s minutes after REGISTER and reopens it to deliver.
// DITO PH sends FIN on port-s after about 180s idle (lifetimes 3m00-3m58 on
// 10-17); the REGISTER after each close hit a protected port conflict every
// 3.5 minutes, about 400 times a day.
func (s *Service) canAwaitOnDemandPortS() bool {
	if s == nil {
		return false
	}
	preset := ""
	if s.cfg != nil {
		preset = strings.TrimSpace(s.cfg.CarrierPresetID)
	}
	observed := s.portSOnDemandObserved.Load()
	switch s.capturePortSSession().lastCloseKind {
	case portSCloseEOF:
		// 2degrees closes an idle port-s after about 2 minutes and has been
		// seen reopening it for a later MT request.
		return observed || preset == globePHCarrierPresetID || preset == ditoPHCarrierPresetID ||
			preset == twoDegreesNZCarrierPresetID
	case portSClosePeerReset:
		return preset == oneNZCarrierPresetID
	case portSCloseSARetired:
		// The P-CSCF sends further requests to port_us over the new SAs,
		// opening a new port-s flow (TS 24.229 5.2.2.2).
		return true
	}
	return false
}

// A carrier preset only skips the REGISTER right after the close. Port-s
// still missing after a successful REGISTER backs off like any other carrier,
// and a reset that REGISTER has not restored keeps following that backoff.
// A historical peer reconnect only keeps a clean EOF waiting.
func (s *Service) awaitPortSAfterRegister() bool {
	if s.capturePortSSession().lastCloseKind == portSClosePeerReset {
		if s.portSRecoveryAwaitingFlow.Load() || s.portSMissingAfterRegister.Load() {
			return false
		}
	} else if (s.portSRecoveryAwaitingFlow.Load() || s.portSMissingAfterRegister.Load()) &&
		!s.portSOnDemandObserved.Load() {
		return false
	}
	return s.canAwaitOnDemandPortS()
}

// canAwaitPeerAfterTimeout reports whether a timed-out port-s can be left
// for the P-CSCF to reopen. TS 33.203 §7.1 item 1 (TCP case) makes the P-CSCF
// set up a connection to port_us before sending a request, and §7.1 NOTE 6
// does not require keeping it open. The REGISTER after the close must have
// succeeded (portSRecoveryAwaitingFlow) so a dead P-CSCF is still caught.
// A timeout does not show the P-CSCF chose to close, and a past peer reconnect
// is only a hint for a clean EOF (canAwaitOnDemandPortS), so only carriers
// seen reopening a timed-out port-s for MT requests wait (timeoutAwaitPreset).
// Other carriers, Vodafone UK's validation and failover, and RFC 5626 outbound
// flows keep their recovery.
func (s *Service) canAwaitPeerAfterTimeout() bool {
	if s == nil || usesVodafoneUKPortSResetRecovery(s.cfg) || s.sipOutboundFlow.Load() {
		return false
	}
	return s.capturePortSSession().lastCloseKind == portSCloseTimeout &&
		s.timeoutAwaitPreset() && s.portSRecoveryAwaitingFlow.Load()
}

// timeoutAwaitPreset reports a carrier whose P-CSCF lets an idle port-s time
// out and reopens it only to deliver a request. Lebara UK: port-s timed out
// after 2-14 hours idle on 10-06, 10-08 and 10-09, REGISTER 200 never reopened
// it, and an MT SMS reopened it each time (10-24: after 21m48s closed; 10-28:
// after 9m51s and 4m27s, the second for an A2P SMS).
func (s *Service) timeoutAwaitPreset() bool {
	return s != nil && s.cfg != nil && strings.TrimSpace(s.cfg.CarrierPresetID) == lebaraUKCarrierPresetID
}

// timeoutProbeGraceLocked keeps SMS reception ready from a port-s timeout on
// a timeoutAwaitPreset carrier until the probe REGISTER after it answers. The
// timeout itself is not a fault: either side may close the TCP connection
// (TS 24.229 §4.2A NOTE 2) and this P-CSCF reopens it on demand. The probe
// takes about one REGISTER round trip; its failure (endTimeoutProbeGrace), a
// validation after its success, another P-CSCF or a new registration
// (resetPortSRecoveryKnowledge) ends the grace. Outbound flows keep their
// RFC 5626 recovery and readiness. The caller holds mu.
func (s *Service) timeoutProbeGraceLocked(portS portSSessionSnapshot) bool {
	probe := s.portSTimeoutProbe.Load()
	return probe != nil && probe.generation == portS.generation &&
		strings.EqualFold(probe.registrar, strings.TrimSpace(s.registrar)) &&
		s.timeoutAwaitPreset() && !s.sipOutboundFlow.Load() &&
		portS.lastCloseKind == portSCloseTimeout && !portS.connected &&
		!s.portSRecoveryAwaitingFlow.Load() && !s.portSMissingAfterRegister.Load()
}

// endTimeoutProbeGrace ends the reception grace of the current timed-out
// port-s; it reports whether a grace was ended. Callers publish readiness.
func (s *Service) endTimeoutProbeGrace(reason string, err error) bool {
	probe := s.portSTimeoutProbe.Load()
	session := s.capturePortSSession()
	if probe == nil || probe.generation != session.generation ||
		!s.portSTimeoutProbe.CompareAndSwap(probe, nil) {
		return false
	}
	logging.Info("IMS port-s timeout reception grace ended",
		"device", s.DeviceID(), "reason", reason, "err", err,
		"port_s_generation", session.generation, "cseq", s.cseq.Load(), "status", s.lastSIPCode.Load(),
		"since_close", time.Since(session.closedAt).Round(time.Second),
		"pcscf", probe.registrar, "carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
	return true
}

// probeTimedOutPortSAgain sends the probe REGISTER for a timeout that closed
// port-s while another REGISTER was in flight: that REGISTER's success belongs
// to an older port-s generation, so it does not show the P-CSCF answered after
// this close. The watch sends the probe once this REGISTER releases registerMu.
func (s *Service) probeTimedOutPortSAgain() bool {
	if !s.timeoutAwaitPreset() || s.sipOutboundFlow.Load() {
		return false
	}
	s.protectedConnMu.Lock()
	session := s.capturePortSSession()
	registerGen := s.portSRecoveryRegisterGen.Load()
	stale := len(s.protectedConns) == 0 && session.lastCloseKind == portSCloseTimeout &&
		session.generation != registerGen
	s.protectedConnMu.Unlock()
	if !stale {
		return false
	}
	s.portSRecoveryAwaitingFlow.Store(false)
	logging.Info("IMS port-s timed out during a REGISTER; probing with another REGISTER",
		"device", s.DeviceID(), "port_s_generation", session.generation,
		"register_port_s_generation", registerGen, "cseq", s.cseq.Load(), "status", s.lastSIPCode.Load(),
		"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
	s.schedulePortSReconnectWatchAt(time.Now())
	return true
}

// awaitingPeerAfterTimeout reports an await that enterPortSTimeoutAwait
// accepted for the current episode. Recovery and readiness follow this, not
// eligibility, so a refused entry keeps the normal validation path.
func (s *Service) awaitingPeerAfterTimeout() bool {
	return s != nil && s.portSTimeoutAwaitEntered.Load()
}

func portSClosedFor(session portSSessionSnapshot) time.Duration {
	if session.closedAt.IsZero() || session.openedAt.Before(session.closedAt) {
		return 0
	}
	return session.openedAt.Sub(session.closedAt)
}

const (
	oneNZCarrierPresetID        = "one_nz_53001"
	globePHCarrierPresetID      = "globe_ph_51502"
	ditoPHCarrierPresetID       = "dito_51566"
	twoDegreesNZCarrierPresetID = "2degrees_nz_53024"
	lebaraUKCarrierPresetID     = "lebara_uk_23487"
)

func (s *Service) consumePortSReconnectWatch(generation uint64, registrar string) bool {
	if s == nil {
		return false
	}
	currentRegistrar := s.currentPortSRecoveryRegistrar()
	s.portSWatchMu.Lock()
	defer s.portSWatchMu.Unlock()
	if generation != s.portSWatchGeneration || registrar != currentRegistrar {
		return false
	}
	s.portSWatchTimer = nil
	return true
}

func (s *Service) completePortSRecovery(err error, bindingPreserved bool) {
	if s == nil {
		return
	}
	pending := s.portSRecoveryPending.Swap(false)
	if err == nil {
		if pending {
			s.markPortSResetRecoverySucceeded(s.currentPortSRecoveryRegistrar())
		}
		if s.portSPushReady.Load() || s.portSTimeoutDownlinkProven() {
			s.portSRecoveryAwaitingFlow.Store(false)
			s.portSReconnectWaiting.Store(false)
			s.resetPortSRecoveryBackoff()
			return
		}
		// A periodic success on the old flow cannot postpone an already
		// validated downlink failure or erase its retry deadline.
		if s.pendingPortSTimeoutFailover().registrar != "" {
			if retryAt, _ := s.portSRecoveryDeadline(time.Now()); !retryAt.IsZero() {
				s.schedulePortSReconnectWatchAt(retryAt)
			}
			return
		}
		if pending || s.portSReconnectWaiting.Load() {
			s.clearPortSRecoveryDeadline()
			s.portSRecoveryAwaitingFlow.Store(true)
			s.portSReconnectWaiting.Store(true)
			if s.canAwaitPeerAfterTimeout() && s.enterPortSTimeoutAwait(s.portSRecoveryRegisterGen.Load()) {
				return
			}
			if s.portSTimeoutAwaitEntered.Swap(false) {
				// The await no longer qualifies (for example this refresh
				// negotiated an outbound flow), so validate port-s again.
				logging.Info("IMS port-s await after timeout revoked; validating port-s",
					"device", s.DeviceID(), "peer_reconnect_observed", s.portSOnDemandObserved.Load(),
					"carrier_preset_await", s.timeoutAwaitPreset(),
					"vodafone_uk", usesVodafoneUKPortSResetRecovery(s.cfg), "outbound", s.sipOutboundFlow.Load(),
					"close_kind", s.capturePortSSession().lastCloseKind,
					"register_port_s_generation", s.portSRecoveryRegisterGen.Load(),
					"port_s_generation", s.capturePortSSession().generation,
					"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
			}
			if s.probeTimedOutPortSAgain() {
				return
			}
			if pending && s.capturePortSSession().lastCloseKind == portSCloseTimeout {
				logging.Info("IMS port-s timed out; REGISTER succeeded, validating port-s",
					"device", s.DeviceID(), "peer_reconnect_observed", s.portSOnDemandObserved.Load(),
					"vodafone_uk", usesVodafoneUKPortSResetRecovery(s.cfg), "outbound", s.sipOutboundFlow.Load(),
					"register_port_s_generation", s.portSRecoveryRegisterGen.Load(),
					"port_s_generation", s.capturePortSSession().generation,
					"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
			}
			s.schedulePortSReconnectWatchAt(time.Now().Add(s.portSRecoveryValidationWait()))
		}
		return
	}
	// A failed REGISTER supersedes any earlier post-success validation wait.
	s.retainReplacementRegisterRetryAfter(err)
	s.portSRecoveryAwaitingFlow.Store(false)
	s.endTimeoutProbeGrace("REGISTER failed", err)
	if s.portSTimeoutAwaitEntered.Swap(false) {
		next := "resume port-s recovery"
		if !bindingPreserved {
			next = "binding lost; registration restarts"
		}
		logging.Info("IMS REGISTER failed while awaiting peer port-s after timeout",
			"device", s.DeviceID(), "err", err, "binding_preserved", bindingPreserved,
			"canceled", isRegisterOperationCanceled(err), "next", next,
			"since_close", time.Since(s.capturePortSSession().closedAt).Round(time.Second),
			"peer_reconnect_observed", s.portSOnDemandObserved.Load(),
			"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
	}
	if !bindingPreserved {
		s.portSReconnectWaiting.Store(false)
		s.resetPortSRecoveryBackoff()
		return
	}
	if !pending && (s.portSPushReady.Load() || !s.portSReconnectWaiting.Load() || s.canAwaitOnDemandPortS()) {
		if s.portSPushReady.Load() {
			s.portSReconnectWaiting.Store(false)
		}
		return
	}
	backoff := s.recordPortSRecoveryFailure(err, time.Now())
	if s.portSPushReady.Load() {
		// The failed REGISTER itself may have prompted this connection.
		// It does not prove that the peer reconnects on demand.
		s.portSReconnectWaiting.Store(false)
	} else {
		s.schedulePortSReconnectWatchAt(backoff.retryAt)
	}
	logging.WarnRate("ims-ports-recovery-backoff-"+s.DeviceID(), 30*time.Second,
		"IMS port-s REGISTER recovery failed; keep current binding and back off",
		"device", s.DeviceID(), "err", err,
		"failures", backoff.failures, "retry_in", backoff.delay,
		"retry_after", backoff.retryAfter, "retry_after_present", backoff.retryAfterSet)
}

func (s *Service) recordOnDemandPortSReconnect() {
	if s == nil {
		return
	}
	timeout := s.clearPortSTimeoutRecovery()
	awaitedAfterTimeout := s.portSTimeoutAwaitEntered.Swap(false)
	awaitingFlow := s.portSRecoveryAwaitingFlow.Swap(false)
	session := s.capturePortSSession()
	if awaitedAfterTimeout {
		s.portSReconnectWaiting.Store(false)
		s.resetPortSRecoveryBackoff()
		if s.portSRecoveryPending.Load() || s.registrationInProgress() {
			// A REGISTER in flight may have prompted this reopen, so it is
			// not on-demand evidence, as below.
			logging.Info("IMS peer reopened port-s while awaited after timeout during a REGISTER; not counted as on-demand",
				"device", s.DeviceID(), "close_kind", session.lastCloseKind, "closed_for", portSClosedFor(session),
				"port_s_generation", session.generation, "cseq", s.cseq.Load(), "status", s.lastSIPCode.Load(),
				"peer_reconnect_observed", s.portSOnDemandObserved.Load(),
				"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
			return
		}
		s.portSOnDemandObserved.Store(true)
		logging.Info("IMS peer reopened port-s while awaited after timeout", "device", s.DeviceID(),
			"close_kind", session.lastCloseKind, "closed_for", portSClosedFor(session),
			"peer_reconnect_observed", s.portSOnDemandObserved.Load(),
			"carrier_preset", strings.TrimSpace(s.cfg.CarrierPresetID))
		return
	}
	if awaitingFlow || timeout.validationFailed {
		s.portSReconnectWaiting.Store(false)
		s.resetPortSRecoveryBackoff()
		logging.Info("IMS port-s reopened after successful REGISTER", "device", s.DeviceID(),
			"close_kind", session.lastCloseKind, "closed_for", portSClosedFor(session))
		return
	}
	if s.portSRecoveryPending.Load() || s.registrationInProgress() {
		return
	}
	if !s.portSReconnectWaiting.CompareAndSwap(true, false) {
		return
	}
	if s.portSOnDemandObserved.Swap(true) {
		if session.lastCloseKind == portSClosePeerReset && s.canAwaitOnDemandPortS() {
			// The peer's own reconnect restarts a One NZ backoff, so failures
			// do not accumulate across reset cycles.
			s.resetPortSRecoveryFailures()
		}
		logging.Info("IMS peer reopened port-s while awaiting it", "device", s.DeviceID(),
			"close_kind", session.lastCloseKind, "closed_for", portSClosedFor(session))
		return
	}
	s.clearPortSResetRecovery(s.currentPortSRecoveryRegistrar())
	s.resetPortSRecoveryBackoff()
	logging.Info("IMS peer-initiated port-s reconnect observed",
		"device", s.DeviceID(),
		"reason", "peer reopened port-s without a successful REGISTER",
		"close_kind", session.lastCloseKind, "closed_for", portSClosedFor(session))
}

func (s *Service) registrationInProgress() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.regState == regRegistering || s.regState == regReregister
}

func (s *Service) resetPortSRecoveryKnowledge() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.downlinkGeneration++
	s.downlinkRequests = 0
	s.smsReception = smsReceptionEvidence{}
	s.udpDownlinkProven.Store(false)
	s.cancelReplacementDownlinkWatchLocked()
	s.mu.Unlock()
	s.resetPortSRecoveryBackoff()
	s.portSReconnectWaiting.Store(false)
	s.portSRecoveryPending.Store(false)
	s.portSRecoveryAwaitingFlow.Store(false)
	s.portSOnDemandObserved.Store(false)
	s.portSMissingAfterRegister.Store(false)
	s.portSTimeoutAwaitEntered.Store(false)
	s.portSTimeoutProbe.Store(nil)
	s.clearPortSResetRecovery("")
	s.clearPortSTimeoutRecovery()
}

func (s *Service) trackProtectedConnection(conn net.Conn) bool {
	// Validation commits a failed path under mu; accepting a new downlink must
	// be serialized with that final decision, not just the connection map.
	s.mu.RLock()
	s.protectedConnMu.Lock()
	select {
	case <-s.stop:
		s.protectedConnMu.Unlock()
		s.mu.RUnlock()
		return false
	default:
		s.protectedConns[conn] = struct{}{}
		changed := !s.portSPushReady.Swap(true)
		s.protectedConnMu.Unlock()
		s.mu.RUnlock()
		s.finishProtectedConnectionTracking(changed)
		return true
	}
}

// Track the accepted socket and its port-s generation atomically with failover.
func (s *Service) trackProtectedPortSConnection(conn net.Conn, now time.Time) bool {
	s.mu.Lock()
	s.protectedConnMu.Lock()
	select {
	case <-s.stop:
		s.protectedConnMu.Unlock()
		s.mu.Unlock()
		return false
	default:
	}
	// The P-CSCF reopened port-s, so a later close is awaiting it again.
	s.smsReception.reportFailed = false
	registrar := strings.TrimSpace(s.registrar)
	s.portSSessionMu.Lock()
	generation := s.recordPortSOpenedLocked(conn, now, registrar)
	s.protectedConns[conn] = struct{}{}
	changed := !s.portSPushReady.Swap(true)
	s.portSSessionMu.Unlock()
	s.protectedConnMu.Unlock()
	s.mu.Unlock()
	s.logPortSOpened(conn, registrar, generation)
	s.finishProtectedConnectionTracking(changed)
	return true
}

func (s *Service) finishProtectedConnectionTracking(readinessChanged bool) {
	s.portSMissingAfterRegister.Store(false)
	s.confirmCurrentRegistrarDownlinkHealthy()
	s.signalDownlinkValidation()
	s.cancelPortSReconnectWatch()
	s.recordOnDemandPortSReconnect()
	if readinessChanged {
		s.notifySMSReadiness()
	}
}

func (s *Service) untrackProtectedConnection(conn net.Conn) {
	s.protectedConnMu.Lock()
	delete(s.protectedConns, conn)
	empty := len(s.protectedConns) == 0
	changed := empty && s.portSPushReady.Swap(false)
	s.protectedConnMu.Unlock()
	if changed {
		s.notifySMSReadiness()
	}
}

func (s *Service) detachProtectedConnections() []net.Conn {
	if s == nil {
		return nil
	}
	s.protectedConnMu.Lock()
	connections := make([]net.Conn, 0, len(s.protectedConns))
	for conn := range s.protectedConns {
		connections = append(connections, conn)
	}
	s.protectedConns = make(map[net.Conn]struct{})
	changed := s.portSPushReady.Swap(false)
	s.protectedConnMu.Unlock()
	if changed {
		s.notifySMSReadiness()
	}
	return connections
}

func (s *Service) readRegistrationStream(conn net.Conn) {
	defer s.networkDone.Done()
	s.receiverStarted()
	defer s.receiverStopped()
	readErr := s.readRegistrationStreamSync(conn)
	s.clearClosedRegistrationTCP(conn, readErr)
}

func (s *Service) clearClosedRegistrationTCP(conn net.Conn, readErr error) {
	if readErr == nil {
		readErr = io.EOF
	}
	err := fmt.Errorf("imscore: registration SIP stream closed: %w", readErr)
	_ = conn.Close()
	stopped := s.stopped()
	s.mu.Lock()
	current := s.registrationTCP == conn
	pending := s.registrationPendingTCP == conn
	switching := s.registrationPendingTCP != nil
	if pending {
		// Fail SM7 now instead of waiting for Timer F; a commit racing this
		// close sees the error.
		s.registrationPendingErr = readErr
		if s.registrationPendingCancel != nil {
			s.registrationPendingCancel()
		}
	}
	role := "retired"
	switch {
	case current:
		role = "current"
	case pending:
		role = "sm7_pending"
	}
	logging.WarnRate("ims-registration-stream", "IMS registration SIP stream closed", "err", err,
		"role", role, "switch_pending", switching, "local", conn.LocalAddr())
	if current && switching {
		// The refresh switch owns the outcome: a commit publishes the new
		// flow, an abort finds no intact flow and fails the refresh.
		s.registrationTCP = nil
		s.registrationTCPProtected = false
		s.transport.SetSendFn(func(string) error {
			return errors.New("imscore: registered SIP transport is not connected")
		})
		s.mu.Unlock()
		s.failTCPKeepalivePong(errTCPCRLFStreamClosed)
		return
	}
	if current {
		if !stopped {
			s.abandonReplacementDownlinkWaitLocked()
		}
		s.registrationTCP = nil
		s.invalidateSMSReceptionLocked()
		s.registrationTCPProtected = false
		if !stopped {
			s.regState = regFailed
			s.transport.SetSendFn(func(string) error {
				return errors.New("imscore: registered SIP transport is not connected")
			})
		}
	}
	s.mu.Unlock()
	if current {
		// A retired or still-pending flow carries no keepalive of its own.
		s.failTCPKeepalivePong(errTCPCRLFStreamClosed)
	}
	if !current || stopped {
		return
	}
	s.transport.terminateClientTransactions(transactionTransportError(readErr))
	s.transitionRegStatus(registrationRejectedTemporary)
	s.notifySMSReadiness()
	s.reportRegistrationRuntimeError(err)
}

func (s *Service) readRegistrationStreamSync(conn net.Conn) error {
	decoder := newSIPStreamDecoder(conn)
	decoder.onPong = s.signalTCPKeepalivePong
	defer decoder.Close()
	for {
		message, err := decoder.ReadMessage()
		if err != nil {
			return err
		}
		s.noteInboundOverNewSA(tcpPortFromAddr(conn.LocalAddr()), tcpPortFromAddr(conn.RemoteAddr()))
		if err := s.dispatchInboundSIPMessageWithPeer(message, message.String(), func(response string) error {
			return s.writeSIPStream(conn, response)
		}, conn); err != nil {
			logging.WarnRate("ims-tcp-inbound", "IMS TCP inbound handling failed", "err", err)
		}
	}
}

type tcpKeepaliveConn interface {
	SetKeepAlive(bool) error
	SetKeepAlivePeriod(time.Duration) error
}

func configureTCPKeepalive(conn net.Conn, idle time.Duration) {
	for {
		counted, ok := conn.(*inboundCountingConn)
		if !ok || counted == nil {
			break
		}
		conn = counted.conn
	}
	keepalive, ok := conn.(tcpKeepaliveConn)
	if !ok {
		return
	}
	if idle <= 0 {
		idle = imsTCPSocketKeepaliveIdle
	}
	_ = keepalive.SetKeepAlive(true)
	_ = keepalive.SetKeepAlivePeriod(idle)
}

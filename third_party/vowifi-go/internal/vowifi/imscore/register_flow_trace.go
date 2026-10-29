package imscore

import (
	"strconv"
	"strings"
	"time"

	"github.com/iniwex5/vowifi-go/internal/vowifi/logging"
)

func (s *Service) logRegisterFlowNegotiation(response *sipResponse) {
	if response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return
	}
	supported := strings.ToLower(strings.Join(response.HeaderValues("Supported"), ","))
	require := strings.ToLower(strings.Join(response.HeaderValues("Require"), ","))
	path := strings.ToLower(strings.Join(response.HeaderValues("Path"), ","))
	serviceRoutes := response.HeaderValues("Service-Route")
	serviceRouteText := strings.Join(serviceRoutes, ",")
	contact := strings.ToLower(strings.Join(response.HeaderValues("Contact"), ","))
	requiredOutbound := containsHeaderToken(require, "outbound")
	viaKeep := viaHasKeepParameter(response.HeaderValues("Via"))
	supportedOutbound := containsHeaderToken(supported, "outbound")
	pathOB := containsSIPParameter(path, "ob")
	contactRegID := containsSIPParameter(contact, "reg-id")
	s.mu.Lock()
	s.sipOutboundKeepalive = requiredOutbound || viaKeep
	// RFC 5626: Supported: outbound only means the registrar understands
	// outbound. A follow-up REGISTER with reg-id is for an actual flow
	// (Require, Path;ob, or an echoed reg-id). 2degrees advertises
	// Supported and 503s the extra REGISTER (hideck#8 / #6).
	s.sipOutboundRequired = requiredOutbound
	s.sipOutbound = requiredOutbound || pathOB || contactRegID
	s.sipOutboundFlow.Store(s.sipOutbound)
	if s.outboundContactOffered {
		s.outboundContactRegistered = true
	}
	s.flowTimer = parseFlowTimerHeader(response.HeaderValues("Flow-Timer"))
	s.stunMappedAddr = nil
	s.mu.Unlock()
	s.applyRFC5626TCPKeepalive()
	logging.Info("IMS REGISTER flow negotiation",
		"device", s.DeviceID(),
		"supported_outbound", supportedOutbound,
		"required_outbound", requiredOutbound,
		"via_keep", viaKeep,
		"path_present", strings.TrimSpace(path) != "",
		"path_ob", pathOB,
		"service_route_count", serviceRouteHeaderHopCount(serviceRouteText),
		"service_route_orig", serviceRouteHeaderHasOrig(serviceRouteText),
		"contact_reg_id", contactRegID)
}

func serviceRouteHeaderHopCount(value string) int {
	count := 0
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) != "" {
			count++
		}
	}
	return count
}

func serviceRouteHeaderHasOrig(value string) bool {
	for _, item := range strings.Split(strings.ToLower(value), ",") {
		item = strings.TrimSpace(item)
		if strings.Contains(item, "sip:orig@") || strings.Contains(item, "sip:orig;") {
			return true
		}
	}
	return false
}

func parseFlowTimerHeader(values []string) time.Duration {
	for _, value := range values {
		seconds, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || seconds <= 0 {
			continue
		}
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func viaHasKeepParameter(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ";") {
			name, interval, found := strings.Cut(strings.TrimSpace(part), "=")
			if !strings.EqualFold(name, "keep") || !found {
				continue
			}
			interval = strings.TrimSpace(interval)
			if interval == "" || interval == "0" {
				continue
			}
			for _, character := range interval {
				if character < '0' || character > '9' {
					interval = ""
					break
				}
			}
			if interval != "" {
				return true
			}
		}
	}
	return false
}

func containsHeaderToken(value, target string) bool {
	for token := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), target) {
			return true
		}
	}
	return false
}

func containsSIPParameter(value, parameter string) bool {
	parameter = strings.ToLower(strings.TrimSpace(parameter))
	if parameter == "" {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == ',' || r == '>' || r == '<'
	}) {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.EqualFold(name, parameter) {
			return true
		}
	}
	return false
}

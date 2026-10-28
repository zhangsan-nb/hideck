package imscore

import (
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
)

var errRegisterContactMissing = errors.New("imscore: REGISTER response does not contain the requested Contact binding")

func registrationExpires(
	response *sipResponse, requestContact string, configured time.Duration, acceptWithoutUser bool,
) (time.Duration, error) {
	contact := matchingRegisterContact(response, requestContact, acceptWithoutUser)
	if contact == "" {
		return 0, errRegisterContactMissing
	}
	// RFC 3261 10.2.4: only our binding's Contact parameter overrides Expires.
	if value, exists := contactParameterValue(contact, "expires"); exists {
		return parseRegistrationLifetime(value)
	}
	if value := strings.TrimSpace(response.Header("Expires")); value != "" {
		return parseRegistrationLifetime(value)
	}
	// Preserve the existing configured/default lifetime when none was supplied.
	if configured <= 0 {
		configured = time.Hour
	}
	return configured, nil
}

func parseRegistrationLifetime(value string) (time.Duration, error) {
	seconds, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0, errors.New("imscore: invalid REGISTER binding expiration")
	}
	if seconds == 0 {
		return 0, errors.New("imscore: REGISTER binding has zero expiration")
	}
	return time.Duration(seconds) * time.Second, nil
}

func matchingRegisterContact(response *sipResponse, requestContact string, acceptWithoutUser bool) string {
	var own sip.Uri
	if _, err := sip.ParseAddressValue(requestContact, &own, nil); err != nil || own.Host == "" {
		return ""
	}
	if contact := findRegisterContact(response, own); contact != "" || !acceptWithoutUser || own.User == "" {
		return contact
	}
	// RFC 3261 19.1.4 says a URI without a user part does not match ours.
	// 2degrees NZ still answers our REGISTER with only the userless form of
	// our address. The inner IP and protected port are unique to this flow,
	// so the preset opts in to taking that form as our binding.
	own.User, own.Password = "", ""
	return findRegisterContact(response, own)
}

func findRegisterContact(response *sipResponse, own sip.Uri) string {
	for _, header := range response.HeaderValues("Contact") {
		for _, contact := range splitQuotedSIPHeaderValues(header) {
			var candidate sip.Uri
			if _, err := sip.ParseAddressValue(contact, &candidate, nil); err != nil {
				continue
			}
			if sameRegisterContactURI(own, candidate) {
				return contact
			}
		}
	}
	return ""
}

// Compare the SIP URI, not display names or Contact header parameters (19.1.4).
// In particular, a shared instance ID alone does not identify the current flow.
func sameRegisterContactURI(left, right sip.Uri) bool {
	if !strings.EqualFold(left.Scheme, right.Scheme) ||
		!sameRegisterContactHost(left.Host, right.Host) || left.Port != right.Port ||
		registerURIUnescape(left.User) != registerURIUnescape(right.User) ||
		registerURIUnescape(left.Password) != registerURIUnescape(right.Password) {
		return false
	}
	// Generated registration Contacts never carry URI headers. Do not ignore
	// headers injected into a response Contact when identifying our binding.
	if len(left.Headers) != 0 || len(right.Headers) != 0 {
		return false
	}
	return registerURIParamsMatch(left.UriParams, right.UriParams) &&
		registerURIParamsMatch(right.UriParams, left.UriParams)
}

func registerURIParamsMatch(left, right sip.HeaderParams) bool {
	for _, parameter := range left {
		name := registerURIUnescape(parameter.K)
		value, exists := registerURIParam(right, name)
		if exists {
			if !strings.EqualFold(registerURIUnescape(parameter.V), value) {
				return false
			}
			continue
		}
		switch strings.ToLower(name) {
		case "transport", "user", "ttl", "method", "maddr":
			return false
		}
	}
	return true
}

func registerURIParam(params sip.HeaderParams, name string) (string, bool) {
	for _, parameter := range params {
		if strings.EqualFold(registerURIUnescape(parameter.K), name) {
			return registerURIUnescape(parameter.V), true
		}
	}
	return "", false
}

func registerURIUnescape(value string) string {
	var result strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			decoded, err := strconv.ParseUint(value[i+1:i+3], 16, 8)
			if err == nil && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.!~*'()", rune(decoded)) {
				result.WriteByte(byte(decoded))
				i += 2
				continue
			}
		}
		result.WriteByte(value[i])
	}
	return result.String()
}

// redactContactUser keeps only what Contact matching compares and cannot
// carry an identity: the SIP host, port and transport. tel: URIs, user parts,
// other URI parameters and headers can carry the number, IMSI or IMEI.
func redactContactUser(contact string) string {
	var uri sip.Uri
	if _, err := sip.ParseAddressValue(contact, &uri, nil); err != nil {
		return "unparsable"
	}
	scheme := strings.ToLower(uri.Scheme)
	if scheme != "sip" && scheme != "sips" {
		return scheme + ":redacted"
	}
	var out strings.Builder
	out.WriteString(scheme + ":")
	if uri.User != "" {
		out.WriteString("redacted@")
	}
	out.WriteString(uri.Host)
	if uri.Port > 0 {
		out.WriteString(":" + strconv.Itoa(uri.Port))
	}
	if transport, ok := registerURIParam(uri.UriParams, "transport"); ok {
		out.WriteString(";transport=" + transport)
	}
	return out.String()
}

func redactedResponseContacts(response *sipResponse) []string {
	var contacts []string
	for _, header := range response.HeaderValues("Contact") {
		for _, contact := range splitQuotedSIPHeaderValues(header) {
			contacts = append(contacts, redactContactUser(contact))
		}
	}
	return contacts
}

func sipHeaderNames(response *sipResponse) []string {
	names := make([]string, 0, len(response.Headers))
	for name := range response.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RFC 5954 4.1: IPv6 references compare as addresses, so "1:0:2d13" and
// "1::2d13" are the same host.
func sameRegisterContactHost(left, right string) bool {
	if strings.EqualFold(left, right) {
		return true
	}
	leftIP := net.ParseIP(strings.Trim(left, "[]"))
	rightIP := net.ParseIP(strings.Trim(right, "[]"))
	return leftIP != nil && rightIP != nil && leftIP.Equal(rightIP)
}

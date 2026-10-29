package imscore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/emiago/sipgo/sip"
)

func TestFirstSIPHeaderURI(t *testing.T) {
	value := "<sip:+447840844894@o2.co.uk>,<tel:+447840844894>"
	if got := firstSIPHeaderURI(value); got != "sip:+447840844894@o2.co.uk" {
		t.Fatalf("firstSIPHeaderURI = %q", got)
	}
}

func TestBuildSIPRequestResponseAcknowledgesNotify(t *testing.T) {
	request := "NOTIFY sip:user@example SIP/2.0\r\n" +
		"Via: SIP/2.0/TCP 192.0.2.1:6060;branch=z9hG4bK-notify\r\n" +
		"From: <sip:server@example>;tag=server\r\n" +
		"To: <sip:user@example>\r\n" +
		"Call-ID: notify-call\r\n" +
		"CSeq: 1 NOTIFY\r\n" +
		"Event: reg\r\nContent-Length: 0\r\n\r\n"
	response, err := buildSIPRequestResponse(request, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SIP/2.0 200 OK", "Via: SIP/2.0/TCP 192.0.2.1:6060;branch=z9hG4bK-notify",
		"Call-ID: notify-call", "CSeq: 1 NOTIFY", "To: <sip:user@example>;tag=",
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("NOTIFY response omitted %q: %q", want, response)
		}
	}
}

func TestRegistrationSubscriptionUsesProductionTransactionAndRefreshes(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	client, server := net.Pipe()
	service.activateProtectedRegistrationTCP(client)
	t.Cleanup(func() { _ = server.Close() })

	requests := make(chan string, 2)
	errorsSeen := make(chan error, 1)
	go serveSubscriptionResponses(server, requests, errorsSeen, 2)
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.sendSubscribeReg(context.Background()); err != nil {
			t.Fatalf("sendSubscribeReg attempt %d: %v", attempt+1, err)
		}
	}
	if err := <-errorsSeen; err != nil {
		t.Fatal(err)
	}
	first, second := <-requests, <-requests
	assertRegistrationSubscriptionRequest(t, first, 5)
	assertInDialogRegistrationSubscription(t, second, 6, "reg-notifier")
	if rawSIPHeaderValue(first, "Call-ID") != rawSIPHeaderValue(second, "Call-ID") {
		t.Fatal("refresh opened a new subscription dialog")
	}
	if rawSIPHeaderValue(first, "From") != rawSIPHeaderValue(second, "From") {
		t.Fatal("refresh changed the subscription From")
	}

	service.mu.RLock()
	expires := service.subscriptionExpires
	lastOK := service.subscriptionLastOKAt
	refreshAt := service.subscriptionRefreshAt
	dialog := service.subscriptionDialog
	service.mu.RUnlock()
	if expires != 120*time.Second {
		t.Fatalf("subscription expires = %s, want 2m", expires)
	}
	if delay := refreshAt.Sub(lastOK); delay != time.Minute {
		t.Fatalf("subscription refresh delay = %s, want 1m", delay)
	}
	if !dialog.ready() || dialog.remoteTag != "reg-notifier" || dialog.cseq != 6 {
		t.Fatalf("subscription dialog = %+v", dialog)
	}
}

func serveSubscriptionResponses(
	conn net.Conn,
	requests chan<- string,
	result chan<- error,
	count int,
) {
	reader := bufio.NewReader(conn)
	for index := 0; index < count; index++ {
		request, err := readSIPStreamMessage(reader)
		if err != nil {
			result <- err
			return
		}
		requests <- request
		if _, err = io.WriteString(conn, subscriptionWireResponse(request, 200, "Expires: 120\r\n")); err != nil {
			result <- err
			return
		}
	}
	result <- nil
}

func assertRegistrationSubscriptionRequest(t *testing.T, request string, wantCSeq int) {
	t.Helper()
	assertRegistrationSubscription(t, request, wantCSeq, 3600, "")
}

func assertInDialogRegistrationSubscription(t *testing.T, request string, wantCSeq int, toTag string) {
	t.Helper()
	assertRegistrationSubscription(t, request, wantCSeq, 3600, toTag)
}

func assertRegistrationSubscription(t *testing.T, request string, wantCSeq, expires int, toTag string) {
	t.Helper()
	if !strings.HasPrefix(request, "SUBSCRIBE sip:+447840844894@o2.co.uk SIP/2.0") {
		t.Fatalf("request line = %q", strings.SplitN(request, "\r\n", 2)[0])
	}
	checks := map[string]string{
		"Event": "reg", "Accept": reginfoContentType, "Expires": strconv.Itoa(expires),
		"CSeq":            strconv.Itoa(wantCSeq) + " SUBSCRIBE",
		"Security-Verify": "ipsec-3gpp;alg=hmac-sha-1-96",
	}
	for name, want := range checks {
		if got := rawSIPHeaderValue(request, name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	to := rawSIPHeaderValue(request, "To")
	if toTag == "" {
		if sipAddressTag(to) != "" {
			t.Fatalf("initial SUBSCRIBE To should not have a tag: %q", to)
		}
	} else if sipAddressTag(to) != toTag {
		t.Fatalf("To = %q, want tag %s", to, toTag)
	}
	contact := rawSIPHeaderValue(request, "Contact")
	if !strings.Contains(contact, ":16083;ob;transport=tcp>") {
		t.Fatalf("Contact = %q, want protected server port, ob, and transport", contact)
	}
	viaBranch, err := parseTopViaBranch(rawSIPHeaderValue(request, "Via"))
	if err != nil || !strings.HasPrefix(viaBranch, "z9hG4bK") || len(viaBranch) != len("z9hG4bK")+36 {
		t.Fatalf("Via branch = %q, err = %v", viaBranch, err)
	}
}

func subscriptionWireResponse(request string, status int, extraHeaders string) string {
	from := rawSIPHeaderValue(request, "From")
	to := rawSIPHeaderValue(request, "To")
	if sipAddressTag(to) == "" {
		to += ";tag=reg-notifier"
	}
	return registerWireResponse(request, status, "From: "+from+"\r\nTo: "+to+"\r\n"+extraHeaders)
}

func TestRegistrationSubscriptionSkipReason(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	if eligible, reason := service.registrationSubscriptionGate(); eligible || reason != "no_registration_tcp" {
		t.Fatalf("gate without TCP = eligible=%v reason=%q", eligible, reason)
	}
}

func TestLoggablePublicIDMasksUser(t *testing.T) {
	if got := loggablePublicID("sip:+447785016005@ims.mnc015.mcc234.3gppnetwork.org"); got != "***@ims.mnc015.mcc234.3gppnetwork.org" {
		t.Fatalf("loggablePublicID = %q", got)
	}
	if got := loggablePublicID(""); got != "" {
		t.Fatalf("empty public id = %q", got)
	}
}

func TestSubscriptionFailureKeepsRegistration(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	client, server := net.Pipe()
	service.activateProtectedRegistrationTCP(client)
	t.Cleanup(func() { _ = server.Close() })

	service.reportSubscriptionRuntimeError(errors.New("SUBSCRIBE rejected with status 403 (Forbidden)"))
	select {
	case err := <-service.RegistrationErrors():
		t.Fatalf("SUBSCRIBE failure tore down runtime: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if service.RegState() != regRegistered {
		t.Fatalf("registration state = %s, want %s", service.RegState(), regRegistered)
	}
}

func TestRegistrationSubscriptionTimeoutIsRecorded(t *testing.T) {
	// Advance the deadline only after the pipe request has had a chance to run.
	synctest.Test(t, func(t *testing.T) {
		service := newProtectedKeepaliveTestService(t)
		client, server := net.Pipe()
		service.activateProtectedRegistrationTCP(client)
		t.Cleanup(func() { _ = server.Close() })
		readDone := make(chan error, 1)
		go func() {
			_, err := readSIPStreamMessage(bufio.NewReader(server))
			readDone <- err
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := service.sendSubscribeReg(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sendSubscribeReg error = %v, want deadline exceeded", err)
		}
		if err := <-readDone; err != nil {
			t.Fatal(err)
		}
		service.mu.RLock()
		lastErr := service.subscriptionLastErr
		refreshAt := service.subscriptionRefreshAt
		service.mu.RUnlock()
		if !strings.Contains(lastErr, context.DeadlineExceeded.Error()) || refreshAt.IsZero() {
			t.Fatalf("subscription failure state = err %q refresh %s", lastErr, refreshAt)
		}
	})
}

func TestRegistrationSubscriptionStopsWithService(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	client, server := net.Pipe()
	service.activateProtectedRegistrationTCP(client)
	t.Cleanup(func() { _ = server.Close() })
	requestRead := make(chan error, 1)
	go func() {
		_, err := readSIPStreamMessage(bufio.NewReader(server))
		requestRead <- err
	}()
	result := make(chan error, 1)
	go func() { result <- service.sendSubscribeReg(context.Background()) }()
	if err := <-requestRead; err != nil {
		t.Fatal(err)
	}
	service.StopCurrent()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "service stopped") {
		t.Fatalf("sendSubscribeReg after Stop = %v", err)
	}
	if service.subscriptionInFlight.Load() {
		t.Fatal("Stop left SUBSCRIBE marked in flight")
	}
}

func TestRegistrationNotifyRepliesThenParsesReginfo(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	primeSubscriptionNotifyDialog(service, false)
	body := `<?xml version="1.0"?><reginfo xmlns="urn:ietf:params:xml:ns:reginfo">` +
		`<registration aor="sip:user@example"><contact id="registered-contact" state="terminated"><uri>sip:registered-contact@old.example</uri></contact></registration>` +
		`<registration aor="sip:+447840844894@o2.co.uk"><contact id="registered-contact" state="active"><uri>sip:registered-contact@new.example</uri></contact></registration></reginfo>`
	raw := registrationNotifyRequest(body)
	replied := make(chan string, 1)
	if err := service.dispatchInboundSIP(raw, func(response string) error {
		replied <- response
		return nil
	}); err != nil {
		t.Fatalf("dispatchInboundSIP: %v", err)
	}
	response := <-replied
	if !strings.HasPrefix(response, "SIP/2.0 200 OK") {
		t.Fatalf("NOTIFY response = %q", response)
	}
	waitForReginfoAOR(t, service, "sip:+447840844894@o2.co.uk")
	if service.notifyReconnectPending.Load() {
		t.Fatal("active binding did not override a stale terminated binding")
	}
}

func TestCollectReginfoStatsCountsBindingsWithoutIdentityData(t *testing.T) {
	body := []byte(`<reginfo><registration aor="sip:user@example">` +
		`<contact id="current" state="active"><uri>sip:current@new.example</uri></contact>` +
		`<contact id="stale" state="active"><uri>sip:stale@old.example</uri></contact>` +
		`<contact id="current" state="terminated"><uri>sip:current@old.example</uri></contact>` +
		`</registration></reginfo>`)
	document, err := parseReginfoXML(body)
	if err != nil {
		t.Fatal(err)
	}
	stats := collectReginfoStats(document, "current", "current")
	if stats.registrations != 1 || stats.contacts != 3 || stats.active != 2 ||
		stats.terminated != 1 || stats.currentActive != 1 || stats.currentTerminated != 1 {
		t.Fatalf("reginfo stats = %+v", stats)
	}
}

func TestDuplicateActiveRegistrationRequiresSameAORAndCurrentContact(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "duplicate in one registration",
			body: `<reginfo><registration aor="sip:user@example">` +
				`<contact id="current" state="active"><uri>sip:current@new.example</uri></contact>` +
				`<contact id="stale" state="active"><uri>sip:stale@old.example</uri></contact>` +
				`</registration></reginfo>`,
			want: true,
		},
		{
			name: "one binding for each public identity",
			body: `<reginfo><registration aor="sip:user@example">` +
				`<contact id="current" state="active"><uri>sip:current@one.example</uri></contact>` +
				`</registration><registration aor="sip:+44123@example">` +
				`<contact id="current" state="active"><uri>sip:current@two.example</uri></contact>` +
				`</registration></reginfo>`,
		},
		{
			name: "duplicates do not include current contact",
			body: `<reginfo><registration aor="sip:user@example">` +
				`<contact id="other-1" state="active"><uri>sip:other-1@one.example</uri></contact>` +
				`<contact id="other-2" state="active"><uri>sip:other-2@two.example</uri></contact>` +
				`</registration></reginfo>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := parseReginfoXML([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := hasDuplicateActiveRegistration(document, "current", "current"); got != test.want {
				t.Fatalf("hasDuplicateActiveRegistration = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRegistrationBindingCleanupOncePerIdentity(t *testing.T) {
	document, err := parseReginfoXML([]byte(`<reginfo><registration aor="sip:user@example">` +
		`<contact id="current" state="active"><uri>sip:current@new.example</uri></contact>` +
		`<contact id="stale" state="active"><uri>sip:stale@old.example</uri></contact>` +
		`</registration></reginfo>`))
	if err != nil {
		t.Fatal(err)
	}
	newService := func(preset, device string) *Service {
		return &Service{cfg: &IMSConfig{CarrierPresetID: preset, DeviceID: device, IMPU: "sip:user@example"},
			regSession: &registerSession{contactUser: "current", publicID: "sip:user@example", authHeader: "Digest auth"}}
	}
	cte := newService("CTEUK_23433", "cte")
	cte.cfg.DeviceID = t.Name() + "-cte"
	isolateRegistrationCleanupAttempt(t, cte)
	if !cte.requestRegistrationBindingCleanup(document) || !cte.bindingCleanupPending.Load() {
		t.Fatal("duplicate binding did not request cleanup")
	}
	service := newService(giffgaffCarrierPresetID, "giffgaff-once")
	service.cfg.DeviceID = t.Name() + "-giffgaff"
	isolateRegistrationCleanupAttempt(t, service)
	if !service.requestRegistrationBindingCleanup(document) || !service.bindingCleanupPending.Load() {
		t.Fatal("duplicate binding did not request cleanup")
	}
	if service.requestRegistrationBindingCleanup(document) {
		t.Fatal("duplicate binding requested cleanup more than once")
	}
	replacement := newService(giffgaffCarrierPresetID, service.cfg.DeviceID)
	if replacement.requestRegistrationBindingCleanup(document) {
		t.Fatal("replacement service repeated cleanup for the same identity")
	}
}

func isolateRegistrationCleanupAttempt(t *testing.T, service *Service) {
	t.Helper()
	service.mu.RLock()
	key := service.registrationBindingCleanupKeyLocked()
	service.mu.RUnlock()
	registrationCleanupAttempts.Delete(key)
	t.Cleanup(func() { registrationCleanupAttempts.Delete(key) })
}

func TestRegistrationNotifyDeduplicatesReRegistration(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	primeSubscriptionNotifyDialog(service, false)
	body := `<reginfo><registration aor="sip:+447840844894@o2.co.uk">` +
		`<contact id="registered-contact" state="terminated"><uri>sip:registered-contact@old.example</uri></contact>` +
		`</registration></reginfo>`
	raw := registrationNotifyRequest(body)
	service.handleRegistrationNotification(raw)
	service.handleRegistrationNotification(raw)
	if !service.notifyReconnectPending.Load() {
		t.Fatal("terminated binding did not schedule re-registration")
	}
	service.StopCurrent()
	if service.notifyReconnectPending.Load() {
		t.Fatal("Stop did not clear the pending reginfo re-registration")
	}
}

func TestMalformedRegistrationNotifyIsAcknowledgedWithoutMutation(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	primeSubscriptionNotifyDialog(service, false)
	raw := registrationNotifyRequest(`<reginfo><registration`)
	replied := make(chan string, 1)
	if err := service.dispatchInboundSIP(raw, func(response string) error {
		replied <- response
		return nil
	}); err != nil {
		t.Fatalf("dispatchInboundSIP: %v", err)
	}
	if response := <-replied; !strings.HasPrefix(response, "SIP/2.0 200 OK") {
		t.Fatalf("NOTIFY response = %q", response)
	}
	time.Sleep(10 * time.Millisecond)
	service.mu.RLock()
	aor := service.reginfoAOR
	service.mu.RUnlock()
	if aor != "" || service.notifyReconnectPending.Load() {
		t.Fatalf("malformed reginfo mutated state: aor=%q pending=%v", aor, service.notifyReconnectPending.Load())
	}
}

func TestReginfoAORPrefersTelephoneIdentityAndSummaryIsBounded(t *testing.T) {
	body := []byte(`<reginfo><registration aor="sip:user@example">` +
		`<contact id="1" state="active"><uri>sip:1@example</uri></contact>` +
		`<contact id="2" state="active"><uri>sip:2@example</uri></contact>` +
		`<contact id="3" state="active"><uri>sip:3@example</uri></contact>` +
		`<contact id="4" state="active"><uri>sip:4@example</uri></contact>` +
		`<contact id="5" state="active"><uri>sip:5@example</uri></contact>` +
		`<contact id="6" state="active"><uri>sip:6@example</uri></contact>` +
		`<contact id="7" state="active"><uri>sip:7@example</uri></contact>` +
		`</registration><registration aor="sip:+447840844894@o2.co.uk"/></reginfo>`)
	if got := extractReginfoAOR(body); got != "sip:+447840844894@o2.co.uk" {
		t.Fatalf("extractReginfoAOR = %q", got)
	}
	summary := summarizeReginfoXML(body)
	if strings.Contains(summary, "id=7,") || strings.Count(summary, "id=") != reginfoSummaryLimit {
		t.Fatalf("reginfo summary is not bounded to %d contacts: %q", reginfoSummaryLimit, summary)
	}
}

// Shutdown ends the reg-event subscription and stops there. Measured on
// 2026-09-03, 3 of 11 shutdown de-registrations timed out on the Contact
// expires=0 and escalated to Contact:*, which third-party de-registers the
// IP-SM-GW and stopped Vodafone pushing MT MESSAGE even after a clean
// re-REGISTER, so the binding is left to expire instead.
func TestStopUnsubscribesRegistrationWithoutDeregistering(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	service.mu.Lock()
	service.regSession.callID = "reg-call"
	service.regSession.expires = time.Hour
	service.mu.Unlock()
	client, server := net.Pipe()
	service.activateProtectedRegistrationTCP(client)
	t.Cleanup(func() { _ = server.Close() })

	seen := make(chan string, 4)
	go func() {
		defer close(seen)
		reader := bufio.NewReader(server)
		for {
			request, err := readSIPStreamMessage(reader)
			if err != nil {
				return
			}
			seen <- request
			expires := "120"
			if rawSIPHeaderValue(request, "Expires") == "0" {
				expires = "0"
			}
			if _, err = io.WriteString(server, subscriptionWireResponse(request, 200, "Expires: "+expires+"\r\n")); err != nil {
				return
			}
		}
	}()
	if err := service.sendSubscribeReg(context.Background()); err != nil {
		t.Fatalf("sendSubscribeReg: %v", err)
	}
	initial := <-seen
	if err := service.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	unsubscribe, ok := <-seen
	if !ok {
		t.Fatal("shutdown closed the transport without sending unsubscribe")
	}
	assertRegistrationSubscription(t, unsubscribe, 6, 0, "reg-notifier")
	if rawSIPHeaderValue(unsubscribe, "Call-ID") != rawSIPHeaderValue(initial, "Call-ID") {
		t.Fatal("unsubscribe used a different Call-ID")
	}
	select {
	case extra, ok := <-seen:
		if ok {
			t.Fatalf("shutdown sent %q after unsubscribing",
				strings.SplitN(extra, "\r\n", 2)[0])
		}
	case <-time.After(200 * time.Millisecond):
	}
	if service.hasSubscriptionDialog() || !service.subscriptionClosed {
		t.Fatalf("subscription after stop dialog=%v closed=%v",
			service.hasSubscriptionDialog(), service.subscriptionClosed)
	}
}

func TestUnsubscribeSkippedWithoutDialog(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	service.mu.Lock()
	service.regSession.callID = "reg-call"
	service.regSession.expires = time.Hour
	service.mu.Unlock()
	requests := make(chan string, 1)
	service.transport.SetSendFn(func(request string) error {
		requests <- request
		service.transport.DeliverResponse(registerResponseForRequest(request, 200, nil))
		return nil
	})
	if err := service.Unregister(context.Background()); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	select {
	case request := <-requests:
		if !strings.HasPrefix(request, "REGISTER ") || sipHeaderValue(request, "Expires") != "0" {
			t.Fatalf("unexpected shutdown request %q", request)
		}
	default:
		t.Fatal("Unregister sent no REGISTER")
	}
	select {
	case request := <-requests:
		t.Fatalf("extra request after Unregister: %q", request)
	default:
	}
}

func TestNotifyLearnsSubscriptionDialogAndTerminatedClosesIt(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	primeSubscriptionNotifyDialog(service, false)
	replied := make(chan string, 1)
	active := registrationNotifyRequestWithState(
		`<reginfo><registration aor="sip:+447840844894@o2.co.uk">`+
			`<contact id="registered-contact" state="active"><uri>sip:registered-contact@new.example</uri></contact>`+
			`</registration></reginfo>`,
		"active;expires=3600",
	)
	if err := service.dispatchInboundSIP(active, func(response string) error {
		replied <- response
		return nil
	}); err != nil {
		t.Fatalf("active NOTIFY: %v", err)
	}
	if !strings.HasPrefix(<-replied, "SIP/2.0 200 OK") {
		t.Fatal("active NOTIFY was not acknowledged")
	}
	waitForReginfoAOR(t, service, "sip:+447840844894@o2.co.uk")
	if !service.hasSubscriptionDialog() {
		t.Fatal("NOTIFY did not learn the subscription remote tag")
	}
	service.mu.RLock()
	dialog := service.subscriptionDialog
	service.mu.RUnlock()
	if dialog.remoteTag != "server" || dialog.localTag != "client" {
		t.Fatalf("learned dialog = %+v", dialog)
	}

	terminated := strings.Replace(
		registrationNotifyRequestWithState(
			`<reginfo><registration aor="sip:+447840844894@o2.co.uk">`+
				`<contact id="registered-contact" state="active"><uri>sip:registered-contact@new.example</uri></contact>`+
				`</registration></reginfo>`,
			"terminated;reason=timeout",
		),
		"CSeq: 1 NOTIFY",
		"CSeq: 2 NOTIFY",
		1,
	)
	if err := service.dispatchInboundSIP(terminated, func(response string) error {
		replied <- response
		return nil
	}); err != nil {
		t.Fatalf("terminated NOTIFY: %v", err)
	}
	<-replied
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !registrationSubscriptionClosed(service) {
		time.Sleep(time.Millisecond)
	}
	closed := registrationSubscriptionClosed(service)
	if service.hasSubscriptionDialog() || !closed {
		t.Fatalf("terminated NOTIFY left dialog=%v closed=%v",
			service.hasSubscriptionDialog(), closed)
	}
}

func TestSubscriptionStateTerminatedParsesHeader(t *testing.T) {
	raw := registrationNotifyRequestWithState("<reginfo/>", "terminated;reason=timeout")
	if got := rawSIPHeaderValue(raw, "Subscription-State"); got != "terminated;reason=timeout" {
		t.Fatalf("Subscription-State = %q", got)
	}
	state, err := parseSubscriptionNotifyState(raw)
	if err != nil || state.state != "terminated" {
		t.Fatal("terminated subscription-state was not recognized")
	}
	state, err = parseSubscriptionNotifyState(registrationNotifyRequestWithState("<reginfo/>", "active;expires=3600"))
	if err != nil || state.state != "active" {
		t.Fatal("active subscription-state was treated as terminated")
	}
}

func TestSubscriptionRefreshDelayMatches3GPP(t *testing.T) {
	if got := subscriptionRefreshDelay(time.Hour); got != 50*time.Minute {
		t.Fatalf("hour subscription refresh delay = %s", got)
	}
	if got := subscriptionRefreshDelay(time.Minute); got != 30*time.Second {
		t.Fatalf("one-minute subscription refresh delay = %s, want half lifetime", got)
	}
}

func registrationNotifyRequest(body string) string {
	return registrationNotifyRequestWithState(body, "active;expires=3600")
}

func registrationNotifyRequestWithState(body, subscriptionState string) string {
	headers := "Event: reg\r\nContent-Type: application/reginfo+xml;charset=UTF-8\r\n"
	if subscriptionState != "" {
		headers += "Subscription-State: " + subscriptionState + "\r\n"
	}
	return "NOTIFY sip:user@example SIP/2.0\r\n" +
		"Via: SIP/2.0/TCP 192.0.2.1:6060;branch=z9hG4bK-notify\r\n" +
		"From: <sip:server@example>;tag=server\r\n" +
		"To: <sip:user@example>;tag=client\r\n" +
		"Call-ID: notify-call\r\nCSeq: 1 NOTIFY\r\n" +
		headers +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func waitForSubscriptionDialog(t *testing.T, service *Service, ready bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if service.hasSubscriptionDialog() == ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subscription dialog ready=%v, want %v", service.hasSubscriptionDialog(), ready)
}

func registrationSubscriptionClosed(service *Service) bool {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.subscriptionClosed
}

func waitForReginfoAOR(t *testing.T, service *Service, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.mu.RLock()
		got := service.reginfoAOR
		service.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reginfo AOR was not updated to %q", want)
}

func TestRecordSubscriptionResultClosesOnPermanentReject(t *testing.T) {
	for _, status := range []int{403, 405, 489} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			service := newProtectedKeepaliveTestService(t)
			if err := service.recordSubscriptionAttempt(subscriptionResult{context: service.subscriptionAttemptContextLocked(false), requestedExpires: time.Hour}); err != nil {
				t.Fatal(err)
			}
			err := service.recordSubscriptionResult(subscriptionResult{
				context:          service.subscriptionAttemptContextLocked(false),
				response:         &sip.Response{StatusCode: status, Reason: "rejected"},
				requestedExpires: time.Hour, err: fmt.Errorf("SUBSCRIBE rejected with status %d", status),
			})
			if err == nil {
				t.Fatal("expected permanent reject error")
			}
			service.mu.RLock()
			closed := service.subscriptionClosed
			refresh := service.subscriptionRefreshAt
			service.mu.RUnlock()
			if !closed || !refresh.IsZero() {
				t.Fatalf("closed=%t refresh=%v", closed, refresh)
			}
			now := time.Now()
			service.mu.Lock()
			service.registrationRefreshAt = now.Add(time.Hour)
			service.lastPingAt = now
			service.mu.Unlock()
			if got := service.nextIMSMaintenanceAction(now); got == imsMaintenanceSubscribe {
				t.Fatal("permanent reject still scheduled SUBSCRIBE")
			}
		})
	}
}

func TestRecordSubscriptionResultKeepsRetryOnTemporaryReject(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	if err := service.recordSubscriptionAttempt(subscriptionResult{context: service.subscriptionAttemptContextLocked(false), requestedExpires: time.Hour}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	err := service.recordSubscriptionResult(subscriptionResult{
		context:          service.subscriptionAttemptContextLocked(false),
		response:         &sip.Response{StatusCode: 503, Reason: "Service Unavailable"},
		requestedExpires: time.Hour, err: errors.New("SUBSCRIBE rejected with status 503"),
	})
	if err == nil {
		t.Fatal("expected 503 error")
	}
	service.mu.RLock()
	closed := service.subscriptionClosed
	refresh := service.subscriptionRefreshAt
	service.mu.RUnlock()
	if closed || refresh.IsZero() || !refresh.After(before) {
		t.Fatalf("temporary reject closed=%t refresh=%v before=%v", closed, refresh, before)
	}
}

func TestSubscribeReg405StopsPeriodicRefresh(t *testing.T) {
	service := newProtectedKeepaliveTestService(t)
	client, server := net.Pipe()
	service.activateProtectedRegistrationTCP(client)
	t.Cleanup(func() { _ = server.Close() })
	errorsSeen := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		request, err := readSIPStreamMessage(reader)
		if err != nil {
			errorsSeen <- err
			return
		}
		_, err = io.WriteString(server, subscriptionWireResponse(request, 405, ""))
		errorsSeen <- err
	}()
	err := service.sendSubscribeReg(context.Background())
	if err == nil || !strings.Contains(err.Error(), "405") {
		t.Fatalf("sendSubscribeReg error = %v", err)
	}
	if err := <-errorsSeen; err != nil {
		t.Fatal(err)
	}
	service.mu.RLock()
	closed := service.subscriptionClosed
	refresh := service.subscriptionRefreshAt
	service.mu.RUnlock()
	if !closed || !refresh.IsZero() {
		t.Fatalf("closed=%t refresh=%v", closed, refresh)
	}
}

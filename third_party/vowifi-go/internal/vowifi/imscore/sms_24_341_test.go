package imscore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/internal/smscodec"
	"github.com/iniwex5/vowifi-go/internal/vowifi/events"
	"github.com/warthog618/sms/encoding/tpdu"
)

func TestParseSMSDestinationMSISDNLessAndPhone(t *testing.T) {
	tests := []struct {
		in         string
		msisdnLess bool
		display    string
		tpDA       string
	}{
		{in: "+447700900123", display: "+447700900123", tpDA: "+447700900123"},
		{in: "sip:+447700900123@ims.example;user=phone", display: "+447700900123", tpDA: "+447700900123"},
		{in: "sip:alice@home.net", msisdnLess: true, display: "sip:alice@home.net", tpDA: smscodec.DummyMSISDN},
		{in: "tel:+447700900123", display: "+447700900123", tpDA: "+447700900123"},
	}
	for _, test := range tests {
		got, err := parseSMSDestination(test.in)
		if err != nil {
			t.Fatalf("%s: %v", test.in, err)
		}
		if got.msisdnLess() != test.msisdnLess || got.display != test.display || got.tpDA != test.tpDA {
			t.Fatalf("%s: %+v", test.in, got)
		}
	}
}

func TestNotifySMSMemoryAvailableUsesLastIPSMGW(t *testing.T) {
	service, _, outbound := newInboundSMSTestService(t)
	service.smsRandom = bytes.NewReader([]byte{0x2a})
	service.rememberSMSMemoryDenied(
		"MESSAGE sip:user@ims.example SIP/2.0\r\n" +
			"P-Asserted-Identity: <sip:ipsmgw@ims.example>\r\n" +
			"Call-ID: denied\r\nCSeq: 1 MESSAGE\r\nContent-Length: 0\r\n\r\n",
	)
	if err := service.NotifySMSMemoryAvailable(); err != nil {
		t.Fatal(err)
	}
	request := waitForOutboundSMSControl(t, outbound)
	if !strings.HasPrefix(request, "MESSAGE sip:ipsmgw@ims.example SIP/2.0") {
		t.Fatalf("SMMA target = %q", strings.SplitN(request, "\r\n", 2)[0])
	}
	if got := rawSIPHeaderValue(request, "In-Reply-To"); got != "" {
		t.Fatalf("RP-SMMA In-Reply-To = %q", got)
	}
	if got := rawSIPHeaderValue(request, "Content-Type"); got != imsSMSContentType {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rawSIPHeaderValue(request, "Content-Transfer-Encoding"); got != "binary" {
		t.Fatalf("Content-Transfer-Encoding = %q", got)
	}
	if got := rawSIPHeaderValue(request, "Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want omitted", got)
	}
	assertSMSOverIPServiceHeaders(t, request)
	body, err := rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, smscodec.BuildRPSMMA(0x2a)) {
		t.Fatalf("RP-SMMA body = %x", body)
	}
}

func TestInboundMemoryFullSendsRPError22ThenSMMA(t *testing.T) {
	service, subscriber, outbound := newInboundSMSTestService(t)
	service.smsRandom = bytes.NewReader([]byte{0x2b})
	service.SetSMSMemoryFull(true)
	raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x41, "+447700900123", "held"))
	raw = strings.Replace(raw, "From: <sip:+447802002606@ims.example>;tag=remote\r\n",
		"P-Asserted-Identity: <sip:ipsmgw@ims.example>\r\n"+
			"From: <sip:+447802002606@ims.example>;tag=remote\r\n", 1)
	var response string
	if err := service.dispatchInboundSIP(raw, func(value string) error {
		response = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response, "SIP/2.0 200") {
		t.Fatalf("SIP response = %q", response)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("memory-full SMS published %#v", event)
	default:
	}
	request := waitForOutboundSMSControl(t, outbound)
	body, err := rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, smscodec.BuildRPError(0x41, smscodec.RPCauseMemoryCapacityExceeded)) {
		t.Fatalf("RP-ERROR body = %x", body)
	}
	service.SetSMSMemoryFull(false)
	smma := waitForOutboundSMSControl(t, outbound)
	if !strings.HasPrefix(smma, "MESSAGE sip:ipsmgw@ims.example SIP/2.0") {
		t.Fatalf("SMMA target = %q", strings.SplitN(smma, "\r\n", 2)[0])
	}
	smmaBody, err := rawSIPBody(smma)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(smmaBody, smscodec.BuildRPSMMA(0x2b)) {
		t.Fatalf("RP-SMMA body = %x", smmaBody)
	}
}

func TestNotifySMSMemoryAvailableRetriesRejectedFinalResponses(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	service.smsRandom = bytes.NewReader([]byte{0x2c})
	service.rememberSMSMemoryDenied(
		"MESSAGE sip:user@ims.example SIP/2.0\r\n" +
			"P-Asserted-Identity: <sip:ipsmgw@ims.example>\r\n" +
			"Call-ID: denied\r\nCSeq: 1 MESSAGE\r\nContent-Length: 0\r\n\r\n",
	)
	attempts := 0
	service.transport.SetSendFn(func(request string) error {
		attempts++
		status := 503
		if attempts == rpReportMaxAttempts {
			status = 200
		}
		service.transport.DeliverResponse(registerResponseForRequest(request, status, nil))
		return nil
	})
	if err := service.sendRPSMMAWithRetryPolicy(0, 0); err != nil {
		t.Fatal(err)
	}
	if attempts != rpReportMaxAttempts {
		t.Fatalf("SMMA attempts = %d, want %d", attempts, rpReportMaxAttempts)
	}
}

func TestRejectedRPReportDoesNotSendMemoryAvailable(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	var kinds []smscodec.RPDUKind
	service.transport.SetSendFn(func(request string) error {
		body, err := rawSIPBody(request)
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, smscodec.ClassifyRPDU(body).Kind)
		service.transport.DeliverResponse(registerResponseForRequest(request, 488, nil))
		return nil
	})
	raw := inboundSMSRequest(t, imsSMSContentType,
		inboundRPData(t, 0x41, "+447700900123", "rejected report"))
	service.sendRPReportWithRetry(rpReportRequest{
		Inbound: raw, Body: smscodec.BuildRPAck(0x41), RPMR: 0x41,
	})
	if len(kinds) != 1 || kinds[0] != smscodec.RPDUKindAck {
		t.Fatalf("outbound RPDU kinds = %v, want only RP-ACK", kinds)
	}
}

func TestNotifySMSMemoryAvailableAbortOnStop(t *testing.T) {
	service, _, _ := newInboundSMSTestService(t)
	service.rememberSMSMemoryDenied(
		"MESSAGE sip:user@ims.example SIP/2.0\r\n" +
			"P-Asserted-Identity: <sip:ipsmgw@ims.example>\r\n" +
			"Call-ID: denied\r\nCSeq: 1 MESSAGE\r\nContent-Length: 0\r\n\r\n",
	)
	service.StopCurrent()
	if err := service.sendRPSMMAWithRetryPolicy(0, 0); !errors.Is(err, errRPReportAborted) {
		t.Fatalf("stopped SMMA = %v", err)
	}
}

func TestInboundPersistFailureSendsTemporaryErrorWithoutBlockingNextSMS(t *testing.T) {
	service, subscriber, outbound := newInboundSMSTestService(t)
	persistCalls := 0
	subscriber.onEvent = func(event events.Event) {
		if received, ok := event.(*events.EventSMSReceived); ok {
			persistCalls++
			if persistCalls == 1 {
				received.PersistenceResult <- errors.New("database temporarily unavailable")
				return
			}
			received.PersistenceResult <- nil
		}
	}
	first := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x61, "+447700900123", "retry me"))
	if err := service.dispatchInboundSIP(first, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	<-subscriber.events
	request := waitForOutboundSMSControl(t, outbound)
	body, err := rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, smscodec.BuildRPError(0x61, smscodec.RPCauseTemporaryFailure)) {
		t.Fatalf("persist-failure RP body = %x", body)
	}

	second := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x62, "+447700900123", "store me"))
	if err := service.dispatchInboundSIP(second, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	<-subscriber.events
	request = waitForOutboundSMSControl(t, outbound)
	body, err = rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, smscodec.BuildRPAck(0x62)) {
		t.Fatalf("next-message RP body = %x", body)
	}
}

func TestOutboundSIPURIUsesMultipartDummyMSISDN(t *testing.T) {
	service, subscriber, _ := newOutboundSMSTestService(t)
	requests := make(chan string, 1)
	service.transport.SetSendFn(func(request string) error {
		requests <- request
		service.transport.DeliverResponse(registerResponseForRequest(request, 200, nil))
		return nil
	})
	outcome, err := service.SendSMSWithResult(context.Background(), "sip:alice@home.net", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.PartsTotal != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	request := waitForOutboundSMSControl(t, requests)
	if !strings.HasPrefix(request, "MESSAGE sip:+447802002606@ims.example;user=phone SIP/2.0") {
		t.Fatalf("request URI = %q", strings.SplitN(request, "\r\n", 2)[0])
	}
	contentType := rawSIPHeaderValue(request, "Content-Type")
	if !strings.HasPrefix(contentType, "multipart/mixed") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if got := rawSIPHeaderValue(request, "Content-Transfer-Encoding"); got != "" {
		t.Fatalf("outer CTE = %q", got)
	}
	body, err := rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := extractIMSSMSPayload(contentType, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.xml.To != "sip:alice@home.net" {
		t.Fatalf("xml To = %q", payload.xml.To)
	}
	_, _, destination, submit, err := smscodec.ParseRPDataWithAddresses(payload.rpdu)
	if err != nil {
		t.Fatal(err)
	}
	if destination != "+447802002606" {
		t.Fatalf("RP-DA = %q", destination)
	}
	decoded := &tpdu.TPDU{Direction: tpdu.MO}
	if err := decoded.UnmarshalBinary(submit); err != nil {
		t.Fatal(err)
	}
	if !smscodec.IsDummyMSISDN(decoded.DA.Number()) {
		t.Fatalf("TP-DA = %q", decoded.DA.Number())
	}
	assertAcceptedEvent(t, subscriber, outcome.MessageID)
}

func TestInboundMSISDNLessMultipartUsesXMLFromAndMultipartAck(t *testing.T) {
	service, subscriber, outbound := newInboundSMSTestService(t)
	rpdu := inboundRPData(t, 0x51, smscodec.DummyMSISDN, "hello")
	contentType, body, err := buildMSISDNLessSMSPayload(shortMessageInfo{From: "sip:alice@home.net"}, rpdu)
	if err != nil {
		t.Fatal(err)
	}
	raw := inboundSMSRequest(t, contentType, body)
	raw = strings.Replace(raw, "CSeq:",
		"Feature-Caps: *;+g.3gpp.smsip-msisdn-less\r\n"+
			"P-Asserted-Identity: <sip:ipsmgw@ims.example>\r\nCSeq:", 1)
	if err := service.dispatchInboundSIP(raw, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-subscriber.events:
		received, ok := event.(*events.EventSMSReceived)
		if !ok || received.Sender != "sip:alice@home.net" || received.Content != "hello" {
			t.Fatalf("received event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("inbound MSISDN-less SMS event was not published")
	}
	request := waitForOutboundSMSControl(t, outbound)
	if got := rawSIPHeaderValue(request, "In-Reply-To"); got != "inbound-sms" {
		t.Fatalf("In-Reply-To = %q", got)
	}
	if got := rawSIPHeaderValue(request, "Call-ID"); got == "" || got == "inbound-sms" {
		t.Fatalf("Call-ID = %q, want a fresh UAC Call-ID", got)
	}
	ackType := rawSIPHeaderValue(request, "Content-Type")
	if !strings.HasPrefix(ackType, "multipart/mixed") {
		t.Fatalf("RP-ACK Content-Type = %q", ackType)
	}
	ackBody, err := rawSIPBody(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := extractIMSSMSPayload(ackType, ackBody)
	if err != nil {
		t.Fatal(err)
	}
	if payload.xml.To != "sip:alice@home.net" {
		t.Fatalf("RP-ACK xml To = %q", payload.xml.To)
	}
	if !bytes.Equal(payload.rpdu, smscodec.BuildRPAck(0x51)) {
		t.Fatalf("RP-ACK body = %x", payload.rpdu)
	}
}

func TestInboundDummyMSISDNWithoutFeatureCapsIsDiscarded(t *testing.T) {
	service, subscriber, outbound := newInboundSMSTestService(t)
	raw := inboundSMSRequest(t, imsSMSContentType, inboundRPData(t, 0x52, smscodec.DummyMSISDN, "hello"))
	var response string
	if err := service.dispatchInboundSIP(raw, func(value string) error {
		response = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response, "SIP/2.0 200") {
		t.Fatalf("SIP response = %q", response)
	}
	select {
	case event := <-subscriber.events:
		t.Fatalf("discarded dummy SMS published %#v", event)
	case request := <-outbound:
		t.Fatalf("discarded dummy SMS sent RP control %q", request)
	case <-time.After(50 * time.Millisecond):
	}
}

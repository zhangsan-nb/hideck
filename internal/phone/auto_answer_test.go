package phone

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost/voicehost"
	"github.com/pion/rtp"
)

type promptDecoderStub struct {
	pcm        []int16
	sampleRate int
	err        error
}

func (d promptDecoderStub) DecodePCM(context.Context, string) ([]int16, int, error) {
	return append([]int16(nil), d.pcm...), d.sampleRate, d.err
}

func TestAutoAnswerWaitsThenPlaysPromptWithoutBrowser(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	gateway := newFakeVoiceGateway()
	service, err := NewService(ServiceOptions{
		Gateway: gateway, Store: newMemoryCallStore(), RecordingDir: t.TempDir(),
		WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: promptDecoderStub{
			pcm: make([]int16, browserSamplesPerFrame*2), sampleRate: 8000,
		},
		AutoAnswer: AutoAnswerConfig{AudioPath: "greeting.wav", Devices: map[string]AutoAnswerDeviceConfig{
			"dev-1": {Enabled: true, Delay: 10 * time.Millisecond},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Close(ctx)
	})

	gateway.emitIncoming(voicehost.IncomingCall{
		DeviceID: "dev-1", CallID: "auto-1", Caller: "10010", ReceivedAt: time.Now(),
		OfferSDP: fmt.Sprintf("v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio %d RTP/AVP 0\r\n", target.LocalAddr().(*net.UDPAddr).Port),
	})

	var request voicehost.AnswerRequest
	select {
	case request = <-gateway.answerCalls:
		if request.CallID != "auto-1" || request.SDP == "" {
			t.Fatalf("answer request = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("auto-answer did not answer the incoming call")
	}
	buffer := make([]byte, 2048)
	if err := target.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.ReadFromUDP(buffer); err != nil {
		t.Fatalf("auto-answer sent no RTP: %v", err)
	}
	inboundTarget, err := parseRTPEndpoint(request.SDP)
	if err != nil {
		t.Fatal(err)
	}
	writeUDPRTP(t, target, inboundTarget.Address, &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 0, SequenceNumber: 1, Timestamp: 160, SSRC: 7,
	}, Payload: make([]byte, browserSamplesPerFrame)})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.mu.RLock()
		call := service.calls["auto-1"]
		mediaID := ""
		if call != nil {
			mediaID = call.mediaID
		}
		service.mu.RUnlock()
		media := service.media.Get(mediaID)
		if media != nil && media.Stats().PacketsFromIMS > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("auto-answer did not receive the caller RTP")
}

func TestManualAnswerCancelsPendingAutoAnswer(t *testing.T) {
	gateway := newFakeVoiceGateway()
	service, err := NewService(ServiceOptions{
		Gateway: gateway, Store: newMemoryCallStore(), RecordingDir: t.TempDir(),
		WebRTCUDPAddress: "127.0.0.1:0", PromptDecoder: promptDecoderStub{
			pcm: make([]int16, browserSamplesPerFrame), sampleRate: 8000,
		},
		AutoAnswer: AutoAnswerConfig{AudioPath: "greeting.wav", Devices: map[string]AutoAnswerDeviceConfig{
			"dev-1": {Enabled: true, Delay: 50 * time.Millisecond},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	gateway.emitIncoming(voicehost.IncomingCall{
		DeviceID: "dev-1", CallID: "manual-wins", Caller: "10010", ReceivedAt: time.Now(), OfferSDP: testPlainSDP,
	})
	addStubMedia(t, service, "media-1", "admin", "lease-1")
	if _, err := service.Answer(context.Background(), ControlRequest{
		Owner: "admin", CallID: "manual-wins", MediaID: "media-1", Lease: "lease-1",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gateway.answerCalls:
	case <-time.After(time.Second):
		t.Fatal("manual answer did not reach the gateway")
	}
	select {
	case request := <-gateway.answerCalls:
		t.Fatalf("pending auto-answer also answered the call: %+v", request)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAutoAnswerIsDisabledByDefault(t *testing.T) {
	gateway := newFakeVoiceGateway()
	_ = newPhoneTestService(t, gateway, newMemoryCallStore(), time.Second)
	gateway.emitIncoming(voicehost.IncomingCall{
		DeviceID: "dev-1", CallID: "manual-1", Caller: "10010", ReceivedAt: time.Now(),
		OfferSDP: testPlainSDP,
	})
	select {
	case request := <-gateway.answerCalls:
		t.Fatalf("unexpected automatic answer: %+v", request)
	case <-time.After(50 * time.Millisecond):
	}
	gateway.emitEvent(voicehost.CallEvent{
		Type: "CallCanceled", DeviceID: "dev-1", CallID: "manual-1", Reason: "remote_cancel", Time: time.Now(),
	})
}

func TestAutoAnswerOnlyAppliesToEnabledDevice(t *testing.T) {
	gateway := newFakeVoiceGateway()
	service, err := NewService(ServiceOptions{
		Gateway: gateway, Store: newMemoryCallStore(), WebRTCUDPAddress: "127.0.0.1:0",
		PromptDecoder: promptDecoderStub{pcm: make([]int16, browserSamplesPerFrame), sampleRate: 8000},
		AutoAnswer: AutoAnswerConfig{AudioPath: "greeting.wav", Devices: map[string]AutoAnswerDeviceConfig{
			"dev-enabled":  {Enabled: true, Delay: 10 * time.Millisecond},
			"dev-disabled": {Enabled: false, Delay: 10 * time.Millisecond},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	gateway.emitIncoming(voicehost.IncomingCall{
		DeviceID: "dev-disabled", CallID: "disabled-device", Caller: "10010", OfferSDP: testPlainSDP,
	})
	select {
	case request := <-gateway.answerCalls:
		t.Fatalf("disabled device was automatically answered: %+v", request)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRemovingDeviceSettingsCancelsPendingAutoAnswer(t *testing.T) {
	gateway := newFakeVoiceGateway()
	service, err := NewService(ServiceOptions{
		Gateway: gateway, Store: newMemoryCallStore(), WebRTCUDPAddress: "127.0.0.1:0",
		PromptDecoder: promptDecoderStub{pcm: make([]int16, browserSamplesPerFrame), sampleRate: 8000},
		AutoAnswer: AutoAnswerConfig{AudioPath: "greeting.wav", Devices: map[string]AutoAnswerDeviceConfig{
			"dev-1": {Enabled: true, Delay: 30 * time.Millisecond},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	gateway.emitIncoming(voicehost.IncomingCall{
		DeviceID: "dev-1", CallID: "removed-device", Caller: "10010", OfferSDP: testPlainSDP,
	})
	service.RemoveAutoAnswerDevice("dev-1")
	select {
	case request := <-gateway.answerCalls:
		t.Fatalf("removed device was automatically answered: %+v", request)
	case <-time.After(75 * time.Millisecond):
	}
}

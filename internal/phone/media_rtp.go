package phone

import (
	"errors"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/yibaiba/hideck/pkg/logger"
)

const (
	mediaPacketBuffer      = 32
	jitterTick             = 20 * time.Millisecond
	browserClockRate       = 8000
	audioFramesPerSecond   = 50
	browserSamplesPerFrame = browserClockRate / audioFramesPerSecond
)

func (s *MediaSession) forwardBrowserRTP(track *webrtc.TrackRemote) {
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		count := s.browserPackets.Add(1)
		if count == 1 {
			logger.Info("浏览器首个音频包", "media_id", s.ID, "codec", track.Codec().MimeType, "payload_bytes", len(packet.Payload))
		}
		for _, sample := range decodePCMU(packet.Payload) {
			magnitude := int64(sample)
			if magnitude < 0 {
				magnitude = -magnitude
			}
			for old := s.browserPeak.Load(); uint64(magnitude) > old; old = s.browserPeak.Load() {
				if s.browserPeak.CompareAndSwap(old, uint64(magnitude)) {
					break
				}
			}
		}
		endpoint, codec, ok := s.endpoint()
		if !ok {
			continue
		}
		s.recordMixedFrame(mixToIMS, packet.Payload)
		payloadBytes := len(packet.Payload)
		packet.Payload, err = browserPayloadForIMS(packet.Payload, endpoint, codec)
		if err != nil {
			if s.browserRejected.Add(1) == 1 {
				logger.Warn("浏览器音频包未转发", "media_id", s.ID, "payload_bytes", payloadBytes, "err", err)
			}
			s.lost.Add(1)
			continue
		}
		packet.PayloadType = endpoint.PayloadType
		packet.Timestamp = scaleTimestamp(packet.Timestamp, browserClockRate, endpoint.ClockRate)
		raw, err := packet.Marshal()
		if err != nil {
			continue
		}
		if _, err := s.rtpConn.WriteToUDP(raw, endpoint.Address); err == nil {
			s.toIMS.Add(1)
		}
	}
}

func (s *MediaSession) forwardIMSRTP() {
	packets := make(chan *rtp.Packet, mediaPacketBuffer)
	go s.readIMSRTP(packets)
	ticker := time.NewTicker(jitterTick)
	defer ticker.Stop()
	buffer := newIMSJitterBuffer()
	var clock browserRTPClock
	for {
		select {
		case packet := <-packets:
			previousSSRC, previousExpected := buffer.ssrc, buffer.expected
			switch buffer.push(packet) {
			case imsPacketLate:
				// Its slot was already counted as lost when it was skipped.
				s.imsLate.Add(1)
			case imsStreamRestarted:
				if s.imsRestarts.Add(1) == 1 {
					logger.Info("IMS 下行语音换流，重新排队", "media_id", s.ID,
						"previous_ssrc", previousSSRC, "ssrc", packet.SSRC,
						"expected_seq", previousExpected, "seq", packet.SequenceNumber)
				}
			}
		case <-ticker.C:
			packet, gap := buffer.pop()
			switch {
			case gap:
				s.lost.Add(1)
				clock.skip()
			case packet == nil:
				clock.idle()
			default:
				s.writeBrowserRTP(packet, &clock)
			}
		case <-s.closed:
			return
		}
	}
}

type imsPushResult int

const (
	imsPacketQueued imsPushResult = iota
	imsPacketLate
	imsStreamRestarted
)

// imsJitterBuffer plays one RTP stream in sequence order. It only holds packets
// of the current SSRC within one buffer length ahead of the next one to play:
// a packet left behind (another SSRC, or a sequence already skipped) would keep
// the buffer non-empty, so every tick would skip the next packet as lost.
// Early media can interleave a second SSRC before the answer; switching to it
// restarts the buffer, and switching back restarts it again.
type imsJitterBuffer struct {
	packets  map[uint16]*rtp.Packet
	ssrc     uint32
	expected uint16
	started  bool
}

func newIMSJitterBuffer() *imsJitterBuffer {
	return &imsJitterBuffer{packets: make(map[uint16]*rtp.Packet, mediaPacketBuffer)}
}

func (b *imsJitterBuffer) push(packet *rtp.Packet) imsPushResult {
	offset := int(int16(packet.SequenceNumber - b.expected))
	result := imsPacketQueued
	if !b.started || packet.SSRC != b.ssrc || offset >= mediaPacketBuffer || offset < -mediaPacketBuffer {
		if b.started {
			result = imsStreamRestarted
		}
		clear(b.packets)
		b.ssrc, b.expected, b.started = packet.SSRC, packet.SequenceNumber, true
		offset = 0
	}
	if offset < 0 {
		return imsPacketLate
	}
	b.packets[packet.SequenceNumber] = packet
	return result
}

// pop returns the packet due on this tick. gap reports a missing packet that
// was skipped because later ones are already buffered.
func (b *imsJitterBuffer) pop() (packet *rtp.Packet, gap bool) {
	if len(b.packets) == 0 {
		return nil, false
	}
	packet = b.packets[b.expected]
	delete(b.packets, b.expected)
	b.expected++
	return packet, packet == nil
}

// browserRTPClock numbers the packets sent to the browser. The browser track
// keeps one SSRC for the whole call, so a new IMS stream must not move its
// sequence numbers backwards: SRTP would reject the rest of the call as replayed.
// It starts from the first IMS packet. The sequence advances once per packet,
// lost ones too; the timestamp also advances on ticks with nothing to play
// (DTX silence, a restarted stream), so it keeps up with real time and the
// browser does not discard the speech that follows as late.
type browserRTPClock struct {
	sequence     uint16
	timestamp    uint32
	frameSamples uint32
	started      bool
}

func (c *browserRTPClock) stamp(packet *rtp.Packet, samples int) {
	if !c.started {
		c.sequence, c.timestamp, c.started = packet.SequenceNumber, packet.Timestamp, true
	}
	packet.SequenceNumber, packet.Timestamp = c.sequence, c.timestamp
	c.sequence++
	c.frameSamples = uint32(samples)
	c.timestamp += c.frameSamples
}

// skip accounts for a lost packet, assumed as long as the last one played.
func (c *browserRTPClock) skip() {
	if c.started {
		c.sequence++
		c.timestamp += c.frameSamples
	}
}

// idle accounts for a tick with nothing to play.
func (c *browserRTPClock) idle() {
	if c.started {
		c.timestamp += browserSamplesPerFrame
	}
}

func (s *MediaSession) writeBrowserRTP(packet *rtp.Packet, clock *browserRTPClock) {
	endpoint, codec, ok := s.endpoint()
	if !ok {
		return
	}
	payloadBytes := len(packet.Payload)
	payload, err := imsPayloadForBrowser(packet.Payload, endpoint, codec)
	if err != nil {
		if s.imsRejected.Add(1) == 1 {
			logger.Warn("IMS 下行语音包未转发", "media_id", s.ID, "codec", endpoint.Codec, "payload_type", packet.PayloadType, "payload_bytes", payloadBytes, "err", err)
		}
		s.lost.Add(1)
		clock.skip()
		return
	}
	packet.Payload = payload
	s.recordMixedFrame(mixFromIMS, payload)
	if s.track == nil {
		s.fromIMS.Add(1)
		return
	}
	packet.PayloadType = pcmPayloadType
	packet.Timestamp = scaleTimestamp(packet.Timestamp, endpoint.ClockRate, browserClockRate)
	clock.stamp(packet, len(payload))
	if err := s.track.WriteRTP(packet); err == nil {
		s.fromIMS.Add(1)
	}
}

func (s *MediaSession) recordMixedFrame(direction mixDirection, payload []byte) {
	s.mu.RLock()
	recorder := s.recorder
	s.mu.RUnlock()
	if recorder != nil {
		recorder.Add(direction, payload)
	}
}

func (s *MediaSession) readIMSRTP(destination chan<- *rtp.Packet) {
	buffer := make([]byte, 2048)
	for {
		read, _, err := s.rtpConn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		packet := &rtp.Packet{}
		raw := append([]byte(nil), buffer[:read]...)
		if packet.Unmarshal(raw) != nil {
			continue
		}
		select {
		case destination <- packet:
		default:
			s.lost.Add(1)
		}
	}
}

func (s *MediaSession) endpoint() (rtpEndpoint, RealtimeCodec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remote, s.realtimeCodec, s.attached
}

func (s *MediaSession) primeRelay() error {
	endpoint, _, ok := s.endpoint()
	if !ok {
		return errors.New("phone: media is not attached")
	}
	return s.writeSilentRTP(1, uint32(endpoint.ClockRate/audioFramesPerSecond))
}

func (s *MediaSession) forwardSilentRTP() {
	ticker := time.NewTicker(jitterTick)
	defer ticker.Stop()
	sequence := uint16(1)
	endpoint, _, ok := s.endpoint()
	if !ok {
		return
	}
	timestamp := uint32(endpoint.ClockRate / audioFramesPerSecond)
	for {
		select {
		case <-ticker.C:
			endpoint, _, ok = s.endpoint()
			if !ok {
				continue
			}
			sequence++
			timestamp += uint32(endpoint.ClockRate / audioFramesPerSecond)
			if err := s.writeSilentRTP(sequence, timestamp); err != nil {
				s.lost.Add(1)
				s.reportSilentRTPFailure(err)
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *MediaSession) reportSilentRTPFailure(err error) {
	logger.Error("仅听静音 RTP 发送失败", "media_id", s.ID, "err", err)
	if s.onState != nil {
		s.onState(s.ID, webrtc.PeerConnectionStateFailed)
	}
}

func (s *MediaSession) writeSilentRTP(sequence uint16, timestamp uint32) error {
	endpoint, codec, ok := s.endpoint()
	if !ok {
		return errors.New("phone: media is not attached")
	}
	payload := make([]byte, browserSamplesPerFrame)
	for index := range payload {
		payload[index] = 0xff
	}
	s.recordMixedFrame(mixToIMS, payload)
	payload, err := browserPayloadForIMS(payload, endpoint, codec)
	if err != nil {
		return err
	}
	packet := &rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: endpoint.PayloadType, SequenceNumber: sequence,
		Timestamp: timestamp, SSRC: 0x564f4849,
	}, Payload: payload}
	raw, err := packet.Marshal()
	if err != nil {
		return err
	}
	_, err = s.rtpConn.WriteToUDP(raw, endpoint.Address)
	if err == nil {
		s.toIMS.Add(1)
	}
	return err
}

func browserPayloadForIMS(payload []byte, endpoint rtpEndpoint, codec RealtimeCodec) ([]byte, error) {
	if len(payload) != browserSamplesPerFrame {
		return nil, errors.New("phone: browser PCMU packet must contain one 20ms frame")
	}
	if endpoint.Codec == "PCMU" {
		return payload, nil
	}
	if endpoint.Codec == "PCMA" {
		transcodeG711(payload, "PCMU", "PCMA")
		return payload, nil
	}
	if codec == nil {
		return nil, errors.New("phone: realtime encoder is unavailable")
	}
	pcm, err := resamplePCM(decodePCMU(payload), browserClockRate, codec.SampleRate())
	if err != nil {
		return nil, err
	}
	return codec.Encode(pcm)
}

func imsPayloadForBrowser(payload []byte, endpoint rtpEndpoint, codec RealtimeCodec) ([]byte, error) {
	if endpoint.Codec == "PCMU" {
		if len(payload) != browserSamplesPerFrame {
			return nil, errors.New("phone: IMS PCMU packet must contain one 20ms frame")
		}
		return payload, nil
	}
	if endpoint.Codec == "PCMA" {
		if len(payload) != browserSamplesPerFrame {
			return nil, errors.New("phone: IMS PCMA packet must contain one 20ms frame")
		}
		transcodeG711(payload, "PCMA", "PCMU")
		return payload, nil
	}
	if codec == nil {
		return nil, errors.New("phone: realtime decoder is unavailable")
	}
	pcm, err := codec.Decode(payload)
	if err != nil {
		return nil, err
	}
	pcm, err = resamplePCM(pcm, codec.SampleRate(), browserClockRate)
	if err != nil {
		return nil, err
	}
	return encodePCMU(pcm), nil
}

func scaleTimestamp(timestamp uint32, fromRate, toRate int) uint32 {
	if fromRate <= 0 || toRate <= 0 || fromRate == toRate {
		return timestamp
	}
	return uint32(uint64(timestamp) * uint64(toRate) / uint64(fromRate))
}

package volte

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

func pcmJitterPacket(ssrc uint32, sequence uint16, timestamp uint32) *rtp.Packet {
	return &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: pcmuPayloadType, SSRC: ssrc, SequenceNumber: sequence, Timestamp: timestamp},
		Payload: make([]byte, pcmuFrameSamples),
	}
}

func TestAdaptivePCMBufferReordersAndHandlesSequenceWrap(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	buffer.push(pcmJitterPacket(1, 65535, 0), start)
	buffer.push(pcmJitterPacket(1, 1, 320), start.Add(20*time.Millisecond))
	buffer.push(pcmJitterPacket(1, 0, 160), start.Add(40*time.Millisecond))

	for _, sequence := range []uint16{65535, 0, 1} {
		packet, kind := buffer.pop()
		if kind != pcmPlayoutAudio || packet.SequenceNumber != sequence {
			t.Fatalf("pop = (%v, %v), want audio sequence %d", packet, kind, sequence)
		}
	}
}

func TestAdaptivePCMBufferConcealsGapAndDropsLatePacket(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	buffer.push(pcmJitterPacket(1, 100, 0), start)
	buffer.push(pcmJitterPacket(1, 102, 320), start.Add(40*time.Millisecond))
	buffer.push(pcmJitterPacket(1, 103, 480), start.Add(60*time.Millisecond))

	if packet, kind := buffer.pop(); kind != pcmPlayoutAudio || packet.SequenceNumber != 100 {
		t.Fatalf("first pop = (%v, %v)", packet, kind)
	}
	if packet, kind := buffer.pop(); packet != nil || kind != pcmPlayoutGap {
		t.Fatalf("gap pop = (%v, %v)", packet, kind)
	}
	buffer.push(pcmJitterPacket(1, 101, 160), start.Add(80*time.Millisecond))
	if packet, kind := buffer.pop(); kind != pcmPlayoutAudio || packet.SequenceNumber != 102 {
		t.Fatalf("post-gap pop = (%v, %v)", packet, kind)
	}
	stats := buffer.stats()
	if stats.Concealed != 1 || stats.Late != 1 {
		t.Fatalf("stats = %+v, want one concealed and one late packet", stats)
	}
}

func TestAdaptivePCMBufferRaisesAndSlowlyLowersTarget(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	first := pcmJitterPacket(1, 10, 0)
	buffer.resetStream(first, start, false)
	buffer.updateTarget(first, start)

	// The second packet is 100ms later than its RTP clock predicts. RFC 3550's
	// first EWMA step is 50 ticks (6.25ms), which raises the target to four frames.
	buffer.emitting = true
	buffer.updateTarget(pcmJitterPacket(1, 11, 160), start.Add(120*time.Millisecond))
	if buffer.targetFrames != 4 || buffer.holdFrames != 1 {
		t.Fatalf("raised target=%d hold=%d, want 4/1", buffer.targetFrames, buffer.holdFrames)
	}
	if got := buffer.stats().JitterMicros; got != 6250 {
		t.Fatalf("jitter=%dus, want 6250us", got)
	}

	buffer.adjustTarget(pcmJitterMinFrames, start.Add(time.Second))
	buffer.adjustTarget(pcmJitterMinFrames, start.Add(time.Second+pcmJitterStablePeriod))
	if buffer.targetFrames != 3 || buffer.trimFrames != 1 {
		t.Fatalf("first decrease target=%d trim=%d, want 3/1", buffer.targetFrames, buffer.trimFrames)
	}
	buffer.adjustTarget(pcmJitterMinFrames, start.Add(time.Second+2*pcmJitterStablePeriod))
	if buffer.targetFrames != 2 || buffer.trimFrames != 2 {
		t.Fatalf("second decrease target=%d trim=%d, want 2/2", buffer.targetFrames, buffer.trimFrames)
	}
}

func TestAdaptivePCMBufferUsesMissingSlotToReduceDelay(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	buffer.push(pcmJitterPacket(1, 100, 0), start)
	buffer.push(pcmJitterPacket(1, 102, 320), start.Add(20*time.Millisecond))
	buffer.push(pcmJitterPacket(1, 103, 480), start.Add(40*time.Millisecond))
	if packet, kind := buffer.pop(); kind != pcmPlayoutAudio || packet.SequenceNumber != 100 {
		t.Fatalf("first pop = (%v, %v)", packet, kind)
	}

	buffer.trimFrames = 1
	packet, kind := buffer.pop()
	if kind != pcmPlayoutAudio || packet.SequenceNumber != 102 {
		t.Fatalf("trimmed gap pop = (%v, %v), want sequence 102", packet, kind)
	}
	if stats := buffer.stats(); stats.Concealed != 0 || stats.Trimmed != 1 {
		t.Fatalf("stats = %+v, want one trim without concealment", stats)
	}
}

func TestAdaptivePCMBufferBoundsBurstAndRestartsOnNewStream(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	for index := 0; index < pcmJitterMaxFrames+3; index++ {
		buffer.push(pcmJitterPacket(1, uint16(100+index), uint32(index*pcmuFrameSamples)), start.Add(time.Duration(index)*time.Millisecond))
	}
	if len(buffer.packets) != pcmJitterMaxFrames {
		t.Fatalf("buffer depth=%d, want %d", len(buffer.packets), pcmJitterMaxFrames)
	}
	if buffer.stats().Trimmed != 3 {
		t.Fatalf("trimmed=%d, want 3", buffer.stats().Trimmed)
	}

	buffer.push(pcmJitterPacket(2, 7, 0), start.Add(time.Second))
	if buffer.ssrc != 2 || buffer.expected != 7 || buffer.emitting {
		t.Fatalf("stream was not reset: ssrc=%d expected=%d emitting=%v", buffer.ssrc, buffer.expected, buffer.emitting)
	}
	if buffer.stats().Restarts != 1 {
		t.Fatalf("restarts=%d, want 1", buffer.stats().Restarts)
	}
}

func TestAdaptivePCMBufferDoesNotRestartForVeryLatePacket(t *testing.T) {
	buffer := newAdaptivePCMBuffer()
	start := time.Unix(0, 0)
	for index := 0; index < pcmJitterInitialFrames; index++ {
		buffer.push(pcmJitterPacket(1, uint16(100+index), uint32(index*pcmuFrameSamples)),
			start.Add(time.Duration(index)*pcmuFrameDuration))
	}
	for range pcmJitterInitialFrames {
		buffer.pop()
	}

	buffer.push(pcmJitterPacket(1, 30, 0), start.Add(time.Second))
	stats := buffer.stats()
	if stats.Late != 1 || stats.Restarts != 0 || buffer.expected != 103 {
		t.Fatalf("late packet changed stream: expected=%d stats=%+v", buffer.expected, stats)
	}
}

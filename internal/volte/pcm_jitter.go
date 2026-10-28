package volte

import (
	"math"
	"time"

	"github.com/pion/rtp"
)

const (
	pcmJitterInitialFrames = 3
	pcmJitterMinFrames     = 2
	pcmJitterMaxFrames     = 6
	pcmJitterSequenceLimit = 64
	pcmJitterStablePeriod  = 5 * time.Second
)

type pcmPlayoutKind uint8

const (
	pcmPlayoutWaiting pcmPlayoutKind = iota
	pcmPlayoutAudio
	pcmPlayoutGap
	pcmPlayoutIdle
	pcmPlayoutRebuffer
)

type pcmJitterDiagnostics struct {
	TargetFrames uint64
	MaxDepth     uint64
	JitterMicros uint64
	Received     uint64
	Late         uint64
	Duplicate    uint64
	Concealed    uint64
	Trimmed      uint64
	Rebuffers    uint64
	Restarts     uint64
}

type adaptivePCMBuffer struct {
	packets  map[uint16]*rtp.Packet
	ssrc     uint32
	expected uint16
	started  bool
	emitting bool

	targetFrames int
	holdFrames   int
	trimFrames   int
	lowerSince   time.Time

	firstArrival    time.Time
	firstTimestamp  uint32
	previousTransit float64
	jitterTicks     float64
	estimatorReady  bool

	diagnostics pcmJitterDiagnostics
}

func newAdaptivePCMBuffer() *adaptivePCMBuffer {
	return &adaptivePCMBuffer{
		packets:      make(map[uint16]*rtp.Packet, pcmJitterMaxFrames),
		targetFrames: pcmJitterInitialFrames,
	}
}

func (b *adaptivePCMBuffer) push(packet *rtp.Packet, arrival time.Time) {
	if packet == nil {
		return
	}
	b.diagnostics.Received++
	if !b.started {
		b.resetStream(packet, arrival, false)
	}
	offset := int(int16(packet.SequenceNumber - b.expected))
	if packet.SSRC != b.ssrc || offset >= pcmJitterSequenceLimit {
		b.resetStream(packet, arrival, true)
		offset = 0
	}
	if offset < 0 {
		b.diagnostics.Late++
		return
	}
	if _, exists := b.packets[packet.SequenceNumber]; exists {
		b.diagnostics.Duplicate++
		return
	}
	b.updateTarget(packet, arrival)
	b.packets[packet.SequenceNumber] = packet
	b.trimOverflow()
	if !b.emitting && len(b.packets) >= b.targetFrames {
		b.emitting = true
	}
	if depth := uint64(len(b.packets)); depth > b.diagnostics.MaxDepth {
		b.diagnostics.MaxDepth = depth
	}
}

func (b *adaptivePCMBuffer) pop() (*rtp.Packet, pcmPlayoutKind) {
	if !b.started || !b.emitting {
		return nil, pcmPlayoutWaiting
	}
	if b.holdFrames > 0 {
		b.holdFrames--
		b.diagnostics.Rebuffers++
		return nil, pcmPlayoutRebuffer
	}
	if b.trimFrames > 0 && len(b.packets) > 0 {
		delete(b.packets, b.expected)
		b.expected++
		b.trimFrames--
		b.diagnostics.Trimmed++
	}
	packet := b.packets[b.expected]
	if packet != nil {
		delete(b.packets, b.expected)
		b.expected++
		return packet, pcmPlayoutAudio
	}
	if len(b.packets) == 0 {
		return nil, pcmPlayoutIdle
	}
	b.expected++
	b.diagnostics.Concealed++
	return nil, pcmPlayoutGap
}

func (b *adaptivePCMBuffer) resetStream(packet *rtp.Packet, arrival time.Time, restart bool) {
	if restart {
		b.diagnostics.Restarts++
	}
	clear(b.packets)
	b.ssrc, b.expected, b.started = packet.SSRC, packet.SequenceNumber, true
	b.emitting = false
	b.targetFrames = pcmJitterInitialFrames
	b.holdFrames, b.trimFrames = 0, 0
	b.lowerSince = time.Time{}
	b.firstArrival, b.firstTimestamp = arrival, packet.Timestamp
	b.previousTransit, b.jitterTicks, b.estimatorReady = 0, 0, false
}

func (b *adaptivePCMBuffer) updateTarget(packet *rtp.Packet, arrival time.Time) {
	arrivalTicks := float64(arrival.Sub(b.firstArrival)) * float64(pcmuClockRate) / float64(time.Second)
	rtpTicks := float64(int32(packet.Timestamp - b.firstTimestamp))
	transit := arrivalTicks - rtpTicks
	if b.estimatorReady {
		delta := math.Abs(transit - b.previousTransit)
		b.jitterTicks += (delta - b.jitterTicks) / 16
	}
	b.previousTransit, b.estimatorReady = transit, true
	b.adjustTarget(b.desiredFrames(), arrival)
}

func (b *adaptivePCMBuffer) desiredFrames() int {
	frameTicks := float64(pcmuFrameSamples)
	desired := int(math.Ceil((float64(pcmJitterMinFrames)*frameTicks + 4*b.jitterTicks) / frameTicks))
	return min(max(desired, pcmJitterMinFrames), pcmJitterMaxFrames)
}

func (b *adaptivePCMBuffer) adjustTarget(desired int, now time.Time) {
	if desired > b.targetFrames {
		increase := desired - b.targetFrames
		b.targetFrames = desired
		if b.emitting {
			b.holdFrames += increase
		}
		b.lowerSince = time.Time{}
		return
	}
	if desired == b.targetFrames {
		b.lowerSince = time.Time{}
		return
	}
	if b.lowerSince.IsZero() {
		b.lowerSince = now
		return
	}
	if now.Sub(b.lowerSince) < pcmJitterStablePeriod {
		return
	}
	b.targetFrames--
	if b.emitting {
		b.trimFrames++
	}
	b.lowerSince = now
}

func (b *adaptivePCMBuffer) trimOverflow() {
	for len(b.packets) > pcmJitterMaxFrames {
		if _, exists := b.packets[b.expected]; exists {
			delete(b.packets, b.expected)
			b.diagnostics.Trimmed++
		}
		b.expected++
	}
}

func (b *adaptivePCMBuffer) stats() pcmJitterDiagnostics {
	result := b.diagnostics
	result.TargetFrames = uint64(b.targetFrames)
	result.JitterMicros = uint64(b.jitterTicks * float64(time.Second/time.Microsecond) / pcmuClockRate)
	return result
}

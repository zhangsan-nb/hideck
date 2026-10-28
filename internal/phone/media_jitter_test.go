package phone

import (
	"testing"

	"github.com/pion/rtp"
)

func jitterPacket(ssrc uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{SSRC: ssrc, SequenceNumber: seq}}
}

// Field capture (Lebara UK, call to a mobile): early media started on one SSRC,
// a second SSRC sent seq 0 and 1, then the first SSRC went on with jittered
// arrivals. The two stray packets stayed buffered forever and every later
// packet was skipped or dropped, so the browser got no audio at all.
func TestIMSJitterBufferPlaysThroughAStraySSRC(t *testing.T) {
	const main, stray = 0x74a7ad0e, 0xe19a7b94
	buffer := newIMSJitterBuffer()
	played := 0
	tick := func(arrivals ...*rtp.Packet) {
		for _, packet := range arrivals {
			buffer.push(packet)
		}
		if packet, _ := buffer.pop(); packet != nil && packet.SSRC == main {
			played++
		}
	}

	tick(jitterPacket(main, 34338))
	tick(jitterPacket(stray, 0))
	tick(jitterPacket(stray, 1))
	// 34339 never arrives; afterwards two packets land on every other tick.
	seq, sent := uint16(34340), 0
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			tick()
			continue
		}
		tick(jitterPacket(main, seq), jitterPacket(main, seq+1))
		seq, sent = seq+2, sent+2
	}
	for len(buffer.packets) > 0 {
		tick()
	}

	if played != 1+sent {
		t.Fatalf("played %d main packets, want %d", played, 1+sent)
	}
}

// The browser track keeps one SSRC, so switching IMS streams must not send its
// sequence numbers backwards; SRTP would drop the main stream as replayed.
func TestBrowserRTPClockKeepsSequenceMovingForwardAcrossStreams(t *testing.T) {
	var clock browserRTPClock
	type stamped struct {
		sequence  uint16
		timestamp uint32
	}
	var got []stamped
	for _, step := range []any{
		jitterPacket(1, 34338), jitterPacket(2, 0), jitterPacket(2, 1), "lost", "idle", jitterPacket(1, 34340),
	} {
		switch step {
		case "lost":
			clock.skip()
		case "idle":
			clock.idle()
		default:
			packet := step.(*rtp.Packet)
			packet.Timestamp = 5000
			clock.stamp(packet, browserSamplesPerFrame)
			got = append(got, stamped{packet.SequenceNumber, packet.Timestamp})
		}
	}
	// The idle tick (DTX silence) moves the timestamp but not the sequence.
	want := []stamped{{34338, 5000}, {34339, 5160}, {34340, 5320}, {34342, 5800}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("browser packets=%v, want %v", got, want)
		}
	}
}

func TestIMSJitterBufferDropsLatePacketsAndFollowsJumps(t *testing.T) {
	buffer := newIMSJitterBuffer()
	if got := buffer.push(jitterPacket(1, 100)); got != imsPacketQueued {
		t.Fatalf("first push=%v", got)
	}
	if packet, gap := buffer.pop(); packet == nil || gap {
		t.Fatalf("pop=%v gap=%v", packet, gap)
	}
	if got := buffer.push(jitterPacket(1, 99)); got != imsPacketLate {
		t.Fatalf("late push=%v, want late", got)
	}
	if len(buffer.packets) != 0 {
		t.Fatalf("late packet buffered: %d", len(buffer.packets))
	}
	if got := buffer.push(jitterPacket(1, 5000)); got != imsStreamRestarted {
		t.Fatalf("jump push=%v, want restart", got)
	}
	if packet, _ := buffer.pop(); packet == nil || packet.SequenceNumber != 5000 {
		t.Fatalf("after jump pop=%v", packet)
	}
	// Same SSRC restarting from a lower sequence must not be dropped as late forever.
	if got := buffer.push(jitterPacket(1, 10)); got != imsStreamRestarted {
		t.Fatalf("backward jump push=%v, want restart", got)
	}
	if packet, _ := buffer.pop(); packet == nil || packet.SequenceNumber != 10 {
		t.Fatalf("after backward jump pop=%v", packet)
	}
	if got := buffer.push(jitterPacket(2, 13)); got != imsStreamRestarted {
		t.Fatalf("new SSRC push=%v, want restart", got)
	}
	if packet, gap := buffer.pop(); packet == nil || packet.SSRC != 2 || gap {
		t.Fatalf("after SSRC change pop=%v gap=%v", packet, gap)
	}
}

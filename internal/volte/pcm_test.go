package volte

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memPCM struct {
	mu      sync.Mutex
	written [][]int16
	toRead  [][]int16
	closed  bool
}

func (m *memPCM) ReadFrame() ([]int16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.toRead) == 0 {
		return make([]int16, pcmuFrameSamples), nil
	}
	frame := m.toRead[0]
	m.toRead = m.toRead[1:]
	return frame, nil
}
func (m *memPCM) WriteFrame(frame []int16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]int16(nil), frame...)
	m.written = append(m.written, cp)
	if len(m.written) > pcmJitterMaxFrames {
		return errorsNew("overflow")
	}
	return nil
}
func (m *memPCM) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *memPCM) writtenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.written)
}

func (m *memPCM) lastWritten() []int16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.written) == 0 {
		return nil
	}
	return append([]int16(nil), m.written[len(m.written)-1]...)
}

func (m *memPCM) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func errorsNew(s string) error { return errStr(s) }

type errStr string

func (e errStr) Error() string { return string(e) }

func TestPCMBridgeBidirectionalPCMU(t *testing.T) {
	a, b := net.Pipe()
	left, right := newPacketPipe(a), newPacketPipe(b)
	pcmDown := &memPCM{}
	tone := make([]int16, pcmuFrameSamples)
	for i := range tone {
		tone[i] = 1234
	}
	pcmUp := &memPCM{toRead: [][]int16{tone, tone, tone}}
	down := NewPCMBridge(left, addr{"peer"}, pcmDown, false)
	up := NewPCMBridge(right, addr{"peer"}, pcmUp, false)
	defer down.Close()
	defer up.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		toPCM, _, _, _, _ := down.Stats()
		_, upFrom, _, _, _ := up.Stats()
		if toPCM > 0 && upFrom > 0 && pcmDown.writtenCount() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("down stats %+v written=%d", mustStats(down), pcmDown.writtenCount())
}

func TestPCMBridgeListenOnlyWritesSilence(t *testing.T) {
	a, b := net.Pipe()
	left, right := newPacketPipe(a), newPacketPipe(b)
	pcm := &memPCM{toRead: [][]int16{nonzeroFrame()}}
	bridge := NewPCMBridge(left, addr{"peer"}, pcm, true)
	peer := NewPCMBridge(right, addr{"peer"}, &memPCM{}, false)
	defer bridge.Close()
	defer peer.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, _, silent, _, _ := bridge.Stats()
		if silent > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("listen-only did not write silent uplink")
}

func TestPCMBridgeCloseReleases(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	pcm := &memPCM{}
	bridge := NewPCMBridge(newPacketPipe(a), addr{"peer"}, pcm, false)
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	if !pcm.isClosed() {
		t.Fatal("pcm not closed")
	}
}

func TestPCMBridgeCloseWaitsForDownlinkReader(t *testing.T) {
	conn := newDelayedClosePacketConn()
	bridge := NewPCMBridge(conn, addr{"peer"}, &memPCM{}, false)
	select {
	case <-conn.readStarted:
	case <-time.After(time.Second):
		t.Fatal("downlink reader did not start")
	}

	closed := make(chan struct{})
	go func() {
		_ = bridge.Close()
		close(closed)
	}()
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		close(conn.releaseRead)
		t.Fatal("bridge did not close the RTP connection")
	}
	select {
	case <-closed:
		close(conn.releaseRead)
		t.Fatal("bridge closed before downlink reader exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(conn.releaseRead)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("bridge did not close after downlink reader exited")
	}
}

func TestPCMBridgeConcealsMissingDownlinkWithSilence(t *testing.T) {
	pcm := &memPCM{}
	bridge := &PCMBridge{pcm: pcm}
	bridge.playDownlink(nil, pcmPlayoutGap, &pcmWriteLogState{})

	frame := pcm.lastWritten()
	if len(frame) != pcmuFrameSamples {
		t.Fatalf("concealment frame length=%d, want %d", len(frame), pcmuFrameSamples)
	}
	for index, sample := range frame {
		if sample != 0 {
			t.Fatalf("concealment sample %d=%d, want silence", index, sample)
		}
	}
	if bridge.lost.Load() != 1 || bridge.toPCM.Load() != 0 {
		t.Fatalf("lost=%d to_pcm=%d, want 1/0", bridge.lost.Load(), bridge.toPCM.Load())
	}
}

func nonzeroFrame() []int16 {
	out := make([]int16, pcmuFrameSamples)
	for i := range out {
		out[i] = 9
	}
	return out
}

func mustStats(b *PCMBridge) [5]uint64 {
	a, c, d, e, f := b.Stats()
	return [5]uint64{a, c, d, e, f}
}

type addr struct{ s string }

func (a addr) Network() string { return "pipe" }
func (a addr) String() string  { return a.s }

type packetPipe struct{ net.Conn }

func newPacketPipe(c net.Conn) *packetPipe { return &packetPipe{Conn: c} }

func (p *packetPipe) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.Conn.Read(b)
	return n, addr{"peer"}, err
}
func (p *packetPipe) WriteTo(b []byte, _ net.Addr) (int, error) {
	return p.Conn.Write(b)
}

type delayedClosePacketConn struct {
	readStarted chan struct{}
	closed      chan struct{}
	releaseRead chan struct{}
	started     atomic.Bool
}

func newDelayedClosePacketConn() *delayedClosePacketConn {
	return &delayedClosePacketConn{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
}

func (c *delayedClosePacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	if c.started.CompareAndSwap(false, true) {
		close(c.readStarted)
	}
	<-c.closed
	<-c.releaseRead
	return 0, nil, net.ErrClosed
}

func (c *delayedClosePacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(payload), nil
	}
}

func (c *delayedClosePacketConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *delayedClosePacketConn) LocalAddr() net.Addr              { return addr{"local"} }
func (c *delayedClosePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *delayedClosePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *delayedClosePacketConn) SetWriteDeadline(time.Time) error { return nil }

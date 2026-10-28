package qmi

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func serialTestClient(t *testing.T, path string) (*Client, net.Conn) {
	t.Helper()
	clientConn, modem := net.Pipe()
	opts := DefaultClientOptions()
	opts.ReadDeadline = 5 * time.Millisecond
	opts.DefaultRequestTimeout = 2 * time.Second
	c := newClientWithTransport(path, opts, clientConn)
	t.Cleanup(func() { _ = c.Close(); _ = modem.Close() })
	return c, modem
}

func serialTestRequest(c *Client, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := c.SendRequest(ctx, ServiceControl, 0, CTLSync, nil)
		done <- err
	}()
	return done
}

func serialReadRequest(t *testing.T, modem net.Conn) *Packet {
	t.Helper()
	_ = modem.SetReadDeadline(time.Now().Add(time.Second))
	p, err := readQMIFrameFromConn(modem)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	return p
}

func serialAssertNoRequest(t *testing.T, modem net.Conn) {
	t.Helper()
	_ = modem.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
	var probe [1]byte
	_, err := modem.Read(probe[:])
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("request reached modem while another request is outstanding: %v", err)
	}
}

func serialReply(t *testing.T, modem net.Conn, p *Packet, done <-chan error) {
	t.Helper()
	_ = modem.SetWriteDeadline(time.Now().Add(time.Second))
	if err := writeCTLSuccess(modem, p); err != nil {
		t.Fatalf("reply: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete after reply")
	}
}

func TestSerialRequestsSharedAcrossClientsUntilReply(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-serial")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-serial")
	done1 := serialTestRequest(first, context.Background())
	p1 := serialReadRequest(t, modem1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	serialReply(t, modem1, p1, done1)
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsCanceledWaitDoesNotWriteOrLeak(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-cancel")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-cancel")
	done1 := serialTestRequest(first, context.Background())
	p1 := serialReadRequest(t, modem1)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := <-serialTestRequest(second, ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request error = %v, want deadline exceeded", err)
	}
	if got := atomic.LoadUint32(&second.ctlTxID); got != 0 {
		t.Fatalf("waiting request allocated transaction ID %d", got)
	}
	serialAssertNoRequest(t, modem2)
	serialReply(t, modem1, p1, done1)
	done2 := serialTestRequest(second, context.Background())
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsDifferentPathsAreIndependent(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-first")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-second")
	done1 := serialTestRequest(first, context.Background())
	p1 := serialReadRequest(t, modem1)
	done2 := serialTestRequest(second, context.Background())
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
	serialReply(t, modem1, p1, done1)
}

func TestSerialRequestsDisabledByDefaultPreservesConcurrency(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "")
	c, modem := serialTestClient(t, "/dev/cdc-wdm-concurrent")
	done1 := serialTestRequest(c, context.Background())
	p1 := serialReadRequest(t, modem)
	done2 := serialTestRequest(c, context.Background())
	p2 := serialReadRequest(t, modem)
	serialReply(t, modem, p2, done2)
	serialReply(t, modem, p1, done1)
}

func TestSerialRequestsCloseWakesWaiter(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-close")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-close")
	done1 := serialTestRequest(first, context.Background())
	p1 := serialReadRequest(t, modem1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	if err := second.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-done2:
		if err == nil {
			t.Fatal("closed waiting request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake waiting request")
	}
	serialReply(t, modem1, p1, done1)
	third, modem3 := serialTestClient(t, "/dev/cdc-wdm-close")
	done3 := serialTestRequest(third, context.Background())
	p3 := serialReadRequest(t, modem3)
	serialReply(t, modem3, p3, done3)
}

func TestSerialRequestsDefaultTimeoutIncludesGateWait(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-default-timeout")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-default-timeout")
	second.opts.DefaultRequestTimeout = 40 * time.Millisecond
	done1 := serialTestRequest(first, context.Background())
	p1 := serialReadRequest(t, modem1)
	select {
	case err := <-serialTestRequest(second, context.Background()):
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiting request error = %v, want default deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gate wait ignored default request timeout")
	}
	if got := atomic.LoadUint32(&second.ctlTxID); got != 0 {
		t.Fatalf("waiting request allocated transaction ID %d", got)
	}
	serialAssertNoRequest(t, modem2)
	serialReply(t, modem1, p1, done1)
}

func TestSerialRequestsOwnerCloseReleasesGate(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	const path = "/dev/cdc-wdm-owner-close"
	first, modem1 := serialTestClient(t, path)
	second, modem2 := serialTestClient(t, path)
	done1 := serialTestRequest(first, context.Background())
	_ = serialReadRequest(t, modem1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	if err := first.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}
	select {
	case err := <-done1:
		if err == nil {
			t.Fatal("closed owner request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("owner Close did not complete outstanding request")
	}
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
	compatRequestGates.Lock()
	_, retained := compatRequestGates.byPath[path]
	compatRequestGates.Unlock()
	if retained {
		t.Fatal("completed requests retained device gate")
	}
}

func serialCanceledCaller(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller error = %v, want cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("caller cancellation waited for physical response")
	}
}

func TestSerialRequestsCanceledInFlightRetainsGateUntilLateReply(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-late-reply")
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-late-reply")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	p1 := serialReadRequest(t, modem1)
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	first.mu.Lock()
	pending := len(first.transactions)
	first.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending transactions = %d, want late-response owner retained", pending)
	}
	if err := writeCTLSuccess(modem1, p1); err != nil {
		t.Fatalf("late reply: %v", err)
	}
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsCanceledBlockedWriteIsDrainedBeforeNextRequest(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	const path = "/dev/cdc-wdm-blocked-write"
	cc, modem1 := net.Pipe()
	transport := &serialObservedWriteTransport{
		Conn: cc, writeStarted: make(chan struct{}),
		writeFinished: make(chan struct{}), closeFinished: make(chan struct{}),
	}
	opts := DefaultClientOptions()
	opts.DefaultRequestTimeout = 2 * time.Second
	first := newClientWithTransport(path, opts, transport)
	t.Cleanup(func() { _ = first.Close(); _ = modem1.Close() })
	second, modem2 := serialTestClient(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	// Wait for the actual write, not the transaction ID allocated before queueing.
	// The pipe writer cannot finish until the modem reads the request.
	select {
	case <-transport.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach write stage")
	}
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	p1 := serialReadRequest(t, modem1)
	serialAssertNoRequest(t, modem2)
	if err := writeCTLSuccess(modem1, p1); err != nil {
		t.Fatalf("late reply: %v", err)
	}
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsDrainTimeoutClosesOwnerBeforeReleasingGate(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	first, modem1 := serialTestClient(t, "/dev/cdc-wdm-drain-timeout")
	first.opts.DefaultRequestTimeout = 120 * time.Millisecond
	second, modem2 := serialTestClient(t, "/dev/cdc-wdm-drain-timeout")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	_ = serialReadRequest(t, modem1)
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	p2 := serialReadRequest(t, modem2)
	select {
	case <-first.closeCh:
	default:
		t.Fatal("drain timeout released gate without closing affected client")
	}
	first.mu.Lock()
	pending := len(first.transactions)
	first.mu.Unlock()
	if pending != 0 {
		t.Fatalf("drain timeout left %d transactions", pending)
	}
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsCanceledQueuedWriteRetainsGateThroughWriteAndReply(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	const path = "/dev/cdc-wdm-queued-write"
	clientConn, modem1 := net.Pipe()
	first := newUIMUnitTestClient()
	first.path, first.conn = path, clientConn
	first.opts.ReadDeadline = 5 * time.Millisecond
	first.opts.DefaultRequestTimeout = 2 * time.Second
	t.Cleanup(func() { _ = first.Close(); _ = modem1.Close() })
	second, modem2 := serialTestClient(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	// Deliberately defer starting the runtime loops so the packet is queued,
	// not yet written, when its caller cancels.
	deadline := time.Now().Add(time.Second)
	for len(first.writeCh) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	first.wg.Add(2)
	go first.readLoop()
	go first.writerLoop()
	p1 := serialReadRequest(t, modem1)
	serialAssertNoRequest(t, modem2)
	if err := writeCTLSuccess(modem1, p1); err != nil {
		t.Fatalf("late reply: %v", err)
	}
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
}

func TestSerialRequestsCloseAfterCallerCancelFinishesDrain(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	const path = "/dev/cdc-wdm-close-drain"
	first, modem1 := serialTestClient(t, path)
	second, modem2 := serialTestClient(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	_ = serialReadRequest(t, modem1)
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	closed := make(chan error, 1)
	go func() { closed <- first.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close during drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked with background drain")
	}
	p2 := serialReadRequest(t, modem2)
	serialReply(t, modem2, p2, done2)
	first.mu.Lock()
	pending := len(first.transactions)
	first.mu.Unlock()
	if pending != 0 {
		t.Fatalf("Close left %d draining transactions", pending)
	}
	compatRequestGates.Lock()
	_, retained := compatRequestGates.byPath[path]
	compatRequestGates.Unlock()
	if retained {
		t.Fatal("Close left device gate retained")
	}
}

// Keep transport.Close blocked so closeCh does not stand in for transport
// shutdown completion.
type serialSlowCloseTransport struct {
	net.Conn
	entered chan struct{}
	unblock chan struct{}
}

func (c *serialSlowCloseTransport) Close() error {
	close(c.entered)
	<-c.unblock
	return c.Conn.Close()
}
func TestSerialRequestsCloseMustFinishTransportBeforeNextClient(t *testing.T) {
	t.Setenv("HIDECK_QMI_SERIAL_REQUESTS", "1")
	const path = "/dev/cdc-wdm-review-close"
	cc, modem1 := net.Pipe()
	transport := &serialSlowCloseTransport{Conn: cc, entered: make(chan struct{}), unblock: make(chan struct{})}
	first := newClientWithTransport(path, DefaultClientOptions(), transport)
	t.Cleanup(func() { _ = first.Close(); _ = modem1.Close() })
	second, modem2 := serialTestClient(t, path)
	done1 := serialTestRequest(first, context.Background())
	_ = serialReadRequest(t, modem1)
	closeDone := make(chan struct{})
	go func() { _ = first.Close(); close(closeDone) }()
	<-transport.entered
	defer func() { close(transport.unblock); <-closeDone }()
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("caller not canceled by Close")
	}
	_ = serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
}

func serialAssertStoppedAndClean(t *testing.T, c *Client, path string) {
	t.Helper()
	stopped := make(chan struct{})
	go func() { c.wg.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("owner runtime loops did not stop")
	}
	c.mu.Lock()
	pending := len(c.transactions)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("owner retained %d transactions", pending)
	}
	compatRequestGates.Lock()
	_, retained := compatRequestGates.byPath[path]
	compatRequestGates.Unlock()
	if retained {
		t.Fatal("completed requests retained device gate")
	}
}

func serialWriteUIMSuccess(t *testing.T, modem net.Conn, req *Packet) {
	t.Helper()
	resp := *req
	resp.TLVs = []TLV{successTLV()}
	frame := resp.Marshal()
	frame[QmuxHeaderSize] = 0x02 // Service response, rather than request.
	_ = modem.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := modem.Write(frame); err != nil {
		t.Fatalf("UIM reply: %v", err)
	}
}

func TestSerialRequestsCanceledUIMReadRecordRetainsGateUntilLateReply(t *testing.T) {
	t.Setenv(serialRequestsEnv, "1")
	const path = "/dev/cdc-wdm-uim-late-reply"
	first, modem1 := serialTestClient(t, path)
	second, modem2 := serialTestClient(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := make(chan error, 1)
	go func() {
		_, err := first.SendRequest(ctx, ServiceUIM, 7, UIMReadRecord, nil)
		done1 <- err
	}()
	p1 := serialReadRequest(t, modem1)
	if p1.ServiceType != ServiceUIM || p1.ClientID != 7 || p1.MessageID != UIMReadRecord {
		t.Fatalf("unexpected first UIM request: %+v", p1)
	}
	cancel()
	serialCanceledCaller(t, done1)
	done2 := make(chan error, 1)
	go func() {
		resp, err := second.SendRequest(context.Background(), ServiceUIM, 9, UIMGetCardStatus, nil)
		if err == nil {
			err = resp.CheckResult()
		}
		done2 <- err
	}()
	serialAssertNoRequest(t, modem2)
	first.mu.Lock()
	_, retained := first.transactions[uint32(ServiceUIM)<<16|uint32(p1.TransactionID)]
	first.mu.Unlock()
	if !retained {
		t.Fatal("canceled UIM transaction was removed before its late reply")
	}
	serialWriteUIMSuccess(t, modem1, p1)
	p2 := serialReadRequest(t, modem2)
	if p2.ServiceType != ServiceUIM || p2.ClientID != 9 || p2.MessageID != UIMGetCardStatus {
		t.Fatalf("unexpected second UIM request: %+v", p2)
	}
	serialWriteUIMSuccess(t, modem2, p2)
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("Get Card Status failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Get Card Status did not complete after reply")
	}
	first.mu.Lock()
	pending := len(first.transactions)
	first.mu.Unlock()
	if pending != 0 {
		t.Fatalf("late UIM reply left %d transactions", pending)
	}
}

// Observe a real net.Pipe write that cannot finish while its peer never reads.
// Separate completion signals distinguish Close intent from completed I/O.
type serialObservedWriteTransport struct {
	net.Conn
	writeStarted  chan struct{}
	writeFinished chan struct{}
	closeFinished chan struct{}
}

func (c *serialObservedWriteTransport) Write(p []byte) (int, error) {
	close(c.writeStarted)
	n, err := c.Conn.Write(p)
	close(c.writeFinished)
	return n, err
}

func (c *serialObservedWriteTransport) Close() error {
	err := c.Conn.Close()
	close(c.closeFinished)
	return err
}

func TestSerialRequestsCanceledBlockedWriteDrainTimeoutStopsOwnerBeforeNextClient(t *testing.T) {
	t.Setenv(serialRequestsEnv, "1")
	const path = "/dev/cdc-wdm-blocked-write-timeout"
	cc, modem1 := net.Pipe()
	transport := &serialObservedWriteTransport{
		Conn: cc, writeStarted: make(chan struct{}),
		writeFinished: make(chan struct{}), closeFinished: make(chan struct{}),
	}
	opts := DefaultClientOptions()
	opts.DefaultRequestTimeout = 200 * time.Millisecond
	first := newClientWithTransport(path, opts, transport)
	t.Cleanup(func() { _ = first.Close(); _ = modem1.Close() })
	second, modem2 := serialTestClient(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1 := serialTestRequest(first, ctx)
	select {
	case <-transport.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("owner did not begin its pipe write")
	}
	cancel()
	serialCanceledCaller(t, done1)
	done2 := serialTestRequest(second, context.Background())
	serialAssertNoRequest(t, modem2)
	select {
	case <-transport.writeFinished:
		t.Fatal("owner write finished although its peer never read the request")
	default:
	}
	p2 := serialReadRequest(t, modem2)
	select {
	case <-transport.closeFinished:
	default:
		t.Fatal("next request reached modem before owner transport finished Close")
	}
	select {
	case <-transport.writeFinished:
	default:
		t.Fatal("next request reached modem while owner write was still blocked")
	}
	// Never read the first request before timeout. After owner shutdown its peer
	// must see EOF, proving no buffered request was sent after gate release.
	_ = modem1.SetReadDeadline(time.Now().Add(time.Second))
	var probe [1]byte
	if n, err := modem1.Read(probe[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("closed owner pipe read = (%d, %v), want (0, EOF)", n, err)
	}
	serialReply(t, modem2, p2, done2)
	serialAssertStoppedAndClean(t, first, path)
}

func TestSerialRequestsTransportFailureClosesOwnerAndReleasesGate(t *testing.T) {
	for _, failure := range []string{"read_eof", "partial_write_error"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv(serialRequestsEnv, "1")
			path := "/dev/cdc-wdm-transport-" + failure
			first, modem1 := serialTestClient(t, path)
			second, modem2 := serialTestClient(t, path)
			done1 := serialTestRequest(first, context.Background())
			if failure == "read_eof" {
				_ = serialReadRequest(t, modem1)
			} else {
				// Consume one actual byte, then leave net.Pipe.Write blocked on
				// the rest of the frame until closing the peer produces an error.
				_ = modem1.SetReadDeadline(time.Now().Add(time.Second))
				var prefix [1]byte
				if _, err := io.ReadFull(modem1, prefix[:]); err != nil {
					t.Fatalf("read partial request: %v", err)
				}
				if prefix[0] != 0x01 {
					t.Fatalf("first request byte = 0x%02x, want QMUX marker", prefix[0])
				}
			}
			done2 := serialTestRequest(second, context.Background())
			serialAssertNoRequest(t, modem2)
			if err := modem1.Close(); err != nil {
				t.Fatalf("close failed peer: %v", err)
			}
			select {
			case err := <-done1:
				if err == nil {
					t.Fatal("owner succeeded after transport failure")
				}
				if failure == "partial_write_error" && !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("owner write error = %v, want closed pipe", err)
				}
			case <-time.After(time.Second):
				t.Fatal("transport failure did not complete owner request")
			}
			p2 := serialReadRequest(t, modem2)
			select {
			case <-first.closeCh:
			default:
				t.Fatal("next request reached modem before owner shutdown")
			}
			serialReply(t, modem2, p2, done2)
			serialAssertStoppedAndClean(t, first, path)
		})
	}
}

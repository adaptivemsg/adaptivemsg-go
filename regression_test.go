package adaptivemsg

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var _ = MustRegister[regressionFloodRequest]()

var regressionFloodsDone atomic.Int64

// regressionFloodRequest makes the server send Count EchoReply messages on
// the stream it arrives on, or as many as it can before the stream closes.
type regressionFloodRequest struct {
	Count int `am:"count"`
}

func (*regressionFloodRequest) WireName() string {
	return "am.test.RegressionFloodRequest"
}

func (r *regressionFloodRequest) Handle(ctx *StreamContext) (Message, error) {
	defer regressionFloodsDone.Add(1)
	for range r.Count {
		if err := ctx.stream.Send(&connTestEchoReply{Text: "flood"}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// newRecoveryPipeConnection returns a v3 connection whose transport is one end
// of a net.Pipe, and the other end, through which the test acts as the peer.
// No loop runs until the test starts one.
func newRecoveryPipeConnection(t *testing.T, maxReplayBytes int64) (*Connection, net.Conn, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	c := newPendingConnection(local, nil, nil, nil).connection
	c.config = connConfig{
		version:  protocolVersionV3,
		codecID:  CodecMsgpackCompact,
		codec:    mustCodec(t, CodecMsgpackCompact),
		maxFrame: defaultMaxFrame,
	}
	c.recovery = newServerRecoveryState(ServerRecoveryOptions{
		Enable:            true,
		DetachedTTL:       time.Minute,
		MaxReplayBytes:    maxReplayBytes,
		AckEvery:          1,
		AckDelay:          time.Millisecond,
		HeartbeatInterval: time.Hour,
		HeartbeatTimeout:  2 * time.Hour,
	}, nil, recoveryToken{1}, recoveryToken{2})
	t.Cleanup(func() {
		c.markClosed()
		_ = peer.Close()
	})
	return c, local, peer
}

// expectDataSeqs reads frames from the peer end until n data frames arrived,
// failing unless their sequence numbers run 1..n in order. It returns how many
// control frames came with them.
func expectDataSeqs(t *testing.T, c *Connection, peer net.Conn, n int) int {
	t.Helper()
	wire := &Connection{config: c.config}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	controls := 0
	for want := uint64(1); want <= uint64(n); {
		streamID, seq, _, _, err := wire.readFrameFrom(peer)
		if err != nil {
			t.Fatalf("waiting for data frame %d: %v", want, err)
		}
		if streamID == controlStreamID {
			controls++
			continue
		}
		if seq != want {
			t.Fatalf("data frame %d arrived while %d was expected", seq, want)
		}
		want++
	}
	return controls
}

// A live frame must not overtake the frames waiting to be replayed. The ACK
// due at resume used to carry a live frame ahead of them, and the peer then
// closed the connection on the sequence gap.
func TestRecoveryResumeReplaysBeforeLiveFrame(t *testing.T) {
	c, local, peer := newRecoveryPipeConnection(t, 1<<20)
	for seq := uint64(1); seq <= 5; seq++ {
		mustStage(t, c.recovery.replay, 1, seq, string(rune('0'+seq)))
	}
	c.nextSendSeq.Store(5)
	c.recovery.noteReceived(c, 1)
	queued := make(chan error, 1)
	c.outbound <- outboundFrame{streamID: 1, payload: []byte{6}, queued: queued}

	c.attachTransport(local, 0)
	go c.recoveryWriterLoop()

	if controls := expectDataSeqs(t, c, peer, 6); controls != 1 {
		t.Fatalf("got %d control frames, want the one ACK", controls)
	}
	if err := <-queued; err != nil {
		t.Fatalf("live frame: %v", err)
	}
}

// The peer's last received sequence number, exchanged at attach, acknowledges
// the frames up to it. The ACK the peer sent for them may have been lost with
// the old transport, and a send waiting for room in the replay buffer then
// waited forever.
func TestRecoveryAttachAcknowledgesFramesThePeerHas(t *testing.T) {
	const payloadLen = 30
	frameSize := int64(frameHeaderLenV3 + payloadLen)
	c, local, peer := newRecoveryPipeConnection(t, 2*frameSize+10)
	for seq := uint64(1); seq <= 2; seq++ {
		mustStage(t, c.recovery.replay, 1, seq, string(make([]byte, payloadLen)))
	}
	c.nextSendSeq.Store(2)
	queued := make(chan error, 1)
	c.outbound <- outboundFrame{streamID: 1, payload: make([]byte, payloadLen), queued: queued}

	c.attachTransport(local, 2)
	go c.recoveryWriterLoop()

	wire := &Connection{config: c.config}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	streamID, seq, _, _, err := wire.readFrameFrom(peer)
	if err != nil {
		t.Fatalf("the frame waiting for room was not sent: %v", err)
	}
	if streamID == controlStreamID || seq != 3 {
		t.Fatalf("got stream %d seq %d, want data frame 3", streamID, seq)
	}
	if err := <-queued; err != nil {
		t.Fatalf("held frame: %v", err)
	}
	if got := c.recovery.replay.count(); got != 1 {
		t.Fatalf("replay buffer holds %d frames, want 1", got)
	}
}

// A transport that fails while the reconnect loop is finishing must still be
// replaced: its detach found the loop running and started none of its own.
func TestRecoveryReconnectsWhenResumedTransportFailsAtOnce(t *testing.T) {
	server := NewServer().WithRecovery(recoveryTestServerOptions())
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()
	conn, err := NewClient().
		WithConnectTimeout(500 * time.Millisecond).
		WithRecovery(recoveryTestClientOptions()).
		Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	var fired atomic.Bool
	hook := func(c *Connection) {
		if c != conn || !fired.CompareAndSwap(false, true) {
			return
		}
		if transport := c.currentTransport(); transport != nil {
			_ = transport.Close()
		}
		for deadline := time.Now().Add(2 * time.Second); c.currentTransport() != nil && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
	}
	testHookReconnected.Store(&hook)
	defer testHookReconnected.Store(nil)

	_ = conn.currentTransportForTest().Close()
	waitForCondition(t, 2*time.Second, fired.Load)

	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("SendRecv after the resumed transport failed: %v", err)
	}
	if reply.Text != "after" {
		t.Fatalf("reply got %q want %q", reply.Text, "after")
	}
}

// Frames that arrive on stream 0 after a protocol error closed it are
// dropped. They used to open a new stream 0 that nothing read; once its
// queues filled, the reader stopped and every stream stalled.
func TestDefaultStreamDropsFramesAfterProtocolError(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().WithRecvTimeout(2 * time.Second).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	floods := regressionFloodsDone.Load()
	if err := conn.Send(&regressionFloodRequest{Count: 5 * streamQueueSize}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := conn.Recv[*OkReply](); !errors.As(err, &ErrTypeMismatch{}) {
		t.Fatalf("Recv got %v, want ErrTypeMismatch", err)
	}
	// The reply below must queue behind the flood.
	waitForCondition(t, 2*time.Second, func() bool { return regressionFloodsDone.Load() > floods })

	stream := conn.NewStream()
	defer stream.Close()
	reply, err := stream.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("SendRecv on another stream: %v", err)
	}
	if reply.Text != "after" {
		t.Fatalf("reply got %q want %q", reply.Text, "after")
	}
}

// Closing a stream whose inbox is full must not stall the other streams.
func TestClosingStreamWithFullInboxDoesNotStallConnection(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().WithRecvTimeout(2 * time.Second).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	floods := regressionFloodsDone.Load()
	flooded := conn.NewStream()
	if err := flooded.Send(&regressionFloodRequest{Count: 3 * streamQueueSize}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return flooded.DebugState().InboxDepth == streamQueueSize
	})
	flooded.Close()
	// The reply below must queue behind the flood.
	waitForCondition(t, 2*time.Second, func() bool { return regressionFloodsDone.Load() > floods })

	stream := conn.NewStream()
	defer stream.Close()
	reply, err := stream.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("SendRecv on another stream: %v", err)
	}
	if reply.Text != "after" {
		t.Fatalf("reply got %q want %q", reply.Text, "after")
	}
}

type regressionNilInner struct {
	Ptr  *string  `am:"ptr"`
	List []string `am:"list"`
}

type regressionNilFields struct {
	Ptr    *string             `am:"ptr"`
	List   []string            `am:"list"`
	Map    map[string]string   `am:"map"`
	Bytes  []byte              `am:"bytes"`
	Inner  *regressionNilInner `am:"inner"`
	Nested regressionNilInner  `am:"nested"`
	Items  []*string           `am:"items"`
}

func (*regressionNilFields) WireName() string {
	return "am.test.RegressionNilFields"
}

// Nil pointers, slices and maps are encoded as msgpack nil. The compact codec
// used to fail with EOF decoding them, because a nil array element decodes
// into an empty RawMessage rather than one holding the nil byte.
func TestCodecsDecodeNilFields(t *testing.T) {
	text := "x"
	messages := []*regressionNilFields{
		{},
		{Items: []*string{nil, &text}, Nested: regressionNilInner{Ptr: &text}},
	}
	for _, id := range []CodecID{CodecMsgpackCompact, CodecMsgpackMap} {
		codec := mustCodec(t, id)
		for i, msg := range messages {
			payload, err := codec.Encode(msg)
			if err != nil {
				t.Fatalf("%s message %d: Encode: %v", id, i, err)
			}
			raw, err := newRawMessageFromPayload(codec, payload)
			if err != nil {
				t.Fatalf("%s message %d: newRawMessageFromPayload: %v", id, i, err)
			}
			decoded, err := decodeRawAs[*regressionNilFields](raw)
			if err != nil {
				t.Fatalf("%s message %d: decodeRawAs: %v", id, i, err)
			}
			if !reflect.DeepEqual(decoded, msg) {
				t.Fatalf("%s message %d: decoded %+v, want %+v", id, i, decoded, msg)
			}
		}
	}
}

// A peer that connects and sends nothing, or sends the hello but no attach,
// is closed once the handshake timeout passes.
func TestServerClosesPeerThatStallsHandshake(t *testing.T) {
	server := NewServer().
		WithRecovery(recoveryTestServerOptions()).
		WithHandshakeTimeout(100 * time.Millisecond)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	for _, sendHello := range []bool{false, true} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		if sendHello {
			if _, err := handshakeClient(conn, []CodecID{CodecMsgpackCompact}, defaultMaxFrame, protocolVersionV3); err != nil {
				t.Fatalf("handshakeClient: %v", err)
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("hello sent %v: Read got %v, want EOF from the server closing", sendHello, err)
		}
		_ = conn.Close()
	}
}

// WithHandshakeTimeout(0) removes the limit, so a silent peer stays connected.
func TestServerHandshakeTimeoutZeroDisablesLimit(t *testing.T) {
	server := NewServer().
		WithHandshakeTimeout(50 * time.Millisecond).
		WithHandshakeTimeout(0)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Read got %v, want our own read deadline to expire while the server waits", err)
	}
}

// The handshake deadline is cleared before the connection starts, so the
// connection outlives it.
func TestServerConnectionOutlivesHandshakeTimeout(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		server := NewServer().WithHandshakeTimeout(50 * time.Millisecond)
		client := NewClient().WithConnectTimeout(time.Second).WithRecvTimeout(2 * time.Second)
		if recovery {
			server = server.WithRecovery(recoveryTestServerOptions())
			client = client.WithRecovery(recoveryTestClientOptions())
		}
		addr, stop := startRecoveryTestServer(t, server)
		conn, err := client.Connect("tcp://" + addr)
		if err != nil {
			t.Fatalf("recovery %v: Connect: %v", recovery, err)
		}

		time.Sleep(200 * time.Millisecond)
		if _, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "late"}); err != nil {
			t.Fatalf("recovery %v: SendRecv after the handshake timeout: %v", recovery, err)
		}
		if detaches := conn.DebugState().Counters.TransportDetaches; detaches != 0 {
			t.Fatalf("recovery %v: transport detached %d times", recovery, detaches)
		}
		conn.Close()
		stop()
	}
}

// Shutdown closes established connections, v2 and v3, after their
// OnDisconnect, drops a connection still in the handshake, and makes Serve
// return ErrServerClosed.
func TestServerShutdownClosesConnections(t *testing.T) {
	var disconnects atomic.Int32
	server := NewServer().
		WithRecovery(recoveryTestServerOptions()).
		OnDisconnect(func(Netconn) error {
			disconnects.Add(1)
			return nil
		})
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	addr := listener.Addr().String()
	served := make(chan error, 1)
	go func() {
		served <- server.serveListener(listener, func() (net.Conn, string, error) {
			return acceptTCP(listener)
		})
	}()

	plain, err := NewClient().WithConnectTimeout(2 * time.Second).Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer plain.Close()
	recovered, err := NewClient().
		WithConnectTimeout(2 * time.Second).
		WithRecovery(recoveryTestClientOptions()).
		Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect with recovery: %v", err)
	}
	defer recovered.Close()
	stalled, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer stalled.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-served:
		if !errors.As(err, &ErrServerClosed{}) {
			t.Fatalf("serve returned %v, want ErrServerClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after Shutdown")
	}
	if n := disconnects.Load(); n != 2 {
		t.Fatalf("OnDisconnect ran %d times, want 2", n)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return plain.DebugState().Closed && recovered.DebugState().Closed
	})
	_ = stalled.SetReadDeadline(time.Now().Add(time.Second))
	_, err = stalled.Read(make([]byte, 1))
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("Read on the stalled connection got %v, want the server to have closed it", err)
	}
	if err := server.Serve("tcp://127.0.0.1:0"); !errors.As(err, &ErrServerClosed{}) {
		t.Fatalf("Serve after Shutdown returned %v, want ErrServerClosed", err)
	}
}

// A failed accept, here EMFILE, does not end Serve; it accepts again.
func TestServerKeepsAcceptingAfterAcceptError(t *testing.T) {
	server := NewServer()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	var attempts atomic.Int32
	served := make(chan error, 1)
	go func() {
		served <- server.serveListener(listener, func() (net.Conn, string, error) {
			if attempts.Add(1) <= 3 {
				return nil, "", &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
			}
			return acceptTCP(listener)
		})
	}()

	conn, err := NewClient().
		WithConnectTimeout(2 * time.Second).
		WithRecvTimeout(2 * time.Second).
		Connect("tcp://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("SendRecv: %v", err)
	}
	if reply.Text != "after" {
		t.Fatalf("reply got %q want %q", reply.Text, "after")
	}
	if n := attempts.Load(); n <= 3 {
		t.Fatalf("accept called %d times, want more than the 3 failures", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-served; !errors.As(err, &ErrServerClosed{}) {
		t.Fatalf("serve returned %v, want ErrServerClosed", err)
	}
}

// Each side's stream queue size applies to the streams it creates.
func TestStreamQueueSizeAppliesToNewStreams(t *testing.T) {
	serverCaps := make(chan int, 1)
	server := NewServer().
		WithStreamQueueSize(4).
		OnNewStream(func(ctx *StreamContext) {
			select {
			case serverCaps <- cap(ctx.stream.inbox):
			default:
			}
		})
	addr, stop := startClientTestServer(t, server)
	defer stop()

	conn, err := NewClient().WithStreamQueueSize(8).WithRecvTimeout(2 * time.Second).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	stream := conn.NewStream()
	defer stream.Close()
	if got := cap(stream.inbox); got != 8 {
		t.Fatalf("client stream queue size %d, want 8", got)
	}
	if _, err := stream.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "x"}); err != nil {
		t.Fatalf("SendRecv: %v", err)
	}
	select {
	case got := <-serverCaps:
		if got != 4 {
			t.Fatalf("server stream queue size %d, want 4", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the server opened no stream")
	}

	plain, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer plain.Close()
	if got := cap(plain.NewStream().inbox); got != streamQueueSize {
		t.Fatalf("default stream queue size %d, want %d", got, streamQueueSize)
	}
}

// Send must report ErrClosed once the connection has closed, including on a
// stream opened afterwards.
func TestSendAfterConnectionFailureReportsClosed(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	// A transport failure closes the connection without Close.
	_ = conn.currentTransportForTest().Close()
	waitForCondition(t, 2*time.Second, func() bool { return conn.DebugState().Closed })

	stream := conn.NewStream()
	for i := range 100 {
		if err := stream.Send(&connTestEchoRequest{Text: "x"}); !errors.As(err, &ErrClosed{}) {
			t.Fatalf("Send %d on a closed connection got %v, want ErrClosed", i, err)
		}
	}
}

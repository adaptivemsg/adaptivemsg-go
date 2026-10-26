package adaptivemsg

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type connTestEchoRequest struct {
	Text string `am:"text"`
}

func (*connTestEchoRequest) WireName() string {
	return "am.test.EchoRequest"
}

func (r *connTestEchoRequest) Handle(*StreamContext) (Message, error) {
	return &connTestEchoReply{Text: r.Text}, nil
}

type connTestEchoReply struct {
	Text string `am:"text"`
}

func (*connTestEchoReply) WireName() string {
	return "am.test.EchoReply"
}

type connTestErrRequest struct{}

func (*connTestErrRequest) WireName() string {
	return "am.test.ErrRequest"
}

func (*connTestErrRequest) Handle(*StreamContext) (Message, error) {
	return nil, errors.New("boom")
}

var _ = MustRegister[connTestSlowRequest]()

type connTestSlowRequest struct{}

func (*connTestSlowRequest) WireName() string {
	return "am.test.SlowRequest"
}

func (*connTestSlowRequest) Handle(*StreamContext) (Message, error) {
	time.Sleep(300 * time.Millisecond)
	return &OkReply{}, nil
}

var _ = MustRegister[connTestRecordRequest]()

var connTestRecorded atomic.Int64

type connTestRecordRequest struct{}

func (*connTestRecordRequest) WireName() string {
	return "am.test.RecordRequest"
}

func (*connTestRecordRequest) Handle(*StreamContext) (Message, error) {
	connTestRecorded.Add(1)
	return nil, nil
}

var _ = MustRegister[connTestPushThenCloseRequest]()

// connTestPushThenCloseRequest makes the server send Count replies and then
// close the connection.
type connTestPushThenCloseRequest struct {
	Count int `am:"count"`
}

func (*connTestPushThenCloseRequest) WireName() string {
	return "am.test.PushThenCloseRequest"
}

func (r *connTestPushThenCloseRequest) Handle(ctx *StreamContext) (Message, error) {
	for i := range r.Count {
		if err := ctx.stream.Send(&connTestEchoReply{Text: strconv.Itoa(i)}); err != nil {
			return nil, err
		}
	}
	ctx.stream.connection.Close()
	return nil, nil
}

func TestStreamCloseAfterSendDeliversLastMessage(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	const streams = 200
	connTestRecorded.Store(0)
	for i := 0; i < streams; i++ {
		stream := conn.NewStream()
		if err := stream.Send(&connTestRecordRequest{}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		stream.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for connTestRecorded.Load() < streams && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := connTestRecorded.Load(); got != streams {
		t.Fatalf("handled %d of %d messages sent before Close", got, streams)
	}
}

func TestConnectionCloseWhileDeliveringIsRaceFree(t *testing.T) {
	// OkReply has no handler, so the server queues it for a reader that never
	// comes; connTestSlowRequest is still being handled when the client
	// closes.
	for _, msg := range []Message{&OkReply{}, &connTestSlowRequest{}} {
		disconnected := make(chan struct{})
		server := NewServer().OnDisconnect(func(Netconn) error {
			close(disconnected)
			return nil
		})
		addr, stop := startClientTestServer(t, server)
		conn, err := NewClient().Connect(addr)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if err := conn.Send(msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		conn.Close()
		select {
		case <-disconnected:
		case <-time.After(2 * time.Second):
			t.Fatalf("server did not see the close after %T", msg)
		}
		stop()
	}
}

func TestSendRecvWhileReceivingSendsNothing(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	stream := conn.NewStream()
	defer stream.Close()
	stream.SetRecvTimeout(300 * time.Millisecond)

	recvDone := make(chan error, 1)
	go func() {
		_, err := stream.Recv[Message]()
		recvDone <- err
	}()
	for !stream.recvActive.Load() {
		time.Sleep(time.Millisecond)
	}
	if _, err := stream.SendRecv[*OkReply](&connTestRecordRequest{}); !errors.As(err, &ErrConcurrentRecv{}) {
		t.Fatalf("SendRecv got %v, want ErrConcurrentRecv", err)
	}
	// Had the request been sent, its OkReply would have reached the Recv.
	if err := <-recvDone; !errors.As(err, &ErrRecvTimeout{}) {
		t.Fatalf("Recv got %v, want ErrRecvTimeout", err)
	}
}

func TestSendRecvDiscardsLateReplyAfterTimeout(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	stream := conn.NewStream()
	defer stream.Close()

	stream.SetRecvTimeout(100 * time.Millisecond)
	if _, err := stream.SendRecv[*OkReply](&connTestSlowRequest{}); !errors.As(err, &ErrRecvTimeout{}) {
		t.Fatalf("slow SendRecv got %v, want ErrRecvTimeout", err)
	}
	stream.SetRecvTimeout(2 * time.Second)
	reply, err := stream.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "second"})
	if err != nil {
		t.Fatalf("second SendRecv: %v", err)
	}
	if reply.Text != "second" {
		t.Fatalf("second SendRecv got %q, want %q", reply.Text, "second")
	}
}

func TestConcurrentSendRecvOnOneStreamNeverMismatches(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().WithRecvTimeout(2 * time.Second).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				text := fmt.Sprintf("%d-%d", g, i)
				reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: text})
				if errors.As(err, &ErrConcurrentRecv{}) {
					continue
				}
				if err != nil {
					t.Errorf("SendRecv: %v", err)
					return
				}
				if reply.Text != text {
					t.Errorf("request %q got the reply %q", text, reply.Text)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCloseDeliversQueuedMessages(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%v", recovery), func(t *testing.T) {
			server := NewServer()
			client := NewClient()
			if recovery {
				server.WithRecovery(ServerRecoveryOptions{Enable: true})
				client.WithRecovery(ClientRecoveryOptions{Enable: true})
			}
			addr, stop := startClientTestServer(t, server)
			defer stop()

			const rounds, perRound = 20, 10
			before := connTestRecorded.Load()
			for range rounds {
				conn, err := client.Connect(addr)
				if err != nil {
					t.Fatalf("Connect: %v", err)
				}
				for range perRound {
					if err := conn.Send(&connTestRecordRequest{}); err != nil {
						t.Fatalf("Send: %v", err)
					}
				}
				conn.Close()
			}
			const want = rounds * perRound
			deadline := time.Now().Add(5 * time.Second)
			for connTestRecorded.Load()-before < want && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := connTestRecorded.Load() - before; got != want {
				t.Fatalf("server handled %d of the %d messages sent before Close", got, want)
			}
		})
	}
}

func TestSendAfterCloseFails(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	conn.Close()
	if err := conn.Send(&connTestRecordRequest{}); !errors.As(err, &ErrClosed{}) {
		t.Fatalf("Send after Close got %v, want ErrClosed", err)
	}
}

func TestMessageLargerThanReplayBufferFailsOnlyThatSend(t *testing.T) {
	server := NewServer().WithRecovery(ServerRecoveryOptions{Enable: true})
	addr, stop := startClientTestServer(t, server)
	defer stop()
	conn, err := NewClient().
		WithRecvTimeout(2 * time.Second).
		WithRecovery(ClientRecoveryOptions{Enable: true, MaxReplayBytes: 1 << 10}).
		Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	big := &connTestEchoRequest{Text: strings.Repeat("x", 2<<10)}
	if err := conn.Send(big); !errors.As(err, &ErrFrameTooLarge{}) {
		t.Fatalf("Send got %v, want ErrFrameTooLarge", err)
	}
	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "small"})
	if err != nil {
		t.Fatalf("SendRecv after the rejected Send: %v", err)
	}
	if reply.Text != "small" {
		t.Fatalf("reply got %q, want %q", reply.Text, "small")
	}
}

func TestSendWaitsWhileReplayBufferIsFull(t *testing.T) {
	server := NewServer().WithRecovery(ServerRecoveryOptions{Enable: true})
	addr, stop := startClientTestServer(t, server)
	defer stop()
	conn, err := NewClient().
		WithRecvTimeout(5 * time.Second).
		WithRecovery(ClientRecoveryOptions{Enable: true, MaxReplayBytes: 4 << 10}).
		Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	// 50 KiB through a 4 KiB buffer: each Send waits for room instead of failing.
	const count = 50
	text := strings.Repeat("x", 1<<10)
	for i := range count {
		if err := conn.Send(&connTestEchoRequest{Text: strconv.Itoa(i) + text}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	for i := range count {
		reply, err := conn.Recv[*connTestEchoReply]()
		if err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		if want := strconv.Itoa(i) + text; reply.Text != want {
			t.Fatalf("reply %d out of order", i)
		}
	}
}

func TestRecoveryCloseReleasesServerConnection(t *testing.T) {
	disconnected := make(chan struct{}, 1)
	server := NewServer().
		WithRecovery(ServerRecoveryOptions{Enable: true, DetachedTTL: 10 * time.Second}).
		OnDisconnect(func(Netconn) error {
			disconnected <- struct{}{}
			return nil
		})
	addr, stop := startClientTestServer(t, server)
	defer stop()
	conn, err := NewClient().WithRecovery(ClientRecoveryOptions{Enable: true}).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := conn.DebugState().Protocol; got != protocolVersionV3 {
		t.Fatalf("negotiated protocol %d, want %d", got, protocolVersionV3)
	}

	conn.Close()
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("server kept the recovery connection after Close")
	}
}

func TestRecvGetsMessagesSentBeforePeerClose(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery=%v", recovery), func(t *testing.T) {
			server := NewServer()
			client := NewClient().WithRecvTimeout(2 * time.Second)
			if recovery {
				server.WithRecovery(ServerRecoveryOptions{Enable: true})
				client.WithRecovery(ClientRecoveryOptions{Enable: true})
			}
			addr, stop := startClientTestServer(t, server)
			defer stop()
			conn, err := client.Connect(addr)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer conn.Close()

			const count = 100
			if err := conn.Send(&connTestPushThenCloseRequest{Count: count}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			for i := range count {
				reply, err := conn.Recv[*connTestEchoReply]()
				if err != nil {
					t.Fatalf("Recv %d: %v", i, err)
				}
				if reply.Text != strconv.Itoa(i) {
					t.Fatalf("Recv %d got %q", i, reply.Text)
				}
			}
			if _, err := conn.Recv[Message](); !errors.As(err, &ErrClosed{}) {
				t.Fatalf("Recv after the pushed messages got %v, want ErrClosed", err)
			}
		})
	}
}

func TestCloseReturnsWhenPeerStopsReading(t *testing.T) {
	// The peer completes the handshake and then never reads.
	addr := startClientTestPeer(t, func(conn net.Conn) {
		_, _ = handshakeServer(conn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, defaultMaxFrame, false)
	})
	conn, err := NewClient().Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	big := &connTestEchoRequest{Text: strings.Repeat("x", 1<<20)}
	for range 32 {
		if err := conn.Send(big); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	start := time.Now()
	conn.Close()
	if elapsed := time.Since(start); elapsed > closeFlushTimeout+time.Second {
		t.Fatalf("Close returned after %v", elapsed)
	}
}

func TestStreamCloseReleasesPeer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	serverReg := newRegistry()
	if err := registerTypes(serverReg, &connTestEchoRequest{}, &connTestEchoReply{}); err != nil {
		t.Fatalf("registerTypes: %v", err)
	}
	closed := make(chan uint32, 1)
	serverPending := newPendingConnection(serverConn, serverReg, nil, func(ctx *StreamContext) {
		closed <- ctx.stream.ID()
	})
	serverPending.connection.config = connConfig{
		version:  protocolVersion,
		codecID:  CodecMsgpackMap,
		codec:    mustCodec(t, CodecMsgpackMap),
		maxFrame: defaultMaxFrame,
	}
	server := serverPending.connection
	server.start()
	client := newTestConnection(t, clientConn, newRegistry())
	defer client.Close()
	defer server.Close()

	stream := client.NewStream()
	reply, err := stream.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("SendRecv: %v", err)
	}
	if reply.Text != "hi" {
		t.Fatalf("reply got %q want %q", reply.Text, "hi")
	}
	stream.Close()
	stream.Close()
	if err := stream.Send(&connTestEchoRequest{Text: "after"}); !errors.As(err, &ErrClosed{}) {
		t.Fatalf("Send after Close got %v", err)
	}

	select {
	case id := <-closed:
		if id != stream.ID() {
			t.Fatalf("closed stream got %d want %d", id, stream.ID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for peer stream close")
	}
	if got := server.DebugState().StreamCount; got != 0 {
		t.Fatalf("server stream count got %d want 0", got)
	}
}

func TestNewStreamAfterStreamIDsExhausted(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()
	conn.nextStreamID.Store(uint64(maxStreamID) - 1)

	last := conn.NewStream()
	if last.ID() != maxStreamID {
		t.Fatalf("last stream id got %#x want %#x", last.ID(), maxStreamID)
	}
	for i := 0; i < 2; i++ {
		stream := conn.NewStream()
		if err := stream.Send(&OkReply{}); !errors.As(err, &ErrClosed{}) {
			t.Fatalf("Send got %v, want ErrClosed", err)
		}
		if _, err := stream.Recv[*OkReply](); !errors.As(err, &ErrClosed{}) {
			t.Fatalf("Recv got %v, want ErrClosed", err)
		}
		stream.Close()
	}

	state := conn.DebugState()
	if state.LastFailureCode != DebugFailureStreamIDsExhausted {
		t.Fatalf("last failure got %q want %q", state.LastFailureCode, DebugFailureStreamIDsExhausted)
	}
	if state.StreamCount != 1 {
		t.Fatalf("stream count got %d want 1", state.StreamCount)
	}
	if err := last.Send(&OkReply{}); err != nil {
		t.Fatalf("Send on last valid stream: %v", err)
	}
}

func TestPeerOpenedStreamCloseSendsNoFIN(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()

	if err := conn.acceptFrame(1, 0, nil); err != nil {
		t.Fatalf("acceptFrame: %v", err)
	}
	conn.streamsMu.Lock()
	stream := conn.streams[1].stream
	conn.streamsMu.Unlock()

	stream.Close()
	if got := len(conn.outbound); got != 0 {
		t.Fatalf("queued %d frames on close, want 0", got)
	}
	if got := conn.DebugState().StreamCount; got != 0 {
		t.Fatalf("stream count got %d want 0", got)
	}
}

func TestLocallyClosedPeerStreamIsNotReopened(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()

	if err := conn.acceptFrame(1, 0, nil); err != nil {
		t.Fatalf("acceptFrame: %v", err)
	}
	conn.streamsMu.Lock()
	stream := conn.streams[1].stream
	conn.streamsMu.Unlock()
	stream.Close()

	if err := conn.acceptFrame(1, 0, nil); err != nil {
		t.Fatalf("acceptFrame after close: %v", err)
	}
	if got := conn.DebugState().StreamCount; got != 0 {
		t.Fatalf("stream count got %d, want 0: the closed stream was opened again", got)
	}
	if got := conn.debug.streamsOpened.Load(); got != 1 {
		t.Fatalf("streams opened got %d, want 1", got)
	}

	// The peer's FIN ends the stream, so its ID is forgotten.
	if err := conn.acceptFrame(1, FrameFlagFIN, nil); err != nil {
		t.Fatalf("acceptFrame FIN: %v", err)
	}
	conn.streamsMu.Lock()
	reserved := len(conn.closedPeerStreams)
	conn.streamsMu.Unlock()
	if reserved != 0 {
		t.Fatalf("%d stream IDs still reserved after FIN, want 0", reserved)
	}
}

func TestProtocolErrorOnOpenedStreamSendsFIN(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()

	stream := conn.NewStream()
	if err := stream.inboxQ(rawMessage{Wire: "am.test.Other", Codec: CodecMsgpackMap}); err != nil {
		t.Fatalf("inboxQ: %v", err)
	}
	if _, err := stream.Recv[*connTestEchoReply](); !errors.As(err, &ErrTypeMismatch{}) {
		t.Fatalf("Recv got %v, want ErrTypeMismatch", err)
	}

	var flags []FrameFlags
	for len(conn.outbound) > 0 {
		flags = append(flags, (<-conn.outbound).flags)
	}
	if len(flags) != 2 || flags[0] != 0 || flags[1] != FrameFlagFIN {
		t.Fatalf("queued frame flags got %v, want [0 FIN]", flags)
	}
}

func TestRecoveryEnqueueReturnsWhenClosedWhileQueued(t *testing.T) {
	for _, tc := range []struct {
		name    string
		op      func(*Connection, *Stream) error
		wantErr bool
	}{
		{name: "Send", op: func(conn *Connection, _ *Stream) error { return conn.Send(&OkReply{}) }, wantErr: true},
		{name: "StreamClose", op: func(_ *Connection, stream *Stream) error { stream.Close(); return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newDebugTestConnection(t)
			conn.config.version = protocolVersionV3
			conn.recovery = newClientRecoveryState(
				defaultClientRecoveryOptions(),
				defaultServerRecoveryOptions().negotiated(),
				"tcp://127.0.0.1:1",
				100*time.Millisecond,
				recoveryToken{1},
				recoveryToken{2},
			)
			stream := conn.NewStream()

			// No writer loop runs, so the frame stays queued the way it does
			// while the transport is detached.
			done := make(chan error, 1)
			go func() { done <- tc.op(conn, stream) }()
			waitForCondition(t, time.Second, func() bool { return len(conn.outbound) == 1 })

			conn.Close()
			select {
			case err := <-done:
				if tc.wantErr && !errors.As(err, &ErrClosed{}) {
					t.Fatalf("%s got %v, want ErrClosed", tc.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s still blocked after Close", tc.name)
			}
		})
	}
}

func TestInboundFrameForLocallyClosedStreamIsDropped(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()

	stream := conn.NewStream()
	stream.Close()
	payload, err := conn.encodeMessage(&OkReply{})
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	if err := conn.acceptFrame(stream.ID(), 0, payload); err != nil {
		t.Fatalf("acceptFrame: %v", err)
	}
	if got := conn.DebugState().StreamCount; got != 0 {
		t.Fatalf("stream count got %d want 0", got)
	}
}

func TestFrameForStreamClosedDuringDeliveryIsDropped(t *testing.T) {
	conn := newDebugTestConnection(t)
	defer conn.Close()

	stream := conn.NewStream()
	// Close the channels but keep the map entry, as when Close runs between
	// the reader's lookup and its enqueue.
	stream.close()
	payload, err := conn.encodeMessage(&OkReply{})
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	if err := conn.acceptFrame(stream.ID(), 0, payload); err != nil {
		t.Fatalf("acceptFrame: %v", err)
	}
	if conn.DebugState().Closed {
		t.Fatal("connection closed after a frame for a closing stream")
	}
}

func newTestConnection(t *testing.T, conn net.Conn, reg *registry) *Connection {
	t.Helper()
	pending := newPendingConnection(conn, reg, nil, nil)
	pending.connection.config = connConfig{
		version:  protocolVersion,
		codecID:  CodecMsgpackMap,
		codec:    mustCodec(t, CodecMsgpackMap),
		maxFrame: defaultMaxFrame,
	}
	pending.connection.start()
	return pending.connection
}

func TestConnectionBuildHeaderTooLarge(t *testing.T) {
	conn := &Connection{
		config: connConfig{
			version:  protocolVersion,
			maxFrame: 4,
		},
	}
	_, _, err := conn.buildHeader(1, 0, 0, 5)
	var tooLarge ErrFrameTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestConnectionParseHeaderVersion(t *testing.T) {
	conn := &Connection{
		config: connConfig{version: protocolVersion},
	}
	header := make([]byte, frameHeaderLen)
	header[0] = protocolVersion + 1
	_, _, _, _, err := conn.parseHeader(header)
	var unsupported ErrUnsupportedFrameVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedFrameVersion, got %v", err)
	}
}

func TestConnectionParseHeaderRejectsUnsupportedFlags(t *testing.T) {
	conn := &Connection{
		config: connConfig{version: protocolVersion},
	}
	for _, flags := range []FrameFlags{FrameFlagMeta, FrameFlagExt, 1 << 2} {
		header := make([]byte, frameHeaderLen)
		header[0] = protocolVersion
		header[1] = byte(flags | FrameFlagFIN)
		_, _, _, _, err := conn.parseHeader(header)
		var invalid ErrInvalidMessage
		if !errors.As(err, &invalid) {
			t.Fatalf("flags %#02x: expected ErrInvalidMessage, got %v", byte(flags), err)
		}
	}
}

func TestConnectionReadFrameTooLarge(t *testing.T) {
	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()

	conn := &Connection{
		conn:   reader,
		config: connConfig{version: protocolVersion, maxFrame: 1},
	}

	done := make(chan error, 1)
	go func() {
		header := make([]byte, frameHeaderLen)
		header[0] = protocolVersion
		binary.BigEndian.PutUint32(header[2:6], 1)
		binary.BigEndian.PutUint32(header[6:10], 2)
		_, err := writer.Write(header)
		done <- err
	}()

	_, _, _, _, err := conn.readFrame()
	var tooLarge ErrFrameTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("writer error: %v", err)
	}
}

func TestConnectionEncodeUnsupportedCodec(t *testing.T) {
	conn := &Connection{
		config: connConfig{codecID: CodecID(99)},
	}
	_, err := conn.encodeMessage(&OkReply{})
	var unsupported ErrUnsupportedCodec
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedCodec, got %v", err)
	}
}

func TestConnectionSendRecvWithHandler(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	serverReg := newRegistry()
	if err := registerTypes(serverReg, &connTestEchoRequest{}, &connTestEchoReply{}, &connTestErrRequest{}); err != nil {
		t.Fatalf("registerTypes: %v", err)
	}
	client := newTestConnection(t, clientConn, newRegistry())
	server := newTestConnection(t, serverConn, serverReg)
	defer client.Close()
	defer server.Close()

	reply, err := client.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("SendRecv: %v", err)
	}
	if reply.Text != "hi" {
		t.Fatalf("reply got %q want %q", reply.Text, "hi")
	}

	_, err = client.SendRecv[*connTestEchoReply](&connTestErrRequest{})
	var remote ErrRemote
	if !errors.As(err, &remote) {
		t.Fatalf("expected ErrRemote, got %v", err)
	}
	if remote.Code != "handler_error" {
		t.Fatalf("remote code got %q want %q", remote.Code, "handler_error")
	}
	if remote.Message != "boom" {
		t.Fatalf("remote message got %q want %q", remote.Message, "boom")
	}
}

func TestConnectionWriteFrameShortWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	shortConn := &shortWriteConn{Conn: clientConn, maxWrite: 3}
	writer := &Connection{
		conn: shortConn,
		config: connConfig{
			version:  protocolVersion,
			maxFrame: defaultMaxFrame,
		},
	}
	reader := &Connection{
		conn: serverConn,
		config: connConfig{
			version:  protocolVersion,
			maxFrame: defaultMaxFrame,
		},
	}

	payload := []byte("hello, short write")
	errCh := make(chan error, 1)
	go func() {
		streamID, seq, _, gotPayload, err := reader.readFrame()
		if err != nil {
			errCh <- err
			return
		}
		if streamID != 7 {
			errCh <- errors.New("unexpected stream id")
			return
		}
		if seq != 0 {
			errCh <- errors.New("unexpected seq")
			return
		}
		if !bytes.Equal(gotPayload, payload) {
			errCh <- errors.New("unexpected payload")
			return
		}
		errCh <- nil
	}()

	// Use writeFrameTo directly with the short-write conn to test writeFull retry logic.
	if err := writer.writeFrameTo(shortConn, 7, 0, 0, payload); err != nil {
		t.Fatalf("writeFrameTo: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("readFrame: %v", err)
	}
}

func TestConnectionWriteReadFrameV3(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	writer := &Connection{
		conn: clientConn,
		config: connConfig{
			version:  protocolVersionV3,
			maxFrame: defaultMaxFrame,
		},
	}
	writer.writer = bufio.NewWriter(writer.conn)
	reader := &Connection{
		conn: serverConn,
		config: connConfig{
			version:  protocolVersionV3,
			maxFrame: defaultMaxFrame,
		},
	}

	payload := []byte("hello, v3")
	errCh := make(chan error, 1)
	go func() {
		streamID, seq, _, gotPayload, err := reader.readFrame()
		if err != nil {
			errCh <- err
			return
		}
		if streamID != 9 {
			errCh <- errors.New("unexpected stream id")
			return
		}
		if seq != 42 {
			errCh <- errors.New("unexpected seq")
			return
		}
		if !bytes.Equal(gotPayload, payload) {
			errCh <- errors.New("unexpected payload")
			return
		}
		errCh <- nil
	}()

	if err := writer.writeFrame(9, 42, 0, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("readFrame: %v", err)
	}
}

func TestWriteFullZeroWrite(t *testing.T) {
	err := writeFull(zeroWriter{}, []byte("x"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected io.ErrShortWrite, got %v", err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

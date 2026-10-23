package adaptivemsg

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
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

package adaptivemsg

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

func newDebugTestConnection(t *testing.T) *Connection {
	t.Helper()
	codec, ok := codecByID(CodecMsgpackMap)
	if !ok {
		t.Fatal("msgpack map codec not registered")
	}
	conn := newPendingConnection(nil, newRegistrySnapshot(), nil, nil).connection
	conn.config = connConfig{
		version:  protocolVersionV2,
		codecID:  CodecMsgpackMap,
		codec:    codec,
		maxFrame: defaultMaxFrame,
	}
	return conn
}

func TestConnectionAndStreamDebugState(t *testing.T) {
	conn := newDebugTestConnection(t)
	stream := conn.defaultStream()
	stream.SetRecvTimeout(150 * time.Millisecond)

	if err := stream.Send(&OkReply{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	payload, err := conn.encodeMessage(&OkReply{})
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	raw, err := newRawMessageFromPayload(conn.config.codecID, payload)
	if err != nil {
		t.Fatalf("newRawMessageFromPayload: %v", err)
	}
	if err := stream.core.inboxQ(raw); err != nil {
		t.Fatalf("inboxQ: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv: %v", err)
	}

	streamState := stream.DebugState()
	if streamState.ID != defaultStreamID {
		t.Fatalf("stream id got %d want %d", streamState.ID, defaultStreamID)
	}
	if streamState.Counters.DataMessagesSent != 1 {
		t.Fatalf("stream sent got %d want 1", streamState.Counters.DataMessagesSent)
	}
	if streamState.Counters.DataMessagesReceived != 1 {
		t.Fatalf("stream recv got %d want 1", streamState.Counters.DataMessagesReceived)
	}
	if streamState.RecvTimeout != 150*time.Millisecond {
		t.Fatalf("recv timeout got %v want %v", streamState.RecvTimeout, 150*time.Millisecond)
	}

	connState := conn.DebugState()
	if connState.StreamCount != 1 {
		t.Fatalf("stream count got %d want 1", connState.StreamCount)
	}
	if connState.Counters.StreamsOpened != 1 {
		t.Fatalf("streams opened got %d want 1", connState.Counters.StreamsOpened)
	}
	if connState.Counters.DataMessagesSent != 1 {
		t.Fatalf("connection sent got %d want 1", connState.Counters.DataMessagesSent)
	}
	if connState.Counters.DataMessagesReceived != 1 {
		t.Fatalf("connection recv got %d want 1", connState.Counters.DataMessagesReceived)
	}
	if len(connState.Streams) != 1 {
		t.Fatalf("debug streams got %d want 1", len(connState.Streams))
	}

	stream.Close()
	connState = conn.DebugState()
	if connState.StreamCount != 0 {
		t.Fatalf("stream count after close got %d want 0", connState.StreamCount)
	}
	if connState.Counters.StreamsClosed != 1 {
		t.Fatalf("streams closed got %d want 1", connState.Counters.StreamsClosed)
	}
}

func TestConnectionDebugFrameCounters(t *testing.T) {
	conn := newDebugTestConnection(t)
	var buf bytes.Buffer

	if err := conn.writeFrameTo(&buf, 7, 0, []byte("abc")); err != nil {
		t.Fatalf("writeFrameTo: %v", err)
	}
	streamID, seq, payload, err := conn.readFrameFrom(&buf)
	if err != nil {
		t.Fatalf("readFrameFrom: %v", err)
	}
	if streamID != 7 || seq != 0 || string(payload) != "abc" {
		t.Fatalf("frame roundtrip got stream=%d seq=%d payload=%q", streamID, seq, string(payload))
	}

	state := conn.DebugState()
	if state.Counters.FramesWritten != 1 {
		t.Fatalf("frames written got %d want 1", state.Counters.FramesWritten)
	}
	if state.Counters.FramesRead != 1 {
		t.Fatalf("frames read got %d want 1", state.Counters.FramesRead)
	}
	if state.Counters.BytesWritten == 0 || state.Counters.BytesRead == 0 {
		t.Fatalf("expected non-zero byte counters, got write=%d read=%d", state.Counters.BytesWritten, state.Counters.BytesRead)
	}
}

func TestConnectionDebugRecoveryState(t *testing.T) {
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

	left, right := net.Pipe()
	defer right.Close()
	conn.attachTransport(left, 0)
	if !conn.detachTransport(left) {
		t.Fatal("detachTransport returned false")
	}

	state := conn.DebugState()
	if state.Recovery == nil {
		t.Fatal("expected recovery debug state")
	}
	if state.Counters.TransportAttaches != 1 {
		t.Fatalf("transport attaches got %d want 1", state.Counters.TransportAttaches)
	}
	if state.Counters.TransportDetaches != 1 {
		t.Fatalf("transport detaches got %d want 1", state.Counters.TransportDetaches)
	}
	if state.Recovery.TransportAttached {
		t.Fatal("expected detached transport")
	}
	if state.Recovery.Role != "client" {
		t.Fatalf("recovery role got %q want client", state.Recovery.Role)
	}
}

func TestStreamAndConnectionLastFailure(t *testing.T) {
	conn := newDebugTestConnection(t)
	stream := conn.defaultStream()
	stream.SetRecvTimeout(5 * time.Millisecond)

	if _, err := stream.Recv(); err == nil {
		t.Fatal("expected recv timeout")
	} else {
		var timeout ErrRecvTimeout
		if !errors.As(err, &timeout) {
			t.Fatalf("expected ErrRecvTimeout, got %v", err)
		}
	}

	streamState := stream.DebugState()
	if streamState.LastFailure == "" {
		t.Fatal("expected stream last failure to be set")
	}
	if streamState.LastFailureCode != DebugFailureStreamRecvTimeout {
		t.Fatalf("expected stream failure code %q got %q", DebugFailureStreamRecvTimeout, streamState.LastFailureCode)
	}
	if streamState.LastFailureAt.IsZero() {
		t.Fatal("expected stream last failure timestamp")
	}

	connState := conn.DebugState()
	if connState.LastFailure == "" {
		t.Fatal("expected connection last failure to be set")
	}
	if connState.LastFailureCode != DebugFailureStreamRecvTimeout {
		t.Fatalf("expected connection failure code %q got %q", DebugFailureStreamRecvTimeout, connState.LastFailureCode)
	}
	if connState.LastFailureAt.IsZero() {
		t.Fatal("expected connection last failure timestamp")
	}
}

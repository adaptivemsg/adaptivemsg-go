package adaptivemsg

import (
	"errors"
	"testing"
	"time"
)

type peekWireMsg struct {
	A string `msgpack:"a"`
}

func (*peekWireMsg) WireName() string {
	return "am.test.PeekWireMsg"
}

type ifaceMsg struct {
	A string `msgpack:"a"`
}

func (*ifaceMsg) WireName() string {
	return "am.test.InterfaceMsg"
}

type expectedMsg struct {
	A string `msgpack:"a"`
}

func (*expectedMsg) WireName() string {
	return "am.test.ExpectedMsg"
}

func TestStreamPeekWire(t *testing.T) {
	conn := &Connection{closeCh: make(chan struct{})}
	core := &streamCore{
		connection: conn,
		inbox:      make(chan rawMessage, 1),
	}
	stream := &Stream[peekWireMsg]{core: core}

	payload, err := encodeMap(&peekWireMsg{A: "hi"})
	if err != nil {
		t.Fatalf("encodeMap: %v", err)
	}
	raw, err := newRawMessageFromPayload(CodecMsgpackMap, payload)
	if err != nil {
		t.Fatalf("newRawMessageFromPayload: %v", err)
	}
	core.inbox <- raw

	wire, err := stream.PeekWire()
	if err != nil {
		t.Fatalf("PeekWire: %v", err)
	}
	if wire != raw.Wire {
		t.Fatalf("PeekWire got %q want %q", wire, raw.Wire)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if msg.A != "hi" {
		t.Fatalf("Recv got %q want %q", msg.A, "hi")
	}
}

func TestStreamRecvConcurrent(t *testing.T) {
	core := &streamCore{connection: &Connection{closeCh: make(chan struct{})}}
	release, err := core.recvGuard()
	if err != nil {
		t.Fatalf("recvGuard: %v", err)
	}
	defer release()
	stream := &Stream[Message]{core: core}

	_, err = stream.Recv()
	var concurrent ErrConcurrentRecv
	if !errors.As(err, &concurrent) {
		t.Fatalf("expected ErrConcurrentRecv, got %v", err)
	}
}

func TestStreamRecvTimeout(t *testing.T) {
	core := &streamCore{
		connection: &Connection{closeCh: make(chan struct{})},
		inbox:      make(chan rawMessage),
	}
	stream := &Stream[Message]{core: core}
	stream.SetRecvTimeout(20 * time.Millisecond)

	_, err := stream.Recv()
	var timeout ErrRecvTimeout
	if !errors.As(err, &timeout) {
		t.Fatalf("expected ErrRecvTimeout, got %v", err)
	}
}

func TestStreamRecvInterfaceUsesRegistry(t *testing.T) {
	reg := newRegistry()
	if err := registerTypes(reg, &ifaceMsg{}); err != nil {
		t.Fatalf("registerTypes: %v", err)
	}
	conn := &Connection{registry: reg, closeCh: make(chan struct{})}
	core := &streamCore{
		connection: conn,
		inbox:      make(chan rawMessage, 1),
	}
	stream := &Stream[Message]{core: core}

	payload, err := encodeMap(&ifaceMsg{A: "hi"})
	if err != nil {
		t.Fatalf("encodeMap: %v", err)
	}
	raw, err := newRawMessageFromPayload(CodecMsgpackMap, payload)
	if err != nil {
		t.Fatalf("newRawMessageFromPayload: %v", err)
	}
	core.inbox <- raw

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	got, ok := msg.(*ifaceMsg)
	if !ok {
		t.Fatalf("Recv got %T", msg)
	}
	if got.A != "hi" {
		t.Fatalf("Recv got %q want %q", got.A, "hi")
	}
}

func TestStreamRecvInterfaceUnknown(t *testing.T) {
	reg := newRegistry()
	conn := &Connection{registry: reg, closeCh: make(chan struct{})}
	core := &streamCore{
		connection: conn,
		inbox:      make(chan rawMessage, 1),
	}
	stream := &Stream[Message]{core: core}

	payload, err := encodeMap(&ifaceMsg{A: "hi"})
	if err != nil {
		t.Fatalf("encodeMap: %v", err)
	}
	raw, err := newRawMessageFromPayload(CodecMsgpackMap, payload)
	if err != nil {
		t.Fatalf("newRawMessageFromPayload: %v", err)
	}
	core.inbox <- raw

	_, err = stream.Recv()
	var unknown ErrUnknownMessage
	if !errors.As(err, &unknown) {
		t.Fatalf("expected ErrUnknownMessage, got %v", err)
	}
}

func TestStreamTypeMismatchCloses(t *testing.T) {
	conn := &Connection{
		config:   connConfig{codecID: CodecMsgpackMap, codec: mustCodec(t, CodecMsgpackMap)},
		outbound: make(chan outboundFrame, 1),
		streams:  make(map[uint32]*StreamContext),
		closeCh:  make(chan struct{}),
	}
	core := &streamCore{
		id:         1,
		connection: conn,
		inbox:      make(chan rawMessage, 1),
	}
	ctx := &StreamContext{stream: &Stream[Message]{core: core}}
	conn.streams[1] = ctx
	stream := &Stream[expectedMsg]{core: core}

	core.inbox <- rawMessage{Wire: "am.test.Other", Codec: CodecMsgpackMap}
	_, err := stream.Recv()
	var mismatch ErrTypeMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected ErrTypeMismatch, got %v", err)
	}

	select {
	case _, ok := <-core.inbox:
		if ok {
			t.Fatalf("expected inbox closed")
		}
	default:
		t.Fatalf("expected inbox closed")
	}

	select {
	case frame := <-conn.outbound:
		wire, _, err := decodeMapEnvelope(frame.payload)
		if err != nil {
			t.Fatalf("decodeMapEnvelope: %v", err)
		}
		if wire != errorReplyWireName {
			t.Fatalf("expected error reply, got %q", wire)
		}
	default:
		t.Fatalf("expected error reply frame")
	}
}

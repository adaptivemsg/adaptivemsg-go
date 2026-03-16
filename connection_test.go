package adaptivemsg

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
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
	_, err := conn.buildHeader(1, 5)
	var tooLarge ErrFrameTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestConnectionParseHeaderVersion(t *testing.T) {
	conn := &Connection{
		config: connConfig{version: protocolVersion},
	}
	var header [frameHeaderLen]byte
	header[0] = protocolVersion + 1
	_, _, err := conn.parseHeader(header)
	var unsupported ErrUnsupportedFrameVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedFrameVersion, got %v", err)
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

	_, _, err := conn.readFrame()
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

	reply, err := SendRecvAs[*connTestEchoReply](client, &connTestEchoRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("SendRecvAs: %v", err)
	}
	if reply.Text != "hi" {
		t.Fatalf("reply got %q want %q", reply.Text, "hi")
	}

	_, err = SendRecvAs[*connTestEchoReply](client, &connTestErrRequest{})
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

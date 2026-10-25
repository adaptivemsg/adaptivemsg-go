package adaptivemsg

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func writeHandshakeRequest(conn net.Conn, version byte, codecs []byte, maxFrame uint32) error {
	req := make([]byte, handshakeHeaderLen)
	copy(req[0:2], handshakeMagic[:])
	req[2] = version
	req[3] = byte(len(codecs))
	req[4] = 0
	req[5] = 0
	req[6] = 0
	req[7] = 0
	binary.BigEndian.PutUint32(req[8:12], maxFrame)
	if err := writeFull(conn, req); err != nil {
		return err
	}
	if len(codecs) > 0 {
		return writeFull(conn, codecs)
	}
	return nil
}

func TestHandshakeSuccess(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	var serverCfg connConfig
	go func() {
		cfg, err := handshakeServer(serverConn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, 1024, false)
		serverCfg = cfg
		errCh <- err
	}()

	clientCfg, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap, CodecMsgpackCompact}, 2048, protocolVersionV2)
	if err != nil {
		t.Fatalf("handshakeClient: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handshakeServer: %v", err)
	}
	if serverCfg.codecID != CodecMsgpackMap {
		t.Fatalf("server codec got %v want %v", serverCfg.codecID, CodecMsgpackMap)
	}
	if serverCfg.maxFrame != 1024 {
		t.Fatalf("server maxFrame got %d want %d", serverCfg.maxFrame, 1024)
	}
	if clientCfg.maxFrame != 1024 {
		t.Fatalf("client maxFrame got %d want %d", clientCfg.maxFrame, 1024)
	}
}

func TestHandshakeServerTooManyCodecs(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame, false)
		errCh <- err
	}()

	codecs := make([]byte, maxCodecCount+1)
	for i := range codecs {
		codecs[i] = byte(CodecMsgpackMap)
	}
	if err := writeHandshakeRequest(clientConn, protocolVersion, codecs, 0); err != nil {
		t.Fatalf("client write: %v", err)
	}
	response := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(clientConn, response); err != nil {
		t.Fatalf("client read: %v", err)
	}

	var tooMany ErrTooManyCodecs
	if err := <-errCh; !errors.As(err, &tooMany) {
		t.Fatalf("expected ErrTooManyCodecs, got %v", err)
	}
}

func TestHandshakeServerNoCommonCodec(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame, false)
		errCh <- err
	}()

	if err := writeHandshakeRequest(clientConn, protocolVersion, []byte{99}, 0); err != nil {
		t.Fatalf("client write: %v", err)
	}
	response := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(clientConn, response); err != nil {
		t.Fatalf("client read: %v", err)
	}

	var noCommon ErrNoCommonCodec
	if err := <-errCh; !errors.As(err, &noCommon) {
		t.Fatalf("expected ErrNoCommonCodec, got %v", err)
	}
}

func TestHandshakeServerBadMagic(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame, false)
		errCh <- err
	}()

	req := make([]byte, handshakeHeaderLen)
	req[0] = 'B'
	req[1] = 'X'
	if _, err := clientConn.Write(req); err != nil {
		t.Fatalf("client write: %v", err)
	}

	var badMagic ErrBadHandshakeMagic
	if err := <-errCh; !errors.As(err, &badMagic) {
		t.Fatalf("expected ErrBadHandshakeMagic, got %v", err)
	}
}

func TestHandshakeClientRejected(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, handshakeHeaderLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		codecs := make([]byte, buf[3])
		if len(codecs) > 0 {
			if _, err := io.ReadFull(serverConn, codecs); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- writeHandshakeReply(serverConn, 0, protocolVersion, 0, 0)
	}()

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0, protocolVersionV2)
	var noCommon ErrNoCommonCodec
	if !errors.As(err, &noCommon) {
		t.Fatalf("expected ErrNoCommonCodec, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func TestHandshakeClientBadMagic(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, handshakeHeaderLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		codecs := make([]byte, buf[3])
		if len(codecs) > 0 {
			if _, err := io.ReadFull(serverConn, codecs); err != nil {
				errCh <- err
				return
			}
		}
		reply := make([]byte, handshakeHeaderLen)
		reply[0] = 'B'
		reply[1] = 'X'
		reply[2] = 1
		reply[3] = protocolVersion
		_, err := serverConn.Write(reply)
		errCh <- err
	}()

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0, protocolVersionV2)
	var badMagic ErrBadHandshakeMagic
	if !errors.As(err, &badMagic) {
		t.Fatalf("expected ErrBadHandshakeMagic, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func TestHandshakeClientUnsupportedVersion(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, handshakeHeaderLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		codecs := make([]byte, buf[3])
		if len(codecs) > 0 {
			if _, err := io.ReadFull(serverConn, codecs); err != nil {
				errCh <- err
				return
			}
		}
		reply := make([]byte, handshakeHeaderLen)
		copy(reply[0:2], handshakeMagic[:])
		reply[2] = 1
		reply[3] = protocolVersion + 1
		_, err := serverConn.Write(reply)
		errCh <- err
	}()

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0, protocolVersionV2)
	var unsupported ErrUnsupportedFrameVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedFrameVersion, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func TestHandshakeShortWrites(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(&shortWriteConn{Conn: serverConn, maxWrite: 3}, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, 1024, false)
		errCh <- err
	}()

	clientCfg, err := handshakeClient(&shortWriteConn{Conn: clientConn, maxWrite: 2}, []CodecID{CodecMsgpackMap, CodecMsgpackCompact}, 2048, protocolVersionV2)
	if err != nil {
		t.Fatalf("handshakeClient: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handshakeServer: %v", err)
	}
	if clientCfg.codecID != CodecMsgpackMap {
		t.Fatalf("client codec got %v want %v", clientCfg.codecID, CodecMsgpackMap)
	}
	if clientCfg.maxFrame != 1024 {
		t.Fatalf("client maxFrame got %d want %d", clientCfg.maxFrame, 1024)
	}
}

func TestHandshakeV3Success(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	var serverCfg connConfig
	go func() {
		cfg, err := handshakeServer(serverConn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, 1024, true)
		serverCfg = cfg
		errCh <- err
	}()

	clientCfg, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap, CodecMsgpackCompact}, 2048, protocolVersionV3)
	if err != nil {
		t.Fatalf("handshakeClient: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handshakeServer: %v", err)
	}
	if clientCfg.version != protocolVersionV3 {
		t.Fatalf("client version got %d want %d", clientCfg.version, protocolVersionV3)
	}
	if serverCfg.version != protocolVersionV3 {
		t.Fatalf("server version got %d want %d", serverCfg.version, protocolVersionV3)
	}
}

func TestHandshakeV3RejectedByLegacyServer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame, false)
		errCh <- err
	}()

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0, protocolVersionV3)
	var unsupported ErrUnsupportedFrameVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedFrameVersion, got %v", err)
	}
	if unsupported.Version != protocolVersionV2 {
		t.Fatalf("unsupported version got %d want %d", unsupported.Version, protocolVersionV2)
	}
	if err := <-errCh; err == nil {
		t.Fatalf("expected server-side unsupported version error")
	}
}

func TestServerClosesConnectionOnFrameAboveMaxFrame(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()
	conn, err := net.Dial("tcp", strings.TrimPrefix(addr, "tcp://"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	config, err := handshakeClient(conn, []CodecID{CodecMsgpackCompact}, defaultMaxFrame, protocolVersionV2)
	if err != nil {
		t.Fatalf("handshakeClient: %v", err)
	}
	if config.maxFrame != defaultMaxFrame {
		t.Fatalf("negotiated max frame %d, want %d", config.maxFrame, defaultMaxFrame)
	}

	// Send only the header: the server must reject it without waiting for, or
	// allocating, the payload.
	header := make([]byte, frameHeaderLen)
	header[0] = protocolVersionV2
	binary.BigEndian.PutUint32(header[6:10], defaultMaxFrame+1)
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("Read got %v, want EOF from the server closing the connection", err)
	}
}

func TestServerWithMaxFrameLimitsConnection(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer().WithMaxFrame(1024))
	defer stop()
	conn, err := NewClient().WithRecvTimeout(2 * time.Second).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	big := &connTestEchoRequest{Text: strings.Repeat("x", 2048)}
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

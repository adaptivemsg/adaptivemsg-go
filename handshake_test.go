package adaptivemsg

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
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
		cfg, err := handshakeServer(serverConn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, 1024)
		serverCfg = cfg
		errCh <- err
	}()

	clientCfg, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap, CodecMsgpackCompact}, 2048)
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
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame)
		errCh <- err
	}()

	req := make([]byte, handshakeHeaderLen)
	copy(req[0:2], handshakeMagic[:])
	req[2] = protocolVersion
	req[3] = byte(maxCodecCount + 1)
	if _, err := clientConn.Write(req); err != nil {
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
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame)
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
		_, err := handshakeServer(serverConn, []CodecID{CodecMsgpackMap}, defaultMaxFrame)
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

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0)
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

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0)
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

	_, err := handshakeClient(clientConn, []CodecID{CodecMsgpackMap}, 0)
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
		_, err := handshakeServer(&shortWriteConn{Conn: serverConn, maxWrite: 3}, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, 1024)
		errCh <- err
	}()

	clientCfg, err := handshakeClient(&shortWriteConn{Conn: clientConn, maxWrite: 2}, []CodecID{CodecMsgpackMap, CodecMsgpackCompact}, 2048)
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

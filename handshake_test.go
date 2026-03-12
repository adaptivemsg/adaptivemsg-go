package adaptivemsg

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
)

func makeHandshakeRequest(min, max byte, codec byte, maxFrame uint32) []byte {
	req := make([]byte, handshakeClientLen)
	copy(req[0:2], handshakeMagic[:])
	req[2] = min
	req[3] = max
	req[4] = codec
	binary.BigEndian.PutUint16(req[5:7], 0)
	req[7] = 0
	binary.BigEndian.PutUint32(req[8:12], maxFrame)
	return req
}

func TestHandshakeSuccess(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	var serverCfg connConfig
	go func() {
		cfg, err := handshakeServer(serverConn, 1024)
		serverCfg = cfg
		errCh <- err
	}()

	clientCfg, err := handshakeClient(clientConn, CodecMap, 2048)
	if err != nil {
		t.Fatalf("handshakeClient: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("handshakeServer: %v", err)
	}
	if serverCfg.codec != CodecMap {
		t.Fatalf("server codec got %v want %v", serverCfg.codec, CodecMap)
	}
	if serverCfg.maxFrame != 1024 {
		t.Fatalf("server maxFrame got %d want %d", serverCfg.maxFrame, 1024)
	}
	if clientCfg.maxFrame != 1024 {
		t.Fatalf("client maxFrame got %d want %d", clientCfg.maxFrame, 1024)
	}
}

func TestHandshakeServerUnsupportedCodec(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, defaultMaxFrame)
		errCh <- err
	}()

	if _, err := clientConn.Write(makeHandshakeRequest(protocolVersion, protocolVersion, 99, 0)); err != nil {
		t.Fatalf("client write: %v", err)
	}
	response := make([]byte, handshakeServerLen)
	if _, err := io.ReadFull(clientConn, response); err != nil {
		t.Fatalf("client read: %v", err)
	}

	var unsupported ErrUnsupportedCodec
	if err := <-errCh; !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedCodec, got %v", err)
	}
}

func TestHandshakeServerNoCommonVersion(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, defaultMaxFrame)
		errCh <- err
	}()

	if _, err := clientConn.Write(makeHandshakeRequest(protocolVersion+1, protocolVersion+1, byte(CodecMap), 0)); err != nil {
		t.Fatalf("client write: %v", err)
	}
	response := make([]byte, handshakeServerLen)
	if _, err := io.ReadFull(clientConn, response); err != nil {
		t.Fatalf("client read: %v", err)
	}

	var noCommon ErrNoCommonVersion
	if err := <-errCh; !errors.As(err, &noCommon) {
		t.Fatalf("expected ErrNoCommonVersion, got %v", err)
	}
}

func TestHandshakeServerBadMagic(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := handshakeServer(serverConn, defaultMaxFrame)
		errCh <- err
	}()

	req := makeHandshakeRequest(protocolVersion, protocolVersion, byte(CodecMap), 0)
	req[0] = 'B'
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
		buf := make([]byte, handshakeClientLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		errCh <- writeHandshakeReply(serverConn, protocolVersion, 0, 0, 0)
	}()

	_, err := handshakeClient(clientConn, CodecMap, 0)
	var rejected ErrHandshakeRejected
	if !errors.As(err, &rejected) {
		t.Fatalf("expected ErrHandshakeRejected, got %v", err)
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
		buf := make([]byte, handshakeClientLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		reply := make([]byte, handshakeServerLen)
		reply[0] = 'B'
		reply[1] = 'X'
		reply[2] = protocolVersion
		reply[3] = 1
		_, err := serverConn.Write(reply)
		errCh <- err
	}()

	_, err := handshakeClient(clientConn, CodecMap, 0)
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
		buf := make([]byte, handshakeClientLen)
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			errCh <- err
			return
		}
		reply := make([]byte, handshakeServerLen)
		copy(reply[0:2], handshakeMagic[:])
		reply[2] = protocolVersion + 1
		reply[3] = 1
		_, err := serverConn.Write(reply)
		errCh <- err
	}()

	_, err := handshakeClient(clientConn, CodecMap, 0)
	var unsupported ErrUnsupportedFrameVersion
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedFrameVersion, got %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server write: %v", err)
	}
}

package adaptivemsg

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestAttachResponseRoundTripNegotiatedOptions(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	want := attachResponse{
		status:       attachStatusOK,
		connectionID: recoveryToken{1, 2, 3},
		resumeSecret: recoveryToken{4, 5, 6},
		lastRecvSeq:  99,
		negotiated: negotiatedRecoveryOptions{
			AckEvery:          17,
			AckDelay:          25 * time.Millisecond,
			HeartbeatInterval: 40 * time.Millisecond,
			HeartbeatTimeout:  120 * time.Millisecond,
		},
	}

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- writeAttachResponse(serverConn, want)
	}()

	got, err := readAttachResponse(clientConn)
	if err != nil {
		t.Fatalf("readAttachResponse: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("writeAttachResponse: %v", err)
	}

	if got.status != want.status {
		t.Fatalf("status got %d want %d", got.status, want.status)
	}
	if got.connectionID != want.connectionID {
		t.Fatalf("connectionID got %x want %x", got.connectionID, want.connectionID)
	}
	if got.resumeSecret != want.resumeSecret {
		t.Fatalf("resumeSecret got %x want %x", got.resumeSecret, want.resumeSecret)
	}
	if got.lastRecvSeq != want.lastRecvSeq {
		t.Fatalf("lastRecvSeq got %d want %d", got.lastRecvSeq, want.lastRecvSeq)
	}
	if got.negotiated != want.negotiated {
		t.Fatalf("negotiated got %+v want %+v", got.negotiated, want.negotiated)
	}
}

func TestReadAttachResponseRejectsInvalidNegotiatedOptions(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	writeErr := make(chan error, 1)
	go func() {
		buf := make([]byte, attachResponseLen)
		buf[0] = attachStatusOK
		binary.BigEndian.PutUint64(buf[36:44], 7)
		binary.BigEndian.PutUint32(buf[44:48], 0)
		binary.BigEndian.PutUint32(buf[48:52], 10)
		binary.BigEndian.PutUint32(buf[52:56], 20)
		binary.BigEndian.PutUint32(buf[56:60], 40)
		writeErr <- writeFull(serverConn, buf)
	}()

	_, err := readAttachResponse(clientConn)
	if err == nil {
		t.Fatal("readAttachResponse succeeded, want error")
	}
	var invalid ErrInvalidMessage
	if !errors.As(err, &invalid) {
		t.Fatalf("readAttachResponse error = %T, want ErrInvalidMessage", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write raw attach response: %v", err)
	}
}

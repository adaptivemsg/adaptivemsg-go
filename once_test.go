package adaptivemsg

import (
	"testing"
	"time"
)

func startOnceTestServer(t *testing.T) (string, func()) {
	t.Helper()
	server := NewServer()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			peer := ""
			if conn.RemoteAddr() != nil {
				peer = conn.RemoteAddr().String()
			}
			go server.handleConn(conn, Netconn{peerAddr: peer})
		}
	}()
	stop := func() {
		_ = listener.Close()
		<-stopped
	}
	return listener.Addr().String(), stop
}

func TestOnceSendRecvAs(t *testing.T) {
	addr, stop := startOnceTestServer(t)
	defer stop()

	reply, err := SendRecvAs[*connTestEchoReply](
		Once("tcp://"+addr),
		&connTestEchoRequest{Text: "hello"},
	)
	if err != nil {
		t.Fatalf("SendRecvAs with Once: %v", err)
	}
	if reply.Text != "hello" {
		t.Fatalf("reply got %q want %q", reply.Text, "hello")
	}
}

func TestOnceSendRecvAsWithOptions(t *testing.T) {
	addr, stop := startOnceTestServer(t)
	defer stop()

	reply, err := SendRecvAs[*connTestEchoReply](
		Once("tcp://"+addr).
			WithTimeout(5*time.Second).
			WithCodecs(CodecMsgpackCompact),
		&connTestEchoRequest{Text: "options"},
	)
	if err != nil {
		t.Fatalf("SendRecvAs with Once options: %v", err)
	}
	if reply.Text != "options" {
		t.Fatalf("reply got %q want %q", reply.Text, "options")
	}
}

func TestOnceSendRecvAsConnectFailure(t *testing.T) {
	_, err := SendRecvAs[*connTestEchoReply](
		Once("tcp://127.0.0.1:1"),
		&connTestEchoRequest{Text: "fail"},
	)
	if err == nil {
		t.Fatalf("expected error for unreachable address")
	}
}

func TestOnceSendRecvAsNilLink(t *testing.T) {
	_, err := SendRecvAs[*connTestEchoReply](nil, &connTestEchoRequest{Text: "nil"})
	if err == nil {
		t.Fatalf("expected error for nil link")
	}
}

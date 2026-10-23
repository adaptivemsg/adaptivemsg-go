package adaptivemsg

import (
	"testing"
	"time"
)

func startClientSendRecvTestServer(t *testing.T) (string, func()) {
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

func TestClientSendRecv(t *testing.T) {
	addr, stop := startClientSendRecvTestServer(t)
	defer stop()

	reply, err := NewClient().WithTimeout(5 * time.Second).SendRecv[*connTestEchoReply](
		"tcp://"+addr,
		&connTestEchoRequest{Text: "hello"},
	)
	if err != nil {
		t.Fatalf("Client.SendRecv: %v", err)
	}
	if reply.Text != "hello" {
		t.Fatalf("reply got %q want %q", reply.Text, "hello")
	}
}

func TestClientSendRecvWithOptions(t *testing.T) {
	addr, stop := startClientSendRecvTestServer(t)
	defer stop()

	reply, err := NewClient().
		WithTimeout(5 * time.Second).
		WithCodecs(CodecMsgpackCompact).
		SendRecv[*connTestEchoReply](
		"tcp://"+addr,
		&connTestEchoRequest{Text: "options"},
	)
	if err != nil {
		t.Fatalf("Client.SendRecv with options: %v", err)
	}
	if reply.Text != "options" {
		t.Fatalf("reply got %q want %q", reply.Text, "options")
	}
}

func TestClientSendRecvConnectFailure(t *testing.T) {
	_, err := NewClient().WithTimeout(time.Second).SendRecv[*connTestEchoReply](
		"tcp://127.0.0.1:1",
		&connTestEchoRequest{Text: "fail"},
	)
	if err == nil {
		t.Fatalf("expected error for unreachable address")
	}
}

func TestClientSendRecvNil(t *testing.T) {
	var client *Client
	_, err := client.SendRecv[*connTestEchoReply](
		"tcp://127.0.0.1:1",
		&connTestEchoRequest{Text: "nil"},
	)
	if err == nil {
		t.Fatalf("expected error for nil Client")
	}
}

package adaptivemsg

import (
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func serveClientTestListener(server *Server, listener net.Listener) func() {
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
	return func() {
		_ = listener.Close()
		<-stopped
	}
}

func startClientTestServer(t *testing.T, server *Server) (string, func()) {
	t.Helper()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	return "tcp://" + listener.Addr().String(), serveClientTestListener(server, listener)
}

// startClientTestPeer accepts connections and hands each to serve instead of
// running the adaptivemsg protocol.
func startClientTestPeer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var conns []net.Conn
		defer func() {
			for _, conn := range conns {
				_ = conn.Close()
			}
		}()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns = append(conns, conn)
			serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return "tcp://" + listener.Addr().String()
}

func TestClientConnectOnce(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()

	reply, err := NewClient().
		WithRecvTimeout(5 * time.Second).
		ConnectOnce(addr).
		SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("ConnectOnce SendRecv: %v", err)
	}
	if reply.Text != "hello" {
		t.Fatalf("reply got %q want %q", reply.Text, "hello")
	}
}

func TestClientConnectOnceWithOptions(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()

	reply, err := NewClient().
		WithConnectTimeout(5 * time.Second).
		WithRecvTimeout(5 * time.Second).
		WithCodecs(CodecMsgpackCompact).
		ConnectOnce(addr).
		SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "options"})
	if err != nil {
		t.Fatalf("ConnectOnce SendRecv with options: %v", err)
	}
	if reply.Text != "options" {
		t.Fatalf("reply got %q want %q", reply.Text, "options")
	}
}

func TestClientConnectOnceIsSingleUse(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()

	once := NewClient().WithRecvTimeout(5 * time.Second).ConnectOnce(addr)
	if _, err := once.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "first"}); err != nil {
		t.Fatalf("first SendRecv: %v", err)
	}
	if _, err := once.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "second"}); !errors.As(err, &ErrClosed{}) {
		t.Fatalf("second SendRecv got %v, want ErrClosed", err)
	}
}

func TestClientConnectOnceConnectFailure(t *testing.T) {
	_, err := NewClient().
		WithRecvTimeout(time.Second).
		ConnectOnce("tcp://127.0.0.1:1").
		SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "fail"})
	if err == nil {
		t.Fatalf("expected error for unreachable address")
	}
}

func TestClientConnectOnceNil(t *testing.T) {
	var client *Client
	_, err := client.ConnectOnce("tcp://127.0.0.1:1").
		SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "nil"})
	if err == nil {
		t.Fatalf("expected error for nil Client")
	}
}

func TestClientConnectOnceRecvTimeout(t *testing.T) {
	// The peer completes the handshake but never replies.
	addr := startClientTestPeer(t, func(conn net.Conn) {
		_, _ = handshakeServer(conn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, defaultMaxFrame, false)
	})

	start := time.Now()
	_, err := NewClient().
		WithRecvTimeout(200 * time.Millisecond).
		ConnectOnce(addr).
		SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "slow"})
	if !errors.As(err, &ErrRecvTimeout{}) {
		t.Fatalf("SendRecv got %v, want ErrRecvTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SendRecv returned after %v", elapsed)
	}
}

func TestClientRecvTimeoutAppliesToStreams(t *testing.T) {
	addr, stop := startClientTestServer(t, NewServer())
	defer stop()

	conn, err := NewClient().WithRecvTimeout(200 * time.Millisecond).Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Recv[*OkReply](); !errors.As(err, &ErrRecvTimeout{}) {
		t.Fatalf("default stream Recv got %v, want ErrRecvTimeout", err)
	}
	stream := conn.NewStream()
	defer stream.Close()
	if got := stream.DebugState().RecvTimeout; got != 200*time.Millisecond {
		t.Fatalf("new stream recv timeout got %v, want 200ms", got)
	}
	stream.SetRecvTimeout(time.Second)
	if got := stream.DebugState().RecvTimeout; got != time.Second {
		t.Fatalf("recv timeout after SetRecvTimeout got %v, want 1s", got)
	}
}

func TestClientConnectTimeoutWaitsForServer(t *testing.T) {
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	hostPort := listener.Addr().String()
	_ = listener.Close()

	result := make(chan error, 1)
	go func() {
		conn, err := NewClient().WithConnectTimeout(5 * time.Second).Connect("tcp://" + hostPort)
		if err == nil {
			conn.Close()
		}
		result <- err
	}()

	time.Sleep(300 * time.Millisecond)
	listener, err = listenTCP(hostPort)
	if err != nil {
		t.Fatalf("listenTCP %s: %v", hostPort, err)
	}
	stop := serveClientTestListener(NewServer(), listener)
	defer stop()

	if err := <-result; err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func TestClientConnectRetriesUntilTimeout(t *testing.T) {
	start := time.Now()
	_, err := NewClient().WithConnectTimeout(500 * time.Millisecond).Connect("tcp://127.0.0.1:1")
	elapsed := time.Since(start)
	if !errors.As(err, &ErrConnectTimeout{}) {
		t.Fatalf("Connect got %v, want ErrConnectTimeout", err)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("Connect got %v, want it to wrap the last dial error", err)
	}
	if elapsed < 400*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("Connect returned after %v, want about 500ms", elapsed)
	}
}

func TestClientConnectWithoutTimeoutFailsFast(t *testing.T) {
	start := time.Now()
	_, err := NewClient().Connect("tcp://127.0.0.1:1")
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("Connect got %v, want connection refused", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Connect returned after %v", elapsed)
	}
}

func TestClientConnectDoesNotRetryHandshakeErrors(t *testing.T) {
	addr := startClientTestPeer(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 400"))
	})

	start := time.Now()
	_, err := NewClient().WithConnectTimeout(5 * time.Second).Connect(addr)
	if !errors.As(err, &ErrBadHandshakeMagic{}) {
		t.Fatalf("Connect got %v, want ErrBadHandshakeMagic", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Connect returned after %v", elapsed)
	}
}

func TestClientConnectTimeoutOnSilentPeer(t *testing.T) {
	addr := startClientTestPeer(t, func(net.Conn) {})

	start := time.Now()
	_, err := NewClient().WithConnectTimeout(300 * time.Millisecond).Connect(addr)
	if !errors.As(err, &ErrConnectTimeout{}) {
		t.Fatalf("Connect got %v, want ErrConnectTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Connect returned after %v, want about 300ms", elapsed)
	}
}

func TestClientConnectOnceDoesNotUseRecovery(t *testing.T) {
	versions := make(chan byte, 2)
	server := NewServer().
		WithRecovery(ServerRecoveryOptions{Enable: true}).
		OnNewStream(func(sc *StreamContext) {
			versions <- sc.stream.connection.config.version
		})
	addr, stop := startClientTestServer(t, server)
	defer stop()
	client := NewClient().
		WithRecvTimeout(5 * time.Second).
		WithRecovery(ClientRecoveryOptions{Enable: true})
	serverVersion := func() byte {
		select {
		case version := <-versions:
			return version
		case <-time.After(2 * time.Second):
			t.Fatal("server opened no stream")
			return 0
		}
	}

	conn, err := client.Connect(addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "conn"}); err != nil {
		t.Fatalf("SendRecv: %v", err)
	}
	conn.Close()
	if version := serverVersion(); version != protocolVersionV3 {
		t.Fatalf("Connect negotiated v%d, want v3", version)
	}

	once := client.ConnectOnce(addr)
	if _, err := once.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "once"}); err != nil {
		t.Fatalf("ConnectOnce SendRecv: %v", err)
	}
	if version := serverVersion(); version != protocolVersionV2 {
		t.Fatalf("ConnectOnce negotiated v%d, want v2", version)
	}
}

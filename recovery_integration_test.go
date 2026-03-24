package adaptivemsg

import (
	"sync/atomic"
	"testing"
	"time"
)

var _ = MustRegisterGlobalType[connTestEchoRequest]()
var _ = MustRegisterGlobalType[connTestEchoReply]()
var _ = MustRegisterGlobalType[connTestErrRequest]()
var _ = MustRegisterGlobalType[recoveryDropReplyRequest]()

type recoveryDropReplyRequest struct {
	Text string `am:"text"`
}

func (*recoveryDropReplyRequest) WireName() string {
	return "am.test.RecoveryDropReplyRequest"
}

var recoveryDropReplyOnce atomic.Bool

func (r *recoveryDropReplyRequest) Handle(ctx *StreamContext) (Message, error) {
	if recoveryDropReplyOnce.CompareAndSwap(false, true) {
		_ = ctx.stream.core.connection.currentTransportForTest().Close()
	}
	return &connTestEchoReply{Text: r.Text}, nil
}

func recoveryTestClientOptions() ClientRecoveryOptions {
	return ClientRecoveryOptions{
		Enable:              true,
		ReconnectMinBackoff: 5 * time.Millisecond,
		ReconnectMaxBackoff: 20 * time.Millisecond,
		MaxReplayBytes:      1 << 20,
	}
}

func recoveryTestServerOptions() ServerRecoveryOptions {
	return ServerRecoveryOptions{
		Enable:            true,
		DetachedTTL:       2 * time.Second,
		MaxReplayBytes:    1 << 20,
		AckEvery:          1,
		AckDelay:          time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond,
		HeartbeatTimeout:  80 * time.Millisecond,
	}
}

func startRecoveryTestServer(t *testing.T, server *Server) (string, func()) {
	t.Helper()

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

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

func startRecoveryHeartbeatBlackholeServer(t *testing.T, opts ServerRecoveryOptions) (string, <-chan struct{}, func()) {
	t.Helper()
	opts = opts.normalized()

	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}

	resumed := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		codecs := []CodecID{CodecMsgpackCompact, CodecMsgpackMap}

		firstConn, err := listener.Accept()
		if err != nil {
			return
		}
		firstCfg, err := handshakeServer(firstConn, codecs, defaultMaxFrame, true)
		if err != nil {
			_ = firstConn.Close()
			return
		}
		firstReq, err := readAttachRequest(firstConn)
		if err != nil {
			_ = firstConn.Close()
			return
		}
		if firstReq.mode != attachModeNew {
			_ = firstConn.Close()
			return
		}
		connectionID, err := newRecoveryToken()
		if err != nil {
			_ = firstConn.Close()
			return
		}
		resumeSecret, err := newRecoveryToken()
		if err != nil {
			_ = firstConn.Close()
			return
		}
		if err := writeAttachResponse(firstConn, attachResponse{
			status:       attachStatusOK,
			connectionID: connectionID,
			resumeSecret: resumeSecret,
			lastRecvSeq:  0,
			negotiated:   opts.negotiated(),
		}); err != nil {
			_ = firstConn.Close()
			return
		}

		go func() {
			wire := &Connection{config: firstCfg}
			for {
				_, _, _, err := wire.readFrameFrom(firstConn)
				if err != nil {
					_ = firstConn.Close()
					return
				}
			}
		}()

		secondConn, err := listener.Accept()
		if err != nil {
			return
		}
		secondCfg, err := handshakeServer(secondConn, codecs, defaultMaxFrame, true)
		if err != nil {
			_ = secondConn.Close()
			return
		}
		secondReq, err := readAttachRequest(secondConn)
		if err != nil {
			_ = secondConn.Close()
			return
		}
		if secondReq.mode != attachModeResume || secondReq.connectionID != connectionID || secondReq.resumeSecret != resumeSecret {
			_ = writeAttachResponse(secondConn, attachResponse{status: attachStatusRejected})
			_ = secondConn.Close()
			return
		}
		if err := writeAttachResponse(secondConn, attachResponse{
			status:       attachStatusOK,
			connectionID: connectionID,
			resumeSecret: resumeSecret,
			lastRecvSeq:  0,
			negotiated:   opts.negotiated(),
		}); err != nil {
			_ = secondConn.Close()
			return
		}
		close(resumed)

		wire := &Connection{config: secondCfg}
		for {
			streamID, _, payload, err := wire.readFrameFrom(secondConn)
			if err != nil {
				_ = secondConn.Close()
				return
			}
			if streamID != controlStreamID {
				continue
			}
			controlType, _, err := parseControlPayload(payload)
			if err != nil {
				_ = secondConn.Close()
				return
			}
			if controlType == controlTypePing {
				if err := wire.writeFrameTo(secondConn, controlStreamID, 0, buildPongControlPayload()); err != nil {
					_ = secondConn.Close()
					return
				}
			}
		}
	}()

	stop := func() {
		_ = listener.Close()
		<-stopped
	}

	return listener.Addr().String(), resumed, stop
}

func TestRecoverySendRecvAfterReconnect(t *testing.T) {
	clientOpts := recoveryTestClientOptions()
	serverOpts := recoveryTestServerOptions()
	server := NewServer().WithRecovery(serverOpts)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	client := NewClient().WithTimeout(500 * time.Millisecond).WithRecovery(clientOpts)
	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	reply, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "first"})
	if err != nil {
		t.Fatalf("first SendRecvAs: %v", err)
	}
	if reply.Text != "first" {
		t.Fatalf("first reply got %q want %q", reply.Text, "first")
	}

	transport := conn.currentTransportForTest()
	if transport == nil {
		t.Fatal("expected active transport")
	}
	_ = transport.Close()

	reply, err = SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "second"})
	if err != nil {
		t.Fatalf("second SendRecvAs: %v", err)
	}
	if reply.Text != "second" {
		t.Fatalf("second reply got %q want %q", reply.Text, "second")
	}
}

func TestRecoveryReplaysReplyAfterTransportBreak(t *testing.T) {
	recoveryDropReplyOnce.Store(false)

	clientOpts := recoveryTestClientOptions()
	serverOpts := recoveryTestServerOptions()
	server := NewServer().WithRecovery(serverOpts)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	client := NewClient().WithTimeout(500 * time.Millisecond).WithRecovery(clientOpts)
	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	reply, err := SendRecvAs[*connTestEchoReply](conn, &recoveryDropReplyRequest{Text: "replayed"})
	if err != nil {
		t.Fatalf("SendRecvAs with dropped reply: %v", err)
	}
	if reply.Text != "replayed" {
		t.Fatalf("reply got %q want %q", reply.Text, "replayed")
	}

	reply, err = SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("follow-up SendRecvAs: %v", err)
	}
	if reply.Text != "after" {
		t.Fatalf("follow-up reply got %q want %q", reply.Text, "after")
	}
}

func TestRecoveryQueuesSendWhileDetached(t *testing.T) {
	clientOpts := recoveryTestClientOptions()
	serverOpts := recoveryTestServerOptions()
	server := NewServer().WithRecovery(serverOpts)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	client := NewClient().WithTimeout(500 * time.Millisecond).WithRecovery(clientOpts)
	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	transport := conn.currentTransportForTest()
	if transport == nil {
		t.Fatal("expected active transport")
	}
	_ = transport.Close()

	reply, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "queued"})
	if err != nil {
		t.Fatalf("queued SendRecvAs: %v", err)
	}
	if reply.Text != "queued" {
		t.Fatalf("reply got %q want %q", reply.Text, "queued")
	}
}

func TestRecoveryHeartbeatKeepsIdleConnectionAlive(t *testing.T) {
	clientOpts := recoveryTestClientOptions()
	serverOpts := recoveryTestServerOptions()
	server := NewServer().WithRecovery(serverOpts)
	addr, stop := startRecoveryTestServer(t, server)
	defer stop()

	client := NewClient().WithTimeout(500 * time.Millisecond).WithRecovery(clientOpts)
	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	transport := conn.currentTransportForTest()
	if transport == nil {
		t.Fatal("expected active transport")
	}

	time.Sleep(5 * serverOpts.HeartbeatInterval)

	if got := conn.currentTransportForTest(); got != transport {
		t.Fatal("expected idle heartbeat to keep the same transport alive")
	}

	reply, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "idle-ok"})
	if err != nil {
		t.Fatalf("SendRecvAs after idle: %v", err)
	}
	if reply.Text != "idle-ok" {
		t.Fatalf("reply got %q want %q", reply.Text, "idle-ok")
	}
}

func TestRecoveryHeartbeatDetectsIdleBlackholeAndReconnects(t *testing.T) {
	clientOpts := recoveryTestClientOptions()
	serverOpts := recoveryTestServerOptions()
	addr, resumed, stop := startRecoveryHeartbeatBlackholeServer(t, serverOpts)
	defer stop()

	client := NewClient().WithTimeout(500 * time.Millisecond).WithRecovery(clientOpts)
	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	transport := conn.currentTransportForTest()
	if transport == nil {
		t.Fatal("expected active transport")
	}

	select {
	case <-resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("resume not observed")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		current := conn.currentTransportForTest()
		return current != nil && current != transport
	})
}

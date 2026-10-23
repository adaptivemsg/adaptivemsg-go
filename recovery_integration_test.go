package adaptivemsg

import (
	"encoding/binary"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

var _ = MustRegister[connTestEchoRequest]()
var _ = MustRegister[connTestEchoReply]()
var _ = MustRegister[connTestErrRequest]()
var _ = MustRegister[recoveryDropReplyRequest]()

type recoveryDropReplyRequest struct {
	Text string `am:"text"`
}

func (*recoveryDropReplyRequest) WireName() string {
	return "am.test.RecoveryDropReplyRequest"
}

var recoveryDropReplyOnce atomic.Bool

func (r *recoveryDropReplyRequest) Handle(ctx *StreamContext) (Message, error) {
	if recoveryDropReplyOnce.CompareAndSwap(false, true) {
		_ = ctx.stream.connection.currentTransportForTest().Close()
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
				_, _, _, _, err := wire.readFrameFrom(firstConn)
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
			streamID, _, _, payload, err := wire.readFrameFrom(secondConn)
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
				continue
			}
		}
	}()

	stop := func() {
		_ = listener.Close()
		<-stopped
	}

	return listener.Addr().String(), resumed, stop
}

func startRecoveryRawFrameServer(t *testing.T, maxFrame uint32, raw []byte) string {
	t.Helper()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := handshakeServer(conn, []CodecID{CodecMsgpackCompact, CodecMsgpackMap}, maxFrame, true); err != nil {
			return
		}
		if _, err := readAttachRequest(conn); err != nil {
			return
		}
		connectionID, err := newRecoveryToken()
		if err != nil {
			return
		}
		resumeSecret, err := newRecoveryToken()
		if err != nil {
			return
		}
		if err := writeAttachResponse(conn, attachResponse{
			status:       attachStatusOK,
			connectionID: connectionID,
			resumeSecret: resumeSecret,
			negotiated:   recoveryTestServerOptions().negotiated(),
		}); err != nil {
			return
		}
		if err := writeFull(conn, raw); err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
	}()
	return listener.Addr().String()
}

func TestRecoveryMalformedFrameClosesConnection(t *testing.T) {
	const maxFrame = 64
	cases := []struct {
		name       string
		version    byte
		flags      FrameFlags
		payloadLen uint32
	}{
		{"unsupported flags", protocolVersionV3, FrameFlagMeta, 1},
		{"wrong version", protocolVersionV2, 0, 1},
		{"oversized payload", protocolVersionV3, 0, maxFrame + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := make([]byte, frameHeaderLenV3)
			header[0] = tc.version
			header[1] = byte(tc.flags)
			binary.BigEndian.PutUint32(header[2:6], 1)
			binary.BigEndian.PutUint32(header[6:10], tc.payloadLen)
			binary.BigEndian.PutUint64(header[10:18], 1)
			addr := startRecoveryRawFrameServer(t, maxFrame, header)

			conn, err := NewClient().WithRecovery(recoveryTestClientOptions()).Connect("tcp://" + addr)
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer conn.Close()

			waitForCondition(t, 2*time.Second, func() bool {
				return conn.DebugState().Closed
			})
			if got := conn.DebugState().LastFailureCode; got != DebugFailureRecoveryData {
				t.Fatalf("last failure got %q want %q", got, DebugFailureRecoveryData)
			}
		})
	}
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

	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "first"})
	if err != nil {
		t.Fatalf("first SendRecv: %v", err)
	}
	if reply.Text != "first" {
		t.Fatalf("first reply got %q want %q", reply.Text, "first")
	}

	transport := conn.currentTransportForTest()
	if transport == nil {
		t.Fatal("expected active transport")
	}
	_ = transport.Close()

	reply, err = conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "second"})
	if err != nil {
		t.Fatalf("second SendRecv: %v", err)
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

	reply, err := conn.SendRecv[*connTestEchoReply](&recoveryDropReplyRequest{Text: "replayed"})
	if err != nil {
		t.Fatalf("SendRecv with dropped reply: %v", err)
	}
	if reply.Text != "replayed" {
		t.Fatalf("reply got %q want %q", reply.Text, "replayed")
	}

	reply, err = conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "after"})
	if err != nil {
		t.Fatalf("follow-up SendRecv: %v", err)
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

	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "queued"})
	if err != nil {
		t.Fatalf("queued SendRecv: %v", err)
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

	reply, err := conn.SendRecv[*connTestEchoReply](&connTestEchoRequest{Text: "idle-ok"})
	if err != nil {
		t.Fatalf("SendRecv after idle: %v", err)
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

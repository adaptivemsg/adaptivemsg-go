package adaptivemsg

import (
	"fmt"
	"testing"
	"time"
)

var _ = MustRegisterGlobalType[connTestEchoRequest]()
var _ = MustRegisterGlobalType[connTestEchoReply]()

func startBenchmarkServer(b *testing.B, server *Server) (string, func()) {
	b.Helper()
	listener, err := listenTCP("127.0.0.1:0")
	if err != nil {
		b.Fatalf("listenTCP: %v", err)
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

func benchmarkProtocolSendRecv(b *testing.B, recoveryEnabled bool) {
	b.Helper()

	server := NewServer()
	client := NewClient().WithTimeout(2 * time.Second)
	if recoveryEnabled {
		server = server.WithRecovery(ServerRecoveryOptions{
			Enable:            true,
			DetachedTTL:       5 * time.Second,
			MaxReplayBytes:    8 << 20,
			AckEvery:          64,
			AckDelay:          20 * time.Millisecond,
			HeartbeatInterval: 30 * time.Second,
			HeartbeatTimeout:  90 * time.Second,
		})
		client = client.WithRecovery(ClientRecoveryOptions{
			Enable:              true,
			ReconnectMinBackoff: 100 * time.Millisecond,
			ReconnectMaxBackoff: 2 * time.Second,
			MaxReplayBytes:      8 << 20,
		})
	}

	addr, stop := startBenchmarkServer(b, server)
	defer stop()

	conn, err := client.Connect("tcp://" + addr)
	if err != nil {
		b.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	conn.SetRecvTimeout(2 * time.Second)

	// Warm up connection state before timing.
	if _, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "warmup"}); err != nil {
		b.Fatalf("warmup SendRecvAs: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reply, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "x"})
		if err != nil {
			b.Fatalf("SendRecvAs: %v", err)
		}
		if reply.Text != "x" {
			b.Fatalf("reply got %q want %q", reply.Text, "x")
		}
	}
}

func BenchmarkProtocolV2SendRecv(b *testing.B) {
	benchmarkProtocolSendRecv(b, false)
}

func BenchmarkProtocolV3RecoverySendRecv(b *testing.B) {
	benchmarkProtocolSendRecv(b, true)
}

func TestProtocolV2V3PerformanceReport(t *testing.T) {
	v2 := testing.Benchmark(BenchmarkProtocolV2SendRecv)
	v3 := testing.Benchmark(BenchmarkProtocolV3RecoverySendRecv)

	v2Ns := float64(v2.NsPerOp())
	v3Ns := float64(v3.NsPerOp())
	v2B := float64(v2.AllocedBytesPerOp())
	v3B := float64(v3.AllocedBytesPerOp())
	v2A := float64(v2.AllocsPerOp())
	v3A := float64(v3.AllocsPerOp())

	pct := func(base, current float64) float64 {
		if base == 0 {
			return 0
		}
		return (current - base) / base * 100
	}

	t.Logf("v2 baseline: %s", fmt.Sprintf("%s", v2))
	t.Logf("v3 recovery: %s", fmt.Sprintf("%s", v3))
	t.Logf("delta ns/op: %+0.2f%%", pct(v2Ns, v3Ns))
	t.Logf("delta B/op: %+0.2f%%", pct(v2B, v3B))
	t.Logf("delta allocs/op: %+0.2f%%", pct(v2A, v3A))

	t.Logf("v2 ns/op: %.0f, B/op: %.0f, allocs/op: %.0f", v2Ns, v2B, v2A)
	t.Logf("v3 ns/op: %.0f, B/op: %.0f, allocs/op: %.0f", v3Ns, v3B, v3A)
}

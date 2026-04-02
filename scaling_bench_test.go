package adaptivemsg

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkScaling tests server throughput with multiple connections and streams.
// Run with: go test -bench=BenchmarkScaling -benchmem -run=^$ -v
//
// Environment variables:
//   AM_CONNS=N       Number of concurrent connections (default: 4)
//   AM_STREAMS=M     Number of streams per connection (default: 4)

func benchmarkScaling(b *testing.B, conns, streamsPerConn int, recoveryEnabled bool) {
	b.Helper()

	server := NewServer()
	client := NewClient().WithTimeout(5 * time.Second)
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

	// Create all connections and streams upfront
	type streamInfo struct {
		conn   *Connection
		stream *Stream[Message]
	}
	allStreams := make([]streamInfo, 0, conns*streamsPerConn)

	for i := 0; i < conns; i++ {
		conn, err := client.Connect("tcp://" + addr)
		if err != nil {
			b.Fatalf("Connect %d: %v", i, err)
		}
		defer conn.Close()
		conn.SetRecvTimeout(5 * time.Second)

		// Warm up the connection
		if _, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "warmup"}); err != nil {
			b.Fatalf("warmup conn %d: %v", i, err)
		}

		// Create streams (stream 0 is the default, create additional ones)
		for j := 0; j < streamsPerConn; j++ {
			var stream *Stream[Message]
			if j == 0 {
				stream = conn.defaultStream()
			} else {
				stream = conn.NewStream()
			}
			stream.SetRecvTimeout(5 * time.Second)
			allStreams = append(allStreams, streamInfo{conn: conn, stream: stream})
		}
	}

	totalStreams := len(allStreams)
	opsPerStream := b.N / totalStreams
	if opsPerStream < 1 {
		opsPerStream = 1
	}

	b.ReportAllocs()
	b.ResetTimer()

	var wg sync.WaitGroup
	var errors atomic.Int64

	for idx, si := range allStreams {
		wg.Add(1)
		go func(idx int, stream *Stream[Message]) {
			defer wg.Done()
			for i := 0; i < opsPerStream; i++ {
				reply, err := stream.SendRecv(&connTestEchoRequest{Text: "x"})
				if err != nil {
					errors.Add(1)
					return
				}
				echoReply, ok := reply.(*connTestEchoReply)
				if !ok || echoReply.Text != "x" {
					errors.Add(1)
					return
				}
			}
		}(idx, si.stream)
	}
	wg.Wait()

	b.StopTimer()

	if errs := errors.Load(); errs > 0 {
		b.Fatalf("benchmark stream errors: %d", errs)
	}
}

func BenchmarkScalingV2_1Conn1Stream(b *testing.B) {
	benchmarkScaling(b, 1, 1, false)
}

func BenchmarkScalingV2_1Conn4Stream(b *testing.B) {
	benchmarkScaling(b, 1, 4, false)
}

func BenchmarkScalingV2_1Conn16Stream(b *testing.B) {
	benchmarkScaling(b, 1, 16, false)
}

func BenchmarkScalingV2_1Conn64Stream(b *testing.B) {
	benchmarkScaling(b, 1, 64, false)
}

func BenchmarkScalingV2_4Conn1Stream(b *testing.B) {
	benchmarkScaling(b, 4, 1, false)
}

func BenchmarkScalingV2_4Conn4Stream(b *testing.B) {
	benchmarkScaling(b, 4, 4, false)
}

func BenchmarkScalingV2_4Conn16Stream(b *testing.B) {
	benchmarkScaling(b, 4, 16, false)
}

func BenchmarkScalingV2_4Conn64Stream(b *testing.B) {
	benchmarkScaling(b, 4, 64, false)
}

func BenchmarkScalingV3_1Conn1Stream(b *testing.B) {
	benchmarkScaling(b, 1, 1, true)
}

func BenchmarkScalingV3_4Conn1Stream(b *testing.B) {
	benchmarkScaling(b, 4, 1, true)
}

func BenchmarkScalingV3_1Conn4Stream(b *testing.B) {
	benchmarkScaling(b, 1, 4, true)
}

func BenchmarkScalingV3_1Conn16Stream(b *testing.B) {
	benchmarkScaling(b, 1, 16, true)
}

func BenchmarkScalingV3_1Conn64Stream(b *testing.B) {
	benchmarkScaling(b, 1, 64, true)
}

func BenchmarkScalingV3_4Conn4Stream(b *testing.B) {
	benchmarkScaling(b, 4, 4, true)
}

func BenchmarkScalingV3_4Conn16Stream(b *testing.B) {
	benchmarkScaling(b, 4, 16, true)
}

func BenchmarkScalingV3_4Conn64Stream(b *testing.B) {
	benchmarkScaling(b, 4, 64, true)
}

// TestScalingThroughput runs scaling tests and prints throughput summary.
// Uses wall-clock timing with the same methodology as the Rust scaling bench
// so that the numbers are directly comparable.
// Run with: go test -run=TestScalingThroughput -v -timeout=600s
func TestScalingThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scaling test in short mode")
	}

	configs := []struct {
		conns   int
		streams int
	}{
		{1, 1},
		{1, 4},
		{1, 16},
		{1, 64},
		{4, 1},
		{4, 4},
		{4, 16},
		{4, 64},
	}

	iterations := 5000
	runs := 5

	fmt.Printf("\nGo Scaling Throughput (%d runs x %d ops, median)\n", runs, iterations)
	fmt.Printf("%-25s %15s %15s\n", "Config", "ops/sec", "ns total (median)")
	fmt.Printf("%s\n", "--------------------------------------------------------------")

	fmt.Println("\nV2 (no recovery):")
	for _, cfg := range configs {
		name := fmt.Sprintf("  %dconn x %dstream", cfg.conns, cfg.streams)
		var results []float64
		for run := 0; run < runs; run++ {
			ns := runScalingTest(t, cfg.conns, cfg.streams, iterations, false)
			results = append(results, ns)
		}
		med := median(results)
		opsPerSec := float64(iterations) / (med / 1e9)
		fmt.Printf("%-25s %15.0f %15.0f\n", name, opsPerSec, med)
	}

	fmt.Println("\nV3 (recovery enabled):")
	for _, cfg := range configs {
		name := fmt.Sprintf("  %dconn x %dstream", cfg.conns, cfg.streams)
		var results []float64
		for run := 0; run < runs; run++ {
			ns := runScalingTest(t, cfg.conns, cfg.streams, iterations, true)
			results = append(results, ns)
		}
		med := median(results)
		opsPerSec := float64(iterations) / (med / 1e9)
		fmt.Printf("%-25s %15.0f %15.0f\n", name, opsPerSec, med)
	}
}

func runScalingTest(t *testing.T, conns, streamsPerConn, iterations int, recoveryEnabled bool) float64 {
	t.Helper()

	server := NewServer()
	client := NewClient().WithTimeout(5 * time.Second)
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
	defer func() {
		_ = listener.Close()
		<-stopped
	}()

	addr := listener.Addr().String()

	// Create all connections and streams
	type streamInfo struct {
		conn   *Connection
		stream *Stream[Message]
	}
	allStreams := make([]streamInfo, 0, conns*streamsPerConn)
	allConns := make([]*Connection, 0, conns)

	for i := 0; i < conns; i++ {
		conn, err := client.Connect("tcp://" + addr)
		if err != nil {
			t.Fatalf("Connect %d: %v", i, err)
		}
		allConns = append(allConns, conn)
		conn.SetRecvTimeout(5 * time.Second)

		// Warm up
		if _, err := SendRecvAs[*connTestEchoReply](conn, &connTestEchoRequest{Text: "warmup"}); err != nil {
			t.Fatalf("warmup conn %d: %v", i, err)
		}

		for j := 0; j < streamsPerConn; j++ {
			var stream *Stream[Message]
			if j == 0 {
				stream = conn.defaultStream()
			} else {
				stream = conn.NewStream()
			}
			stream.SetRecvTimeout(5 * time.Second)
			allStreams = append(allStreams, streamInfo{conn: conn, stream: stream})
		}
	}

	defer func() {
		for _, conn := range allConns {
			conn.Close()
		}
	}()

	totalStreams := len(allStreams)
	opsPerStream := iterations / totalStreams
	if opsPerStream < 1 {
		opsPerStream = 1
	}

	var wg sync.WaitGroup
	var errors atomic.Int64

	start := time.Now()

	for _, si := range allStreams {
		wg.Add(1)
		go func(stream *Stream[Message]) {
			defer wg.Done()
			for i := 0; i < opsPerStream; i++ {
				reply, err := stream.SendRecv(&connTestEchoRequest{Text: "x"})
				if err != nil {
					errors.Add(1)
					return
				}
				echoReply, ok := reply.(*connTestEchoReply)
				if !ok || echoReply.Text != "x" {
					errors.Add(1)
					return
				}
			}
		}(si.stream)
	}
	wg.Wait()

	elapsed := time.Since(start)

	if errs := errors.Load(); errs > 0 {
		t.Fatalf("errors during test: %d", errs)
	}

	return float64(elapsed.Nanoseconds())
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted[len(sorted)/2]
}

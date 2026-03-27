# adaptivemsg-go

Go runtime for the adaptivemsg wire protocol.

This repository is the Go sibling of `adaptivemsg-rust` and is intended to stay
in lockstep with the protocol defined in `adaptivemsg-doc`.

## Code generation (amgen-go)

Use `//go:generate go run <module>/cmd/amgen-go` in your `message.go`. `amgen-go` reads
`GOFILE` from `go generate` and writes a sibling `.rs` file with the same base
name. Exported fields must include explicit `am:"..."` tags.

## Client/server example

```go
package main

import (
	"fmt"
	"log"

	am "adaptivemsg"
)

type HelloRequest struct {
	Who string `am:"who"`
}

type HelloInternal struct {
	TraceID string `am:"trace_id"`
}

type HelloReply struct {
	Answer   string        `am:"answer"`
	Internal HelloInternal `am:"internal"`
}

func (msg *HelloRequest) Handle(_ *am.StreamContext) (am.Message, error) {
	return &HelloReply{
		Answer: fmt.Sprintf("hi, %s", msg.Who),
		Internal: HelloInternal{
			TraceID: "req-1",
		},
	}, nil
}

var _ = am.MustRegisterGlobalType[HelloRequest]()

func main() {
	// server
	go func() {
		server := am.NewServer().WithRecovery(am.ServerRecoveryOptions{Enable: true})
		if err := server.Serve("tcp://0.0.0.0:5555"); err != nil {
			log.Fatal(err)
		}
	}()

	// client
	client := am.NewClient().WithRecovery(am.ClientRecoveryOptions{Enable: true})
	conn, _ := client.Connect("tcp://127.0.0.1:5555")
	reply, _ := am.SendRecvAs[*HelloReply](conn, &HelloRequest{Who: "alice"})
	log.Printf("reply: %s (trace %s)", reply.Answer, reply.Internal.TraceID)
}
```

## Dynamic receive

```go
package main

import (
	"fmt"
	"log"

	am "adaptivemsg"
	"adaptivemsg/examples/echo"
)

var _ = am.MustRegisterGlobalType[echo.MessageReply]()
var _ = am.MustRegisterGlobalType[echo.WhoElseEvent]()

func main() {
	conn, _ := am.NewClient().WithRecovery(am.ClientRecoveryOptions{Enable: true}).Connect("tcp://127.0.0.1:5560")
	stream := conn.NewStream()

	for {
		msg, err := stream.Recv()
		if err != nil {
			log.Fatal(err)
		}
		switch m := msg.(type) {
		case *echo.MessageReply:
			log.Printf("reply: %s", m.Msg)
		case *echo.WhoElseEvent:
			log.Printf("event: %s", m.Addr)
		default:
			log.Fatal(fmt.Errorf("unexpected %T", msg))
		}
	}
}
```

## API reference (surface)

Functions:
- `SendRecvAs`, `StreamAs`, `WireNameOf`, `ContextAs`
- `RegisterGlobalType`, `MustRegisterGlobalType`

Client:
- `NewClient`
- `Client.WithTimeout`, `Client.WithCodecs`, `Client.WithMaxFrame`, `Client.WithRecovery`, `Client.Connect`

Server:
- `NewServer`
- `Server.WithRecovery`, `Server.Serve`, `Server.OnConnect`, `Server.OnDisconnect`, `Server.OnNewStream`, `Server.OnCloseStream`
- `Netconn.PeerAddr`

Connection (default stream view):
- `Connection.NewStream`, `Connection.Close`, `Connection.WaitClosed`
- `Connection.Send`, `Connection.SendRecv`, `Connection.Recv`, `Connection.PeekWire`, `Connection.SetRecvTimeout`

Stream:
- `Stream[T].Send`, `Stream[T].SendRecv`, `Stream[T].Recv`, `Stream[T].PeekWire`, `Stream[T].SetRecvTimeout`, `Stream[T].ID`, `Stream[T].Close`

Context:
- `StreamContext.SetContext`, `StreamContext.GetContext`, `StreamContext.NewTask`

Codec & messages:
- `CodecID`, `CodecMsgpackMap`, `CodecMsgpackCompact`, `CodecID.String`, `CodecImpl`
- `RegisterCodec`, `MustRegisterCodec`
- `Message`, `NamedMessage`, `OkReply`, `ErrorReply`, `NewErrorReply`

Recovery:
- `ClientRecoveryOptions`, `ServerRecoveryOptions`

## Error reasoning

Local input/usage errors:
- `ErrInvalidMessage`: nil or non-struct messages, invalid wire names, compact field issues.
- `ErrUnknownMessage`: wire name not registered in the registry.

Protocol/compat errors:
- `ErrUnsupportedCodec`, `ErrUnsupportedFrameVersion`, `ErrNoCommonCodec`, `ErrTooManyCodecs`,
  `ErrBadHandshakeMagic`, `ErrFrameTooLarge`, `ErrUnsupportedTransport`, `ErrResumeRejected`.

Runtime errors:
- `ErrClosed`, `ErrRecvTimeout`, `ErrConcurrentRecv`, `ErrHandlerTaskBusy`, `ErrConnectTimeout`, `ErrReplayBufferFull`.

Remote errors:
- `ErrorReply` is sent by the peer; `SendRecv` surfaces it as `ErrRemote{Code, Message}`.
- `protocol_error` = wire mismatch or invalid ordering; `codec_error` = decode/envelope failure;
  `handler_error` = handler returned an error.

Notes:
- Addresses accept `tcp://HOST:PORT`, `uds://@name` (linux abstract), or `uds:///tmp/name.sock`.
- Abstract UDS demo: `go run -tags server ./examples/hello/cmd/server -addr uds://@adaptivemsg-hello` and `go run ./examples/hello/cmd/client -addr uds://@adaptivemsg-hello` (echo uses `@adaptivemsg-echo`).
- Codecs are negotiated from the client's `WithCodecs` preference list; the server selects the first common codec. Defaults are compact-first.
- Custom codecs implement `CodecImpl` and register with `RegisterCodec`; msgpack struct tags only apply to the msgpack built-ins. `CodecImpl.Encode` transfers ownership of the returned payload to the caller, so codecs must not mutate or reuse that backing storage after return.
- Compact codec uses positional arrays; nested structs are encoded as arrays when eligible, but types with custom msgpack/binary/text encoders or unexported fields fall back to msgpack's normal encoding (typically maps), so struct tags may still apply there.
- Connections act as the default stream; use `am.SendRecvAs[Reply](conn, msg)` for one-off calls or `am.StreamAs[Reply](stream)` for a typed view (needed for `Recv`).
- Register handler/message types with `MustRegisterGlobalType` before `NewClient()`/`NewServer()` so the snapshot sees them.
- Use `PeekWire()` on a stream (or `conn.PeekWire()`) to inspect the next message type before decoding; it honors the same recv timeout and concurrency rules as `Recv`.
- Message names default to `am.<package-leaf>.<TypeName>`; implement `WireName() string` on a type if you need an override.
- Example servers rely on build-tagged handlers; run them with `-tags server` (for example: `go run -tags server ./examples/hello/cmd/server`).
- `Stream.Close()` is local-only; there is no on-wire stream close frame.

## Recovery

- Recovery is opt-in via `Client.WithRecovery(...)` and `Server.WithRecovery(...)`.
- When both sides enable recovery, the connection negotiates protocol `v3`; otherwise the client falls back to legacy `v2`.
- The implemented recovery scope is transport-only failure while both client and server processes remain alive.
- In recovery mode, `Connection` is the logical connection and the underlying `net.Conn` may be replaced transparently after reconnect.
- Recovery is client-driven: the dialing side reconnects and the server reattaches the new transport to the existing logical connection.
- The server is authoritative for shared recovery wire behavior. ACK batching and heartbeat settings are chosen by the server and sent to the client during attach/resume.
- `Recv()` can continue waiting across reconnect, and queued/unacknowledged outbound frames are replayed after resume.
- `WaitClosed()` and `ErrClosed` refer to permanent logical closure, not a transient reconnectable transport loss.
- Server `OnConnect` runs for the initial logical connection, not for every resumed transport; `OnDisconnect` runs on permanent logical close.
- Recovery does not cover client/server process restart, node reboot, or durable replay after process death.
- `ClientRecoveryOptions` controls:
  - `Enable`: turn recovery on.
  - `ReconnectMinBackoff` / `ReconnectMaxBackoff`: client reconnect backoff range.
  - `MaxReplayBytes`: client-side byte cap for retained unacknowledged outbound frames.
- `ServerRecoveryOptions` controls:
  - `Enable`: turn recovery on.
  - `DetachedTTL`: how long the server keeps a detached logical connection alive.
  - `MaxReplayBytes`: server-side byte cap for retained unacknowledged outbound frames.
  - `AckEvery` / `AckDelay`: server-selected cumulative ACK batching policy.
  - `HeartbeatInterval` / `HeartbeatTimeout`: server-selected idle failure detection policy for quiet connections.

For detailed recovery protocol behavior, heartbeat/liveness semantics, and
cross-runtime interoperability notes, see `DEVELOP.md`.

## Debugging and Observability

The runtime exposes scoped debugging snapshots (per connection and per stream).
This gives you counters and failure context without relying on global process
metrics.

### What is available

- `Connection.DebugState()` returns:
  - negotiated protocol/codec/max frame
  - active stream count and per-stream snapshots
  - per-connection counters (frames, bytes, messages, handler activity, recovery activity)
  - last failure code, reason, and timestamp
- `Stream[T].DebugState()` returns:
  - stream queue depths and recv timeout
  - per-stream counters
  - stream-level last failure code, reason, and timestamp
- Recovery-enabled connections also include `RecoveryDebugState` in the connection snapshot.

### Typical usage pattern

Use the snapshot where you handle transport/runtime errors so logs include both
the immediate error and current runtime state:

```go
conn, err := client.Connect("tcp://127.0.0.1:5555")
if err != nil {
	log.Printf("connect failed: %v", err)
	return
}

reply, err := am.SendRecvAs[*HelloReply](conn, &HelloRequest{Who: "alice"})
if err != nil {
	dbg := conn.DebugState()
	// log.Printf("sendrecv failed: %+v", dbg)
	log.Printf("sendrecv failed: err=%v code=%s reason=%s streams=%d sent=%d recv=%d",
		err,
		dbg.LastFailureCode,
		dbg.LastFailure,
		dbg.StreamCount,
		dbg.Counters.DataMessagesSent,
		dbg.Counters.DataMessagesReceived,
	)
	return
}
_ = reply
```

For a single stream:

```go
stream := conn.NewStream()
_, err := am.StreamAs[*HelloReply](stream).Recv()
if err != nil {
	sdbg := stream.DebugState()
	log.Printf("stream recv failed: err=%v stream=%d code=%s reason=%s inbox=%d incoming=%d",
		err,
		sdbg.ID,
		sdbg.LastFailureCode,
		sdbg.LastFailure,
		sdbg.InboxDepth,
		sdbg.IncomingDepth,
	)
}
```

### Failure codes

Failure codes are stable strings intended for machine filtering/alerting while
`LastFailure` remains human-readable context.

Common codes include:

- Stream path: `stream.recv_timeout`, `stream.encode`, `stream.enqueue`, `stream.decode`, `stream.protocol`, `stream.protocol_reply_send`
- Connection path: `connection.reader`, `connection.writer`, `connection.reader_enqueue`, `handler.error`
- Recovery path: `recovery.resume`, `recovery.reconnect_terminal`, `recovery.read`, `recovery.control`, `recovery.data`, `recovery.ack_write`, `recovery.resume_write`, `recovery.live_write`, `recovery.ping_write`

### Troubleshooting quick map

| Failure code | Likely cause | First checks |
|---|---|---|
| `stream.recv_timeout` | No message arrived before stream recv timeout | Check `SetRecvTimeout` value; verify peer is producing responses/events; inspect `InboxDepth` and `IncomingDepth` |
| `stream.encode` | Local message cannot be encoded by negotiated codec | Validate message shape/tags; confirm codec supports payload type |
| `stream.enqueue` | Connection is closing/closed or replay enqueue rejected | Check `ConnectionDebugState.Closed`; inspect replay limits and recent close reason |
| `stream.decode` | Received payload cannot be decoded into expected type | Compare wire type versus expected type; verify registry/type registration order |
| `stream.protocol` | Stream-level protocol violation detected | Inspect `LastFailure` detail and peer message ordering/type behavior |
| `stream.protocol_reply_send` | Failed to send protocol `ErrorReply` after violation | Check transport health and whether connection was already closing |
| `connection.reader` | Base frame read failed | Check network/transport health, frame compatibility, and max-frame settings |
| `connection.writer` | Base frame write failed | Check peer reachability and connection lifecycle (`Closed`, detach/reconnect state) |
| `connection.reader_enqueue` | Read frame could not be queued into stream pipeline | Check stream close timing and backpressure symptoms |
| `handler.error` | Handler returned an application error | Inspect handler logs, input validation, and downstream dependencies |
| `recovery.resume` | Resume attempt failed but may retry | Check server reachability, attach credentials, and reconnect backoff progression |
| `recovery.reconnect_terminal` | Resume failed with terminal condition | Check reject reason (`LastFailure`), codec/version mismatch, connection existence |
| `recovery.read` | Recovery transport read failed | Check heartbeat timeout behavior and transport blackhole symptoms |
| `recovery.control` | Invalid recovery control frame payload/type | Verify non-Go peer control frame format and control type handling |
| `recovery.data` | Recovery data frame sequencing/validation failed | Verify monotonic `seq` handling and replay logic |
| `recovery.ack_write` / `recovery.ping_write` | Control frame write failed during recovery | Check transport stability during idle/control periods |
| `recovery.resume_write` / `recovery.live_write` | Replay/live data write failed during recovery writer loop | Check transport churn and reconnect cadence |

Recommended logging fields:

- `last_failure_code`
- `last_failure_reason`
- `last_failure_at`
- relevant scoped counters (for example `frames_read`, `frames_written`, `data_messages_sent`, `data_messages_received`)

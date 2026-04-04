# DEVELOP

Contributor-oriented design notes for adaptivemsg-go.

This file is intentionally split into two layers:

- Practical layer: how the system behaves and where to change it.
- Reference layer: exact wire bytes, protocol constraints, and recovery details.

For public API usage, see `README.md`.

## How to Read This Document

- Start with **Quick Mental Model** for architecture and runtime flow.
- Use **Recovery At A Glance** to understand current v3 semantics.
- Use **Wire Reference** only when implementing/validating protocol compatibility.
- Use **Testing Plan** and **Code Pointers** when making changes.

## Quick Mental Model

### Core Concepts

- Message: any Go value used on the wire. `NamedMessage` overrides wire name.
- Wire name: `WireName()` or default `am.<package-leaf>.<TypeName>`.
- Codec: pluggable envelope+payload codec negotiated per connection.
- Connection: multiplexed streams over one transport; stream `0` is default.
- Stream: FIFO per stream; `Recv` is single-consumer.
- StreamContext: per-stream state for handlers, user context, and task gate.
- Registry: wire-name -> type + optional handler map, snapshotted at `NewClient()` / `NewServer()`.

### Runtime Flow

Client:
1. `NewClient()` snapshots registry.
2. `Connect()` negotiates handshake and starts loops.

Server:
1. `NewServer()` snapshots registry.
2. `Serve()` accepts sockets, negotiates handshake, starts loops per connection.

Reader loop:
- Reads frames and routes payloads into per-stream incoming queues.

Decode loop (per stream):
- Parses envelope to get wire name and payload.
- If a handler is registered: decode and enqueue handler job.
- Otherwise: enqueue raw message into stream inbox.

Handler loop (per stream, if handlers exist):
- Calls handler and sends reply or `OkReply`.
- On handler error: sends `ErrorReply` (`handler_error`).

Recv path:
- `Recv[T]` pulls raw inbox message, validates wire, decodes on demand.
- `Recv` on `Stream[Message]` uses registry dynamic decode.
- Unknown wire in dynamic recv returns `ErrUnknownMessage` (no protocol error emitted).
- `PeekWire()` peeks next wire without decoding.

Send path:
- `Send` encodes with negotiated codec.
- `SendRecv` is send + wait on the same stream.

## Payload Encoding

Map codec (MessagePack, built-in):
- Envelope: `{type: "<wire>", data: <msgpack object>}`
- `data` uses msgpack struct-tag encoding.

Compact codec (MessagePack, built-in):
- Envelope: `["<wire>", field1, field2, ...]`
- Field order is Go struct field order.
- All fields must be exported; decode enforces exact field count.

## Dispatch And Lazy Decode

- Stream inbox stores raw envelopes.
- Handler dispatch is wire-name first.
- If handler exists: decode in handler path.
- Otherwise: decode later in consumer goroutine (`Recv`).

`Recv[T]` for concrete types does strict wire matching.
- On mismatch it sends `ErrorReply` (`protocol_error`) and closes stream.

## Errors And Protocol Signaling

Local errors:
- `ErrInvalidMessage`
- `ErrUnknownMessage`
- `ErrUnsupportedCodec`
- `ErrUnsupportedFrameVersion`
- `ErrNoCommonCodec`
- `ErrTooManyCodecs`
- `ErrRecvTimeout`
- `ErrConcurrentRecv`
- `ErrClosed`

Remote errors:
- `ErrorReply` sent on protocol or handler failures.
- In `SendRecv`, remote error maps to `ErrRemote{Code, Message}`.

Protocol error codes:
- `protocol_error`: wire mismatch, invalid ordering
- `codec_error`: decode/envelope failure
- `handler_error`: handler returned error

## Concurrency And Lifetimes

- Exactly one in-flight `Recv`/`SendRecv` per stream.
- `SetRecvTimeout(0)` disables timeout.
- `Stream.Close()` is local only (no on-wire close frame).
- Stream context is per stream (`SetContext` / `ContextAs`).

## Current Design Gaps

- Registry snapshot is fixed at client/server construction time.
- No explicit on-wire stream close.
- `maxFrame == 0` rejects all non-empty frames.
- Recovery includes heartbeat knobs, but no OS keepalive integration.

## Recovery At A Glance

### Scope

Implemented scope is transport-only breakage while both processes stay alive.

In scope:
- TCP/UDS disconnect and reconnect
- logical connection continuity
- replay of unacknowledged frames

Out of scope:
- client process crash/restart
- server process crash/restart
- durable replay after process death

### Public Model

- Public handle remains `*Connection` from `Client.Connect()`.
- `Connection` is logical and long-lived.
- attached `net.Conn` is replaceable transport.
- stream IDs are stream identity, not connection identity.

### Identity And Ownership

- Internal stable identity: `connection_id` (opaque).
- Secret for resume authorization: `resume_secret`.
- Server issues identity+secret; client does not choose them.
- Client owns reconnect attempts.
- Server validates resume and reattaches to existing logical connection.
- Exactly one active transport per logical connection.

### Negotiation Model

- Recovery is opt-in via protocol version.
- `v2`: legacy behavior.
- `v3`: recovery/replay enabled.
- If peer cannot do `v3`, connection continues with `v2`.

### Heartbeat And Liveness (Current)

Current implementation is Ping-only with read-deadline liveness.

Writer side:
- Sends periodic `PING` at negotiated heartbeat interval when idle.

Reader side:
- Arms read deadline to negotiated heartbeat timeout:
  - before each blocking read
  - after each successful read

Timeout behavior:
- If no inbound frame arrives before deadline, transport is treated as dead.
- Dead transport triggers detach + reconnect flow.

Any valid inbound frame refreshes liveness:
- data frame
- `ACK`
- `PING`

`PONG` is intentionally not part of current protocol/runtime behavior.

### Replay Semantics

Minimum replay design in use:
- one sequence space per direction per connection
- cumulative ACK
- replay retention of unacknowledged outbound data frames
- reconnect + resume based on `connection_id`/`resume_secret`

Send acceptance semantics:
- `Send()` means local acceptance into logical outbound path, not peer receipt.

### Detach Lifecycle

On transport failure:
- mark detached
- keep streams, contexts, and replay state alive
- wake client reconnect loop

On server side while detached:
- logical connection stored in detached table
- per-connection TTL timer controls expiry

On successful resume:
- new transport replaces old transport
- replay resumes from peer last received sequence

On TTL expiry:
- logical connection is permanently closed
- resources and replay retention are freed

## Wire Reference (v2 and v3)

All integer fields are big-endian unless explicitly stated.

### Shared Handshake Envelope

Client -> server request header (12 bytes):

| Offset | Size | Field |
|---|---:|---|
| 0..1 | 2 | magic = `"AM"` |
| 2 | 1 | version (`2` or `3`) |
| 3 | 1 | codec_count |
| 4 | 1 | flags (currently `0`) |
| 5..7 | 3 | reserved (`0`) |
| 8..11 | 4 | max_frame (`u32`) |

Immediately after header:
- codec list length = `codec_count`
- one-byte `CodecID` entries

Server -> client response header (12 bytes):

| Offset | Size | Field |
|---|---:|---|
| 0..1 | 2 | magic = `"AM"` |
| 2 | 1 | accept (`1` accepted, `0` rejected) |
| 3 | 1 | version |
| 4 | 1 | selected_codec |
| 5 | 1 | flags (currently `0`) |
| 6..7 | 2 | reserved (`0`) |
| 8..11 | 4 | negotiated_max_frame (`u32`) |

### v2 Frame Format

v2 frame header is 10 bytes:

| Offset | Size | Field |
|---|---:|---|
| 0 | 1 | version (`2`) |
| 1 | 1 | flags (currently unused) |
| 2..5 | 4 | stream_id (`u32`) |
| 6..9 | 4 | payload_len (`u32`) |

Payload bytes immediately follow the header.

### v3 Frame Format

v3 frame header is 18 bytes:

| Offset | Size | Field |
|---|---:|---|
| 0 | 1 | version (`3`) |
| 1 | 1 | flags (currently unused) |
| 2..5 | 4 | stream_id (`u32`) |
| 6..9 | 4 | payload_len (`u32`) |
| 10..17 | 8 | seq (`u64`) |

Payload bytes immediately follow the header.

### v3 Attach/Resume Format

Attach request (44 bytes):

| Offset | Size | Field |
|---|---:|---|
| 0 | 1 | mode (`1` new, `2` resume) |
| 1..3 | 3 | reserved (`0`) |
| 4..19 | 16 | connection_id |
| 20..35 | 16 | resume_secret |
| 36..43 | 8 | last_recv_seq (`u64`) |

Attach response (60 bytes):

| Offset | Size | Field |
|---|---:|---|
| 0 | 1 | status (`1` ok, `2` rejected) |
| 1..3 | 3 | reserved (`0`) |
| 4..19 | 16 | connection_id |
| 20..35 | 16 | resume_secret |
| 36..43 | 8 | last_recv_seq (`u64`) |
| 44..47 | 4 | ack_every (`u32`) |
| 48..51 | 4 | ack_delay_ms (`u32`) |
| 52..55 | 4 | heartbeat_interval_ms (`u32`) |
| 56..59 | 4 | heartbeat_timeout_ms (`u32`) |

### v3 Control Stream Format

Reserved stream id:
- `control_stream_id = 0xFFFFFFFF` (`^uint32(0)`)

Control payloads:
- ACK (`type=1`, total 9 bytes)
  - byte `0`: `1`
  - bytes `1..8`: `last_recv_seq` (`u64`)
- PING (`type=2`, total 1 byte)
  - byte `0`: `2`

Control frames are not replayed and do not consume sequence numbers.

### Validation Constraints

- handshake version must be supported (`2` or `3`)
- `codec_count` must be in `[1, 16]`
- frame `payload_len` must be <= negotiated `max_frame`
- negotiated recovery values must satisfy:
  - `ack_every > 0`
  - `ack_delay_ms > 0`
  - `heartbeat_interval_ms > 0`
  - `heartbeat_timeout_ms >= 2 * heartbeat_interval_ms`
- unknown control type is invalid

### Interop Checklist (Non-Go Peer)

- follow exact byte layouts (including reserved bytes)
- encode/decode all integers big-endian
- implement v3 `seq` at bytes `10..17` of frame header
- encode/decode ACK as exact 9-byte payload
- encode/decode PING as exact 1-byte payload
- treat heartbeat timeout as transport failure and trigger resume path
- do not rely on PONG semantics

## Detailed Recovery Implementation Shape

### Internal State Summary

Recovery configuration is role-specific:

- `ClientRecoveryOptions`
  - `Enable bool`
  - `ReconnectMinBackoff time.Duration`
  - `ReconnectMaxBackoff time.Duration`
  - `MaxReplayBytes int64`
- `ServerRecoveryOptions`
  - `Enable bool`
  - `DetachedTTL time.Duration`
  - `MaxReplayBytes int64`
  - `AckDelay time.Duration`
  - `AckEvery uint32`
  - `HeartbeatInterval time.Duration`
  - `HeartbeatTimeout time.Duration`

Connection transport state includes:
- current attached `net.Conn`
- `sendNotify chan struct{}`
- `transportCond *sync.Cond`
- `transportGen uint64`
- `transportResume uint64`
- `recovery *recoveryState`

Replay-related state:
- `nextSendSeq uint64`
- replay retention keyed by sequence
- live send deque for new frames
- resume deque populated on reconnect

`recoveryState` includes:
- `connectionID [16]byte`
- `resumeSecret [16]byte`
- negotiated timing/batching (`AckDelay`, `AckEvery`, heartbeat values)
- `lastRecvSeq uint64`
- `lastAckSent uint64`
- `ackPending uint32`
- `ackDue bool`
- `liveQueue`, `resumeQueue`
- atomic mirrors for heartbeat/read-deadline hot path
- detached expiry timer
- client reconnect guards/parameters

Server detached table:
- `map[[16]byte]*Connection`

### Attach/Resume Rules

Client attach request carries:
- mode (`new`/`resume`)
- `connection_id` (zero for `new`)
- `resume_secret` (zero for `new`)
- client last received server sequence

Server attach response carries:
- status (`ok`/`rejected`)
- assigned or confirmed identity + secret
- server last received client sequence
- authoritative shared recovery parameters

Rules:
- first connect: client sends `new`; server issues identity+secret
- reconnect: client sends `resume`; server validates secret + detached entry
- rejection: logical connection permanently closes

### Delivery, ACK, And Replay Rules

Outbound data send:
- allocate next connection sequence
- encode frame
- retain frame in replay storage
- enqueue frame for writer

Inbound data receive:
- if `seq == lastRecvSeq + 1`: accept and deliver
- if `seq <= lastRecvSeq`: drop duplicate
- if `seq > lastRecvSeq + 1`: protocol error

ACK policy:
- cumulative ACK, not per-message ACK
- send ACK after `AckDelay` or every `AckEvery` accepted data frames

Replay on resume:
- each side reports last fully received sequence
- sender replays retained frames with `seq > peerLastRecvSeq`
- replayed frames preserve original encoded bytes and sequence

### Runtime API Semantics

- `Recv()` can continue waiting across reconnect while logical connection survives.
- `Send()` still means local acceptance only.
- `SendRecv()` continuity comes from frame replay (not API-level resend tricks).
- `WaitClosed()` and `ErrClosed` refer to permanent logical closure.

## Protocol v2 vs v3 Performance Snapshot

Measured with Go 1.22.10 on linux/amd64:

```bash
go test -run=^$ -bench='BenchmarkProtocolV(2SendRecv|3RecoverySendRecv)$' -benchmem .
```

Latest measured result:
- v2: `126042 ns/op`, `2306 B/op`, `64 allocs/op`
- v3: `141751 ns/op`, `2437 B/op`, `66 allocs/op`

Delta vs v2 baseline:
- latency: `+12.46%`
- memory: `+5.68%`
- allocations: `+3.12%`

Interpretation:
- v3 has measurable steady-state overhead from replay/ack bookkeeping.
- v3 provides transport-break resilience unavailable in v2.

## Implementation Phases (Roadmap)

Phase 1: logical connection vs transport split
- files: `connection.go`, `client.go`, `server.go`
- goal: detach/reattach transport while preserving logical connection

Phase 2: recovery config and negotiation
- files: `client.go`, `server.go`, `protocol.go`
- goal: role-specific options and v3 negotiation

Phase 3: attach/resume protocol
- files: `protocol.go`, `server.go`, recovery attach helpers
- goal: identity issuance, resume validation, TTL cleanup

Phase 4: replay buffer and ACK control
- files: `frame.go`, `connection.go`, `stream.go`, replay helpers
- goal: sequence, cumulative ACK, replay storage, duplicate handling

Phase 5: optional follow-ups
- files: `connection.go`, `client.go`
- goal: transport tuning and optional OS keepalive integration

Phase 6: docs and compatibility notes
- files: `README.md`, `DEVELOP.md`
- goal: keep semantics and interop docs current

## Testing Plan

### Unit Tests

- Handshake negotiation:
  - v3/v3
  - v3 client with v2-only server
  - v2 client with v3-capable server
- Attach/resume encoding/validation:
  - assign identity+secret
  - accept valid resume
  - reject unknown connection
  - reject wrong secret
- Replay bookkeeping:
  - monotonic sequence
  - cumulative ACK truncation
  - byte accounting
  - duplicate handling
  - sequence-gap rejection
- Detached lifetime:
  - TTL expiry cleanup

### Integration Tests

Recovery disabled:
- behavior unchanged on disconnect

Recovery enabled:
- reconnect resumes same logical connection
- `Recv()` survives reconnect
- queued sends during detach are delivered after resume
- reply replay works when reply was lost after request processing
- duplicate replay is not redelivered
- detached TTL expiry causes permanent close
- stale transport replaced on successful resume

### Fault Injection

- disconnect after partial frame header write
- disconnect after full header but partial payload
- disconnect after request received but before reply received
- disconnect during one-way stream traffic with delayed ACK flush
- resume after multiple reconnect backoff retries

### Compatibility And Performance

- compare recovery off vs on:
  - throughput small/medium payloads
  - replay buffer memory growth
- compatibility matrix:
  - new client vs legacy server
  - legacy client vs new server

## First Implementation Slice

Recommended minimal slice:
1. logical connection vs transport split
2. recovery negotiation
3. attach/resume protocol
4. replay buffer + cumulative ACK

Reason:
- this is the smallest slice that delivers transport-break continuity for in-flight operations.

## Code Pointers

- `connection.go`: handshake, frame IO, stream lifecycle, core loops
- `protocol.go`: handshake and version negotiation
- `frame.go`: frame header read/write
- `stream.go`: send/recv, timeout, inbox, protocol errors
- `codec.go`: codec interfaces and envelope contracts
- `codec_registry.go`: codec registration/lookup
- `codec_msgpack.go`: map/compact MessagePack codecs
- `registry.go`: wire registry and handler registration
- `message.go`: wire name derivation and built-in message types
- `context.go`: per-stream context and handler task gating

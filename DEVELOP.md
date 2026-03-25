# DEVELOP

Design notes, code flow, and protocol details for adaptivemsg-go. This is a
working document for contributors and for future AI skills references; it is
not a full API reference (see `README.md` for usage).

## Core Concepts

- Message: any Go value used on the wire. `NamedMessage` overrides the wire name.
- Wire name: `WireName()` or default `am.<package-leaf>.<TypeName>`.
- Codec: pluggable envelope + payload codec negotiated per connection (see `RegisterCodec`).
- Connection: multiplexed streams over a single transport; default stream is ID 0.
- Stream: FIFO per stream. `Recv` is single-consumer (guarded).
- StreamContext: per-stream state used by handlers; holds user context and task gate.
- Registry: map of wire name -> message type + optional handler. Snapshotted at
  `NewClient()`/`NewServer()`.

## Wire Protocol

Handshake (client -> server, v2 header + codec list):
- magic "AM" (2 bytes)
- version (1 byte)
- codec count (1 byte, max 16)
- flags (1 byte)
- reserved (3 bytes)
- max frame size (4 bytes)
- codec list (codec count bytes)

Handshake (server -> client, 12 bytes):
- magic "AM" (2 bytes)
- accept (1 byte), version (1 byte)
- selected codec (1 byte)
- flags (1 byte), reserved (2 bytes)
- negotiated max frame size (4 bytes)

Frame header (10 bytes):
- version (1 byte)
- flags (1 byte, currently unused)
- stream ID (4 bytes, big-endian)
- payload length (4 bytes, big-endian)

## Payload Encoding

Map codec (MessagePack, built-in):
- Envelope: `{type: "<wire>", data: <msgpack object>}`
- `data` is msgpack-encoded with struct tags.

Compact codec (MessagePack, built-in):
- Envelope: `["<wire>", field1, field2, ...]`
- Field order is the Go struct field order.
- All fields must be exported; field count must match at decode time.

## Runtime Flow (Go)

Client:
1) `NewClient()` snapshots registry.
2) `Connect()` negotiates handshake and starts reader/writer loops.

Server:
1) `NewServer()` snapshots registry.
2) `Serve()` accepts sockets, negotiates handshake, starts loops per connection.

Reader loop:
- Reads frames, routes payloads into per-stream incoming queues.

Decode loop (per stream):
- Parses the envelope to get the wire name and raw payload.
- If a handler is registered for the wire, decode and enqueue handler job.
- Otherwise, enqueue the raw message into the stream inbox.

Handler loop (per stream, if handlers exist):
- Calls the handler, sends reply or `OkReply`.
- On handler error: sends `ErrorReply` (`handler_error`).

Recv path:
- `Recv[T]` pulls from the raw inbox, verifies wire name, decodes on demand.
- `Recv` on `Stream[Message]` uses the registry to decode to a concrete type;
  unregistered wires return `ErrUnknownMessage` without emitting a protocol error.
- `PeekWire()` peeks the next wire without decoding (respects timeout/recv guard).

Send path:
- `Send` encodes to the negotiated codec.
- `SendRecv` sends then waits on the same stream.

## Dispatch & Lazy Decode

The inbox stores raw envelopes so decoding can happen in the consumer goroutine.
Handlers are checked by wire name first; if a handler exists, decoding happens
in the handler path; otherwise the raw envelope is queued for `Recv`.

`Recv[T]` for concrete types does strict wire matching; on mismatch it sends an
`ErrorReply` with `protocol_error` and closes the stream.

## Errors & Protocol Signaling

Local errors:
- `ErrInvalidMessage`: invalid local inputs (nil, not a struct, empty wire, etc).
- `ErrUnknownMessage`: wire name not registered in the registry.
- `ErrUnsupportedCodec` / `ErrUnsupportedFrameVersion`: unsupported peer config.
- `ErrNoCommonCodec`: no codec overlap during handshake.
- `ErrTooManyCodecs`: peer sent too many codecs in the handshake.
- `ErrRecvTimeout` / `ErrConcurrentRecv` / `ErrClosed`: runtime conditions.

Remote errors:
- `ErrorReply` is sent on protocol or handler failures.
- In `SendRecv`, `ErrorReply` is surfaced as `ErrRemote{Code, Message}`.

Protocol error codes:
- `protocol_error`: wire mismatch or invalid message ordering.
- `codec_error`: decode failure or envelope violation.
- `handler_error`: handler returned an error.

## Concurrency & Lifetimes

- Only one `Recv`/`SendRecv` in-flight per stream (guarded).
- `SetRecvTimeout(0)` disables timeout; otherwise `ErrRecvTimeout`.
- `Stream.Close()` removes the stream locally; there is no on-wire close frame.
- Stream context is per stream; use `SetContext`/`ContextAs` to store typed state.

## Design Notes / Gaps

- Registry is snapshotted at client/server creation; later registration changes
  are not visible to existing connections.
- No explicit on-wire stream close; peers learn of closure by protocol or connection
  termination.
- `maxFrame == 0` currently rejects all non-empty frames; keep this in mind if
  exposing custom max-frame values.

## Auto Reconnect / Auto Recover

This section records the current recovery design and implementation status.
Recovery is implemented for the transport-only scope described below. Process
crash/restart recovery and durable replay after process death are still out of
scope.

### Scope

- The intended recovery scope is transport-only breakage: TCP/UDS disconnects
  while both client and server processes remain alive.
- Client crash/restart, server crash/restart, node reboot, and durable recovery
  after process death are out of scope for the first design.
- This scope is a good fit for long-lived TCP connections (for example in k8s),
  where transport churn is common enough to justify built-in recovery.
- For UDS the value is lower but not zero; many UDS disconnects are caused by
  peer restart, which is out of scope here.

### Public Model

- Do not introduce a new public `session` concept.
- Keep `Connection` as the public handle returned by `Client.Connect()`.
- In the recovery design, `Connection` becomes the long-lived logical connection.
- The underlying `net.Conn` becomes a replaceable transport attachment for that
  logical connection.
- Streams remain streams within a `Connection`; stream IDs are not reused as
  connection identity.

### Connection Identity

- Recovery needs an internal stable connection identity; call it `connection_id`.
- `connection_id` should be opaque/internal, not user-facing.
- Prefer server-issued identity plus a `resume_secret` rather than client-chosen
  identity.
- `connection_id`/`resume_secret` are only needed during attach or reconnect;
  they do not need to be carried on every data frame.
- The server should keep a table of detached logical connections keyed by
  `connection_id`.

### Reconnect Ownership

- The dialing side re-initiates transport reconnect.
- In the current API shape, that means the client reconnects.
- The server accepts the new transport and reattaches it to the existing logical
  `Connection` using `connection_id` plus `resume_secret`.
- Only one active transport should exist per logical connection at a time; a
  successful reattach replaces the old transport.

### Negotiation

- Recovery/replay should be opt-in, not always on.
- Prefer a dedicated protocol `v3` for recovery-enabled connections rather than
  a `v2` feature flag.
- `v2` remains the current legacy behavior.
- `v3` means reconnect/replay semantics are enabled and the frame format may
  differ from `v2`.
- If either side does not support `v3`, they should continue using `v2`.

### Stale Connection Handling

- If the client dies permanently, the server must not keep detached logical
  connections forever.
- Detached logical connections should have a reconnect TTL/lease.
- The server should expire detached connections after the TTL and free their
  streams, contexts, and replay buffers.
- The current implementation uses a per-connection expiry timer plus replay
  buffer byte limits; there is no global background sweeper yet.

### Recovery Semantics

- A reconnect-only design with no replay is simpler and has near-zero steady
  state overhead, but it cannot make in-flight operations fully transparent.
- If the goal is truly "user code does not need to care that reconnect
  happened", transport reconnect alone is insufficient.
- To hide transport-only disconnects for in-flight operations, the protocol
  needs connection-level replay state.
- The minimal correct replay design discussed so far is:
  - one per-direction sequence space per `Connection`
  - cumulative acknowledgements
  - replay buffers for unacknowledged outbound frames
  - client-driven reconnect and server-side reattach by `connection_id`
- Sequence/ack state should be per connection, not per stream. Multiple streams
  share the same per-direction sequence space.

### Why Replay Is Needed

- If both sides detect a transport failure before a complete frame is accepted,
  the sender can safely resend that frame after reconnect without extra
  protocol state.
- The hard case is when the sender locally succeeds but the receiver gets
  nothing or only a partial frame before the transport dies.
- In that case the sender cannot infer which frame the peer last fully
  received, so transparent recovery needs replay position from the peer.
- A further transport-only case is "request processed, reply lost". If both
  processes stay alive and replay buffers are preserved, replay can resend the
  unacknowledged reply after reconnect.
- Server/client process crash remains out of scope; replay here only covers
  transport breaks while both processes stay alive.

### API Semantics to Preserve/Change

- Basic user code should continue to work with `Client.Connect()` returning
  `*Connection`; users should not manually manage `connection_id`.
- `WaitClosed()` should mean permanent logical connection closure, not temporary
  transport detachment.
- `ErrClosed` should mean permanent close, not transient reconnectable loss.
- `Recv()` should be able to continue waiting across reconnect if the logical
  connection survives.
- `Send()` already means local acceptance into the outbound path, not peer
  receipt. That behavior fits reconnect/replay well.
- `SendRecv()` is only fully transparent across reconnect if replay is present;
  without replay it may still fail at the reconnect boundary.

### Current Gaps Relevant to Recovery

- There is no on-wire stream close today; `Stream.Close()` is local-only. This
  becomes more important once logical connections outlive a single transport.
- Idle failure detection now uses heartbeat control frames plus read deadlines.
- Remaining gap: there is still no transport-specific tuning beyond the recovery
  heartbeat knobs, and no OS keepalive integration.
- Writer correctness matters for replay logic: low-level writes must handle
  short writes correctly.
- `writeFull` has been added for frame and handshake writes so transport IO no
  longer assumes a single `Write()` flushes the entire buffer.

### Detailed Proposal

This is the current implementation shape for recovery-enabled connections.

#### Internal Types / State

- Recovery config is split by role:
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
- The client API does not expose shared wire-behavior knobs. The server chooses
  them authoritatively and sends them during attach/resume.
- Keep the option surface intentionally small; recovery should be configurable
  but not "fancy".
- Current transport state on `Connection`:
  - current attached `net.Conn`
  - `sendNotify chan struct{}`
  - `transportCond *sync.Cond`
  - `transportGen uint64`
  - `transportResume uint64`
  - `recovery *recoveryState`
- Current replay-related state on `Connection`:
  - `nextSendSeq uint64`
  - recovery runtime uses:
    - replay retention keyed by `seq`
    - live send deque for newly accepted outbound frames
    - resume deque built on reconnect from retained replay state
- Current `recoveryState` keeps:
  - `connectionID [16]byte`
  - `resumeSecret [16]byte`
  - negotiated shared recovery parameters:
    - `AckDelay`
    - `AckEvery`
    - `HeartbeatInterval`
    - `HeartbeatTimeout`
  - `lastRecvSeq uint64`
  - `lastAckSent uint64`
  - `ackPending uint32`
  - `ackDue bool`
  - `liveQueue` and `resumeQueue` as dedicated ring deques
  - heartbeat/read-deadline timing mirrored into atomics for the writer hot path
  - detached expiry timer
  - client reconnect parameters and active reconnect guard
- Current replay bookkeeping stores one canonical `frameRecord` per unacked
  outbound frame; replay retention and send queues share pointers to that
  record rather than duplicating `streamID`/`seq`/`payload` metadata.
- Current detached-connection table on `Server`:
  - `map[[16]byte]*Connection`
- detached expiry is enforced with per-connection timers
- Keep sequence/ack state per connection, not per stream.

#### Negotiation / Protocol Shape

- Recovery is opt-in and selected by protocol version.
- `v2` keeps the existing framing and behavior.
- `v3` means recovery/replay mode is enabled.
- `v3` uses an attach/resume exchange immediately after handshake and before
  normal reader/writer loops begin.

#### Attach / Resume Exchange

Client -> server attach request:
- mode: `new` or `resume`
- `connection_id` (16 bytes, zeroed for `new`)
- `resume_secret` (16 bytes, zeroed for `new`)
- `last_recv_seq` from the server->client direction

Server -> client attach response:
- status: `ok` or `rejected`
- assigned/confirmed `connection_id`
- assigned/confirmed `resume_secret`
- `last_recv_seq` from the client->server direction
- server-chosen shared recovery parameters:
  - `ack_every`
  - `ack_delay_ms`
  - `heartbeat_interval_ms`
  - `heartbeat_timeout_ms`

Rules:
- On first connect the client sends `new`; the server allocates
  `connection_id` and `resume_secret`.
- On reconnect the client sends `resume`; the server validates the secret and
  finds the detached logical connection.
- The response tells each side which outbound frames need replay.
- The response also tells the client which shared ACK/heartbeat policy to use
  for that logical connection.
- Resume rejection falls back to permanent close for that logical connection.

#### Frame / Control Proposal

- `v2` keeps the current frame header unchanged.
- `v3` extends the frame header with connection-level `seq uint64`.
- Proposed `v3` frame header:
  - version (1 byte)
  - flags (1 byte)
  - stream ID (4 bytes)
  - payload length (4 bytes)
  - sequence number (8 bytes)
- Data frames carry `seq` in the `v3` header, not in the payload or codec
  envelope.
- Control traffic uses a reserved stream ID that is never exposed to user code.
- The current implementation uses `^uint32(0)` as the reserved control stream ID.
- Initial control messages needed:
  - cumulative `ACK(last_recv_seq uint64)`
- Idle detection control messages:
  - `PING`
  - `PONG`
- ACK remains a separate control frame in `v3`; there is no ACK piggyback field
  in normal data frames.
- Data frames are replayed until acknowledged.
- Control frames are not replayed and do not consume sequence numbers.

#### Delivery / Replay Rules

- On each data send:
  - allocate the next connection-level `seq`
  - build the encoded frame
  - store it in the replay buffer until acked
  - enqueue it to the active transport writer
- On inbound data receive:
  - if `seq == lastRecvSeq+1`, accept and deliver
  - if `seq <= lastRecvSeq`, drop as duplicate replay
  - if `seq > lastRecvSeq+1`, treat as protocol error
- Advance `lastRecvSeq` when the runtime has safely accepted the frame into the
  stream incoming path or handler queue.
- Do not wait for user `Recv()` or handler completion before acknowledging.
- Send cumulative ACK after `AckDelay` or after `AckEvery` newly accepted data
  frames, whichever comes first.
- Do not send ACK immediately on every receive in v1.
- On successful resume, replay outbound data frames with `seq > peerLastRecvSeq`.
- During idle periods, send `PING` at `HeartbeatInterval` and expect inbound
  traffic (data, `ACK`, `PING`, or `PONG`) before `HeartbeatTimeout`.
- A heartbeat read timeout is treated as transport loss and triggers detach and
  reconnect in the same way as a read/write failure.

#### Send / Ack / Replay Flow

- Outbound send path:
  - user code calls `Send()` / `SendRecv()`
  - runtime encodes the message payload
  - runtime allocates the next connection-level `seq`
  - runtime admits the frame into replay retention before send returns
  - replay retention creates the canonical `frameRecord`
  - runtime enqueues that same `frameRecord` into the live send deque
- Replay buffer semantics:
  - the replay buffer stores unacknowledged outbound data frames in sequence
    order as a side-car retention structure
  - replay buffer accounting is byte-based, bounded by `MaxReplayBytes`
  - steady-state sending should not scan the replay buffer on every frame
  - replay retention and live/resume send queues share one frame record; they
    do not duplicate payload or per-frame metadata
  - on reconnect, runtime builds a temporary resume queue from replay entries
    with `seq > peerLastRecvSeq`
- Normal write success:
  - a successful local transport write does not remove the frame from replay
  - the frame remains retained until cumulative ACK confirms peer receipt
- ACK processing:
  - peer sends `ACK(last_recv_seq=N)` on the reserved control stream
  - sender updates cumulative ack state
  - sender drops replay-buffer entries from the oldest forward while
    `entry.seq <= N`
  - dropping an entry also subtracts its byte size from replay accounting
- Replay after reconnect:
  - during resume, each side reports the last sequence number fully received
    from the peer
  - sender snapshots retained replay-buffer entries with
    `seq > peerLastRecvSeq` into a temporary resume queue
  - writer drains that resume queue before sending newly queued live frames
  - resent frames keep the same `seq` and encoded bytes; they are not rebuilt
    as new logical sends
- Payload ownership:
  - `CodecImpl.Encode` transfers ownership of the returned payload to the
    caller
  - codecs must not mutate or reuse the returned backing storage after return
  - this allows replay retention and live send queues to share payload slices
    without defensive copying
- Why this structure is sufficient:
  - if peer fully received a frame, resume/ACK state causes it to be dropped
    and not resent
  - if peer did not fully receive a frame, it remains retained and is replayed
    after reconnect

#### Runtime Behavior

- `Recv()` should continue waiting across reconnect as long as the logical
  connection survives.
- `Send()` should continue to mean local acceptance into the logical outbound
  path, not peer receipt.
- `SendRecv()` should remain a send followed by a receive on the same stream;
  its recovery comes from replayed request/reply frames, not from higher-level
  API resend logic.
- `WaitClosed()` should block until the logical connection is permanently closed.
- `ErrClosed` should only be returned for permanent close, TTL expiry, resume
  rejection, or explicit close.

#### Detach / Reattach Flow

- On transport read/write failure:
  - mark the transport detached instead of permanently closing immediately
  - wake the client reconnect loop
  - keep streams, stream contexts, and replay state alive
- The client reconnect loop:
  - dials the same address with backoff
  - performs normal handshake
  - performs resume attach
  - swaps in the new transport if resume succeeds
- The server on detach:
  - keeps the logical connection in the detached table
  - starts/restarts detached TTL countdown
- The server on resume:
  - validates `connection_id` and `resume_secret`
  - closes/replaces any older still-attached transport
  - reuses the existing logical connection object and stream table
- On detached TTL expiry:
  - permanently close the logical connection
  - free streams, contexts, and replay buffer

#### Implemented API Semantics

- `Client.WithRecovery(ClientRecoveryOptions)` and
  `Server.WithRecovery(ServerRecoveryOptions)` opt into recovery.
- If both sides support recovery, they negotiate `v3`; otherwise the client
  falls back to legacy `v2`.
- `Recv()` continues waiting across reconnect while the logical connection
  survives.
- `Send()` still means local acceptance into the logical outbound path.
- `SendRecv()` survives transport break within the current scope by replaying
  queued/unacknowledged request and reply frames.
- `WaitClosed()` and `ErrClosed` refer to permanent logical closure, not a
  transient transport detach.
- Server `OnConnect` runs only on the initial logical connection, and
  `OnDisconnect` runs only when that logical connection permanently closes.
- The server is authoritative for shared recovery timing and batching values;
  the client learns them from attach/resume and does not configure them locally.

### Coding Plan

#### Phase 1: Logical Connection vs Transport Split

- Files:
  - `connection.go`
  - `client.go`
  - `server.go`
- Goals:
  - separate permanent logical close from transport detach
  - allow a `Connection` to swap attached `net.Conn`
  - preserve stream table across detach
  - keep legacy behavior unchanged when recovery is disabled

#### Phase 2: Recovery Config and Negotiation

- Files:
  - `client.go`
  - `server.go`
  - `protocol.go`
- Goals:
  - add separate client/server recovery options
  - add protocol `v3` support for recovery-enabled connections
  - have the server publish shared ACK/heartbeat parameters during attach/resume
  - keep legacy peers compatible

#### Phase 3: Attach / Resume Protocol

- Files:
  - `protocol.go`
  - `server.go`
  - new helper file for recovery attach/resume encoding
- Goals:
  - server-issued `connection_id` and `resume_secret`
  - detached connection lookup and validation
  - resume rejection path
  - detached TTL cleanup

#### Phase 4: Replay Buffer and Ack Control

- Files:
  - `frame.go`
  - `connection.go`
  - `stream.go`
  - new helper file for replay bookkeeping
- Goals:
  - per-connection sequence numbers
  - cumulative ack control frames
  - replay buffer with byte limit
  - duplicate detection and replay on resume

#### Phase 5: Optional Follow-up Features

- Files:
  - `connection.go`
  - `client.go`
- Goals:
  - optional transport-specific tuning beyond protocol heartbeat
  - optional future OS keepalive integration if needed
- Notes:
  - protocol heartbeat/read-deadline idle detection is now implemented

#### Phase 6: Documentation and Compatibility Notes

- Files:
  - `README.md`
  - `DEVELOP.md`
- Goals:
  - document negotiated recovery mode
  - document scope and non-goals
  - document changed semantics of `WaitClosed`, `ErrClosed`, and reconnect

### Testing Plan

#### Unit Tests

- Handshake negotiation:
  - client/server both support `v3`
  - client supports `v3`, server only supports `v2`
  - server supports `v3`, client only uses `v2`
- Attach/resume encoding:
  - first connect assigns `connection_id` and `resume_secret`
  - resume accepts valid credentials
  - resume rejects unknown `connection_id`
  - resume rejects wrong `resume_secret`
- Replay bookkeeping:
  - seq allocation monotonic per connection
  - ack drops replayed frames up to cumulative seq
  - replay buffer byte accounting
  - duplicate seq dropped
  - unexpected seq gap rejected
- Detached lifetime:
  - detached connection expires after TTL
  - detached cleanup removes replay state and streams

#### Integration Tests

- Recovery disabled:
  - current behavior unchanged on disconnect
- Recovery enabled:
  - first connect gets server-issued connection identity
  - transport break followed by client reconnect resumes the same logical
    connection
  - `Recv()` survives reconnect
  - client `Send()` queued before/during detach arrives after reconnect
  - server-side `stream.Send()` from handler task arrives after reconnect
  - request fully received, reply lost, reply replayed after reconnect
  - duplicate replay frame is not re-delivered
  - detached TTL expiry causes permanent close
  - second transport replaces stale/older transport for same connection

#### Fault Injection Tests

- Disconnect after partial frame header write
- Disconnect after full header but partial payload
- Disconnect after request fully received but before reply fully received
- Disconnect during one-way streaming traffic with delayed ack flush
- Resume after multiple backoff retries

#### Compatibility / Performance Tests

- Recovery off vs on:
  - throughput benchmark with small payloads
  - throughput benchmark with medium payloads
  - memory benchmark for replay buffer growth
- Protocol compatibility:
  - new client vs legacy server
  - legacy client vs new server

### First Implementation Slice

Recommended first coding slice:
- Phase 1: logical connection vs transport split
- Phase 2: recovery negotiation
- Phase 3: attach/resume protocol
- Phase 4: replay buffer and cumulative ack

Rationale:
- This is the smallest slice that can truthfully claim transport-only
  disconnect recovery with in-flight operation continuity.
- Batched ACK keeps overhead lower than immediate per-message ACK.
- Heartbeat/read-deadline idle detection has now been added on top of that
  recovery core.

## Code Pointers

- `connection.go`: handshake, frames, stream lifecycle, reader/writer loops.
- `protocol.go`: handshake format and negotiation.
- `frame.go`: frame header IO.
- `stream.go`: send/recv, timeouts, inbox handling, protocol errors.
- `codec.go`: codec interfaces and envelopes.
- `codec_registry.go`: codec registration and lookup.
- `codec_msgpack.go`: map/compact MessagePack codecs.
- `registry.go`: wire registry and handler registration.
- `message.go`: wire name derivation and built-in message types.
- `context.go`: per-stream context storage and handler task gating.

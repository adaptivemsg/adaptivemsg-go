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

// Package adaptivemsg is a wire protocol runtime for framed, multiplexed,
// codec-negotiated connections. It is wire-compatible with the Rust counterpart,
// enabling seamless Go ↔ Rust interoperability.
//
// # Transports
//
// Two transports are supported:
//
//   - TCP: addressed as "tcp://host:port" (or bare "host:port", which defaults to TCP)
//   - Unix domain sockets: addressed as "uds:///path/to/sock" or "unix:///path/to/sock"
//
// # Codecs
//
// Codecs are pluggable. The package ships with two MessagePack codecs:
//
//   - [CodecMsgpackCompact] — positional (array) encoding, smaller on the wire
//   - [CodecMsgpackMap] — named-field (map) encoding, easier to evolve schemas
//
// During the handshake the client sends an ordered list of preferred codecs; the
// server selects the first codec that both sides support. Custom codecs can be
// installed globally with [RegisterCodec].
//
// # Core Types
//
// The main abstractions are:
//
//   - [Client] — configures and dials outbound connections.
//   - [Server] — listens for and dispatches inbound connections.
//   - [Connection] — a live, negotiated session. Also acts as the default
//     stream (stream 0). Obtained from [Client.Connect] on the client side or
//     passed to [Server] callbacks on the server side.
//   - [Stream] — a multiplexed logical channel within a [Connection].
//     Created by [Connection.NewStream].
//   - [Message] — the marker interface for all payload types. Register concrete
//     types with [Register] so the codec can round-trip them by wire name.
//
// # Quick Start
//
// A minimal echo server and client:
//
//	// ── server ──
//	type Ping struct{ Text string `am:"text"` }
//	type Pong struct{ Text string `am:"text"` }
//
//	adaptivemsg.Register[Ping]()
//	adaptivemsg.Register[Pong]()
//
//	adaptivemsg.NewServer().
//		OnConnect(func(nc adaptivemsg.Netconn) error {
//			fmt.Println("connected:", nc.PeerAddr())
//			return nil
//		}).
//		Serve("tcp://0.0.0.0:9000")
//
//	// Meanwhile, register a handler that echoes Ping → Pong:
//	adaptivemsg.Handle(func(ctx *adaptivemsg.StreamContext, p *Ping) (*Pong, error) {
//		return &Pong{Text: p.Text}, nil
//	})
//
//	// ── client ──
//	conn, err := adaptivemsg.NewClient().
//		WithTimeout(5 * time.Second).
//		Connect("tcp://127.0.0.1:9000")
//	if err != nil { log.Fatal(err) }
//	defer conn.Close()
//
//	reply, err := adaptivemsg.SendRecvAs[*Pong](conn, &Ping{Text: "hello"})
//
// For one-shot request/reply without keeping a connection open, see [Once].
package adaptivemsg

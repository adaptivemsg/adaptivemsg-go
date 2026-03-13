# adaptivemsg-go

Go runtime for the adaptivemsg wire protocol.

This repository is the Go sibling of `adaptivemsg-rust` and is intended to stay
in lockstep with the protocol defined in `adaptivemsg-doc`.

## Client/server example

```go
package main

import (
	"fmt"
	"log"

	am "adaptivemsg"
)

type HelloRequest struct {
	Who string `msgpack:"who"`
}

type HelloInternal struct {
	TraceID string `msgpack:"trace_id"`
}

type HelloReply struct {
	Answer   string        `msgpack:"answer"`
	Internal HelloInternal `msgpack:"internal"`
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
		if err := am.NewServer().Serve("tcp://0.0.0.0:5555"); err != nil {
			log.Fatal(err)
		}
	}()

	// client
	client := am.NewClient()
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
	conn, _ := am.NewClient().Connect("tcp://127.0.0.1:5560")
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
- `Client.WithTimeout`, `Client.WithCodecs`, `Client.WithMaxFrame`, `Client.Connect`

Server:
- `NewServer`
- `Server.Serve`, `Server.OnConnect`, `Server.OnDisconnect`, `Server.OnNewStream`, `Server.OnCloseStream`
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

## Error reasoning

Local input/usage errors:
- `ErrInvalidMessage`: nil or non-struct messages, invalid wire names, compact field issues.
- `ErrUnknownMessage`: wire name not registered in the registry.

Protocol/compat errors:
- `ErrUnsupportedCodec`, `ErrUnsupportedFrameVersion`, `ErrNoCommonCodec`, `ErrTooManyCodecs`,
  `ErrBadHandshakeMagic`, `ErrFrameTooLarge`, `ErrUnsupportedTransport`.

Runtime errors:
- `ErrClosed`, `ErrRecvTimeout`, `ErrConcurrentRecv`, `ErrHandlerTaskBusy`, `ErrConnectTimeout`.

Remote errors:
- `ErrorReply` is sent by the peer; `SendRecv` surfaces it as `ErrRemote{Code, Message}`.
- `protocol_error` = wire mismatch or invalid ordering; `codec_error` = decode/envelope failure;
  `handler_error` = handler returned an error.

Notes:
- Addresses accept `tcp://HOST:PORT`, `uds://@name` (linux abstract), or `uds:///tmp/name.sock`.
- Abstract UDS demo: `go run -tags server ./examples/hello/cmd/server -addr uds://@adaptivemsg-hello` and `go run ./examples/hello/cmd/client -addr uds://@adaptivemsg-hello` (echo uses `@adaptivemsg-echo`).
- Codecs are negotiated from the client's `WithCodecs` preference list; the server selects the first common codec.
- Custom codecs implement `CodecImpl` and register with `RegisterCodec`; msgpack struct tags only apply to the msgpack built-ins.
- Connections act as the default stream; use `am.SendRecvAs[Reply](conn, msg)` for one-off calls or `am.StreamAs[Reply](stream)` for a typed view (needed for `Recv`).
- Register handler/message types with `MustRegisterGlobalType` before `NewClient()`/`NewServer()` so the snapshot sees them.
- Use `PeekWire()` on a stream (or `conn.PeekWire()`) to inspect the next message type before decoding; it honors the same recv timeout and concurrency rules as `Recv`.
- Message names default to `am.<package-leaf>.<TypeName>`; implement `WireName() string` on a type if you need an override.
- Example servers rely on build-tagged handlers; run them with `-tags server` (for example: `go run -tags server ./examples/hello/cmd/server`).

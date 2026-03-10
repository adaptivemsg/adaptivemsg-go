# adaptivemsg-go

Go runtime for the adaptivemsg wire protocol.

This repository is the Go sibling of `adaptivemsg-rust` and is intended to stay
in lockstep with the protocol defined in `adaptivemsg-doc`.

## Minimal usage

```go
package main

import (
	"fmt"

	am "adaptivemsg"
)

type HelloRequest struct {
	Who string `msgpack:"who"`
}

type HelloReply struct {
	Answer string `msgpack:"answer"`
}

func main() {
	reg := am.NewRegistry()
	_ = am.Handle[HelloRequest](reg, func(_ *am.StreamContext, req *HelloRequest) (am.Message, error) {
		return &HelloReply{Answer: fmt.Sprintf("hi, %s", req.Who)}, nil
	})
}
```

## TCP server/client sketch

```go
package main

import (
	"fmt"

	am "adaptivemsg"
)

type HelloRequest struct {
	Who string `msgpack:"who"`
}

type HelloReply struct {
	Answer string `msgpack:"answer"`
}

func main() {
	// server
	serverReg := am.NewRegistry()
	_ = am.Handle[HelloRequest](serverReg, func(_ *am.StreamContext, req *HelloRequest) (am.Message, error) {
		return &HelloReply{Answer: fmt.Sprintf("hi, %s", req.Who)}, nil
	})
	go func() {
		_ = am.NewServer().WithRegistry(serverReg).Serve("tcp://0.0.0.0:5555")
	}()

	// client
	client := am.NewClient()
	conn, _ := client.Connect("tcp://127.0.0.1:5555")
	reply, _ := am.SendRecvAs[*HelloReply](conn, &HelloRequest{Who: "alice"})
	_ = reply
}
```

Notes:
- Connections act as the default stream; use `am.SendRecvAs[Reply](conn, msg)` for one-off calls or `am.StreamAs[Reply](stream)` for a typed view (needed for `Recv`).
- Typed stream methods auto-register expected response types. For unsolicited messages, register types ahead of time with `RegisterTypes`.
- Message names default to `am.<package-leaf>.<TypeName>`; implement `WireName() string` on a type if you need an override.
- Example servers rely on build-tagged handlers; run them with `-tags server` (for example: `go run -tags server ./examples/hello/server`).

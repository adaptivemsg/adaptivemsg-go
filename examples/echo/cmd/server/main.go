package main

import (
	"flag"
	"log"
	"sync/atomic"

	am "adaptivemsg"
	"adaptivemsg/examples/echo"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5560", "listen address (examples: tcp://127.0.0.1:5560, uds://@adaptivemsg-echo, uds:///tmp/adaptivemsg-echo.sock)")
	flag.Parse()

	mgr := echo.NewStatMgr()
	var streamSeq atomic.Uint64

	server := am.NewServer().
		WithRecovery(am.ServerRecoveryOptions{Enable: true}).
		OnConnect(func(netconn am.Netconn) error {
			addr := netconn.PeerAddr()
			if addr == "" {
				addr = "client-unknown"
			}
			mgr.OnConnect(addr)
			log.Printf("connect: %s", addr)
			return nil
		}).
		OnDisconnect(func(netconn am.Netconn) error {
			addr := netconn.PeerAddr()
			if addr == "" {
				addr = "client-unknown"
			}
			mgr.OnDisconnect(addr)
			log.Printf("disconnect: %s", addr)
			return nil
		}).
		OnNewStream(func(ctx *am.StreamContext) {
			ctx.SetContext(mgr)
			id := streamSeq.Add(1)
			log.Printf("on new stream: %d", id)
		})

	log.Printf("echo server listening on %s (recovery/v3 enabled)", *addr)
	if err := server.Serve(*addr); err != nil {
		log.Fatal(err)
	}
}

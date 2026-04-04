package main

import (
	"flag"
	"log"

	am "github.com/adaptivemsg/adaptivemsg-go"
	_ "github.com/adaptivemsg/adaptivemsg-go/examples/hello"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5555", "listen address (examples: tcp://127.0.0.1:5555, uds://@adaptivemsg-hello, uds:///tmp/adaptivemsg-hello.sock)")
	flag.Parse()

	server := am.NewServer().WithRecovery(am.ServerRecoveryOptions{Enable: true})

	log.Printf("hello server listening on %s (recovery/v3 enabled)", *addr)
	if err := server.Serve(*addr); err != nil {
		log.Fatal(err)
	}
}

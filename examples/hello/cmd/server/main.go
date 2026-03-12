package main

import (
	"flag"
	"log"

	am "adaptivemsg"
	_ "adaptivemsg/examples/hello"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5555", "listen address (examples: tcp://127.0.0.1:5555, uds://@adaptivemsg-hello, uds:///tmp/adaptivemsg-hello.sock)")
	flag.Parse()

	log.Printf("hello server listening on %s", *addr)
	if err := am.NewServer().Serve(*addr); err != nil {
		log.Fatal(err)
	}
}

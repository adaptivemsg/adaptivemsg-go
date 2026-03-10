package main

import (
	"flag"
	"log"

	am "adaptivemsg"
	_ "adaptivemsg/examples/hello/message"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5555", "listen address")
	flag.Parse()

	log.Printf("hello server listening on %s", *addr)
	if err := am.NewServer().Serve(*addr); err != nil {
		log.Fatal(err)
	}
}

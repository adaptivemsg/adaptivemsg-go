package main

import (
	"errors"
	"flag"
	"log"
	"sync"

	am "adaptivemsg"
	"adaptivemsg/examples/hello/message"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5555", "server address")
	flag.Parse()

	client := am.NewClient()
	conn, err := client.Connect(*addr)
	if err != nil {
		log.Fatal(err)
	}

	streamA := conn.NewStream()
	streamB := conn.NewStream()

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		reply, err := am.SendRecvAs[*message.HelloReply](conn, &message.HelloRequest{
			Who:      "John",
			Question: "who are you",
		})
		if err != nil {
			log.Printf("default stream error: %v", err)
			return
		}
		log.Printf("default stream: %s", reply.Answer)
	}()

	go func() {
		defer wg.Done()
		reply, err := am.SendRecvAs[*message.HelloReply](streamA, &message.HelloRequest{
			Who:      "Alice",
			Question: "how are you",
		})
		if err != nil {
			log.Printf("stream A error: %v", err)
			return
		}
		log.Printf("stream A: %s", reply.Answer)
	}()

	go func() {
		defer wg.Done()
		_, err := am.SendRecvAs[*message.HelloReply](streamB, &message.HelloRequest{
			Who:      "Bob",
			Question: "error please",
		})
		if err != nil {
			var remote am.ErrRemote
			if errors.As(err, &remote) {
				log.Printf("stream B expected error: %s: %s", remote.Code, remote.Message)
				return
			}
			log.Printf("stream B error: %v", err)
			return
		}
		log.Printf("stream B: unexpected success")
	}()

	wg.Wait()
}

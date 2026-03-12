package main

import (
	"errors"
	"flag"
	"log"
	"sync"
	"time"

	am "adaptivemsg"
	"adaptivemsg/examples/echo"
)

func main() {
	addr := flag.String("addr", "tcp://127.0.0.1:5560", "server address (examples: tcp://127.0.0.1:5560, uds://@adaptivemsg-echo, uds:///tmp/adaptivemsg-echo.sock)")
	cmd := flag.String("cmd", "echo", "echo (default), timeout, whoelse, or whoelse_sub")
	flag.Parse()

	client := am.NewClient()
	conn, err := client.Connect(*addr)
	if err != nil {
		log.Fatal(err)
	}

	switch *cmd {
	case "timeout":
		if err := timeoutDemo(conn); err != nil {
			log.Fatal(err)
		}
	case "whoelse":
		if err := whoelseQueryDemo(conn); err != nil {
			log.Fatal(err)
		}
	case "whoelse_sub":
		if err := whoelseSubscribeDemo(conn); err != nil {
			log.Fatal(err)
		}
	default:
		if err := echoDemo(conn, *addr); err != nil {
			log.Fatal(err)
		}
	}
}

func timeoutDemo(conn *am.Connection) error {
	stream := conn.NewStream()
	{
		stream := am.StreamAs[*am.OkReply](stream)
		log.Printf("No timeout by default")
		if _, err := stream.SendRecv(&echo.MessageTimeout{Secs: 10}); err != nil {
			return err
		}
		log.Printf("Recv OK")

		log.Printf("Set timeout to 15s")
		stream.SetRecvTimeout(15 * time.Second)
		if _, err := stream.SendRecv(&echo.MessageTimeout{Secs: 10}); err != nil {
			return err
		}
		log.Printf("Recv OK")

		log.Printf("Set timeout to 3s")
		stream.SetRecvTimeout(3 * time.Second)
		if _, err := stream.SendRecv(&echo.MessageTimeout{Secs: 10}); err != nil {
			var timeoutErr am.ErrRecvTimeout
			if errors.As(err, &timeoutErr) {
				log.Printf("Recv timeout: %v", err)
			} else {
				return err
			}
		} else {
			log.Printf("Some unexpected error happened")
		}

		log.Printf("Set back to no timeout")
		stream.SetRecvTimeout(0)
		if _, err := stream.SendRecv(&echo.MessageTimeout{Secs: 10}); err != nil {
			return err
		}
		log.Printf("Recv OK")
	}
	return nil
}

func whoelseSubscribeDemo(conn *am.Connection) error {
	stream := conn.NewStream()
	if _, err := am.SendRecvAs[*am.OkReply](stream, &echo.SubWhoElseEvent{}); err != nil {
		return err
	}

	{
		stream := am.StreamAs[*echo.WhoElseEvent](stream)
		for {
			evt, err := stream.Recv()
			if err != nil {
				return err
			}
			log.Printf("event: new client %s", evt.Addr)
		}
	}
}

func whoelseQueryDemo(conn *am.Connection) error {
	for i := 0; i < 100; i++ {
		rep, err := am.SendRecvAs[*echo.WhoElseReply](conn, &echo.WhoElse{})
		if err != nil {
			return err
		}
		log.Printf("clients: %s", rep.Clients)
		time.Sleep(2 * time.Second)
	}
	return nil
}

func echoDemo(conn *am.Connection, addr string) error {
	msg := "ni hao"
	var num int32

	for i := 0; i < 5; i++ {
		num += 100
		req := &echo.MessageRequest{Msg: msg, Num: num}
		rep, err := am.SendRecvAs[*echo.MessageReply](conn, req)
		if err != nil {
			return err
		}
		log.Printf("%s:%d ==> %s:%d, %s", msg, num, rep.Msg, rep.Num, rep.Signature)
	}

	return concurrentDemo(addr)
}

func concurrentDemo(addr string) error {
	var clientWG sync.WaitGroup
	errCh := make(chan error, 1)

	reportErr := func(err error) {
		select {
		case errCh <- err:
		default:
		}
	}

	for i := 0; i < 6; i++ {
		clientWG.Add(1)
		go func() {
			defer clientWG.Done()

			client := am.NewClient()
			conn, err := client.Connect(addr)
			if err != nil {
				reportErr(err)
				return
			}

			var streamWG sync.WaitGroup
			for j := 0; j < 10; j++ {
				streamWG.Add(1)
				go func(idx int) {
					defer streamWG.Done()
					stream := conn.NewStream()
					{
						msg := "ni hao"
						num := int32(100 * idx)
						for k := 0; k < 9; k++ {
							num += 10
							req := &echo.MessageRequest{Msg: msg, Num: num}
							rep, err := am.SendRecvAs[*echo.MessageReply](stream, req)
							if err != nil {
								reportErr(err)
								return
							}
							if num+1 != rep.Num {
								reportErr(errors.New("wrong number: reply does not match"))
								return
							}
						}
					}
				}(j)
			}
			streamWG.Wait()
		}()
	}

	clientWG.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

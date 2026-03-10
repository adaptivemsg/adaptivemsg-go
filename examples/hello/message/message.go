package message

type HelloRequest struct {
	Who      string `msgpack:"who"`
	Question string `msgpack:"question"`
}

type HelloReply struct {
	Answer string `msgpack:"answer"`
}

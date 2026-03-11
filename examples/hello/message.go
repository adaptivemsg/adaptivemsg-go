package hello

type HelloRequest struct {
	Who      string `msgpack:"who"`
	Question string `msgpack:"question"`
}

type HelloReply struct {
	Answer   string        `msgpack:"answer"`
	Internal HelloInternal `msgpack:"internal"`
}

type HelloInternal struct {
	TraceID string `msgpack:"trace_id"`
}

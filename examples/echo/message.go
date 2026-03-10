package echo

type MessageRequest struct {
	Msg string `msgpack:"msg"`
	Num int32  `msgpack:"num"`
}

type MessageReply struct {
	Msg       string `msgpack:"msg"`
	Num       int32  `msgpack:"num"`
	Signature string `msgpack:"signature"`
}

type SubWhoElseEvent struct{}

type WhoElseEvent struct {
	Addr string `msgpack:"addr"`
}

type WhoElse struct{}

type WhoElseReply struct {
	Clients string `msgpack:"clients"`
}

type MessageTimeout struct {
	Secs uint64 `msgpack:"secs"`
}

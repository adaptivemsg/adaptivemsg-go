package echo

type MessageRequest struct {
	Msg string `am:"msg"`
	Num int32  `am:"num"`
}

type MessageReply struct {
	Msg       string `am:"msg"`
	Num       int32  `am:"num"`
	Signature string `am:"signature"`
}

type SubWhoElseEvent struct{}

type WhoElseEvent struct {
	Addr string `am:"addr"`
}

type WhoElse struct{}

type WhoElseReply struct {
	Clients string `am:"clients"`
}

type MessageTimeout struct {
	Secs uint64 `am:"secs"`
}

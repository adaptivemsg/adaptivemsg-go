package adaptivemsg

type frameRecord struct {
	streamID uint32
	seq      uint64
	payload  []byte
	size     int64
}

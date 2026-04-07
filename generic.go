package adaptivemsg

// Link is the sealed interface for SendRecvAs targets.
// Connection, Stream, and OnceConn implement Link.
type Link interface {
	isLink()
}

// StreamAs returns a typed Stream view for a Link.
func StreamAs[T any](v Link) *Stream[T] {
	vcp, ok := v.(viewCoreProvider)
	if !ok || v == nil {
		return nil
	}
	if stream, ok := v.(*Stream[T]); ok {
		return stream
	}
	core := vcp.viewCore()
	if core == nil {
		return nil
	}
	return &Stream[T]{core: core}
}

// SendRecvAs sends a message and receives a typed reply.
// The target v can be a Connection, Stream, or OnceConn.
func SendRecvAs[T any](v Link, msg Message) (T, error) {
	var zero T
	if v == nil {
		return zero, ErrInvalidMessage{Reason: "link is nil"}
	}
	if o, ok := v.(*OnceConn); ok {
		return onceSendRecv[T](o, msg)
	}
	stream := StreamAs[T](v)
	if stream == nil {
		return zero, ErrInvalidMessage{Reason: "stream view is nil"}
	}
	return stream.SendRecv(msg)
}

func onceSendRecv[T any](o *OnceConn, msg Message) (T, error) {
	var zero T
	client := NewClient().WithTimeout(o.timeout)
	if len(o.codecs) > 0 {
		client = client.WithCodecs(o.codecs...)
	}
	conn, err := client.Connect(o.addr)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	stream := StreamAs[T](conn)
	if stream == nil {
		return zero, ErrInvalidMessage{Reason: "stream view is nil"}
	}
	stream.SetRecvTimeout(o.timeout)
	return stream.SendRecv(msg)
}

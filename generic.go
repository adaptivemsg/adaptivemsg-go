package adaptivemsg

// Link is a sealed interface that unifies [Connection], [Stream], and
// [OnceConn] as valid targets for [SendRecvAs] and [StreamAs]. The
// unexported isLink method prevents external packages from implementing
// Link.
type Link interface {
	isLink()
}

// StreamAs creates a typed [Stream][T] view from any [Link]. It does not
// allocate a new stream — it wraps the existing stream core with the
// requested type parameter. Returns nil if v is nil or has no underlying
// stream core.
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

// SendRecvAs sends msg via the given [Link] and receives a typed reply of
// type T. When v is an [OnceConn], SendRecvAs dials the address, exchanges
// one request-reply message, and closes the connection. When v is a
// [Connection] or [Stream], it delegates to [Stream.SendRecv]. Returns
// [ErrInvalidMessage] if v is nil.
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

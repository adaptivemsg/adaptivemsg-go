package adaptivemsg

import "time"

const defaultOnceTimeout = 5 * time.Second

// OnceConn is a builder for one-shot request-reply over a short-lived
// connection. Create one with [Once], optionally configure it with
// [OnceConn.WithTimeout] or [OnceConn.WithCodecs], and pass it to
// [SendRecvAs] exactly once. An OnceConn is not reusable. The default
// timeout is 5 seconds, covering both connection establishment and reply
// wait.
type OnceConn struct {
	addr    string
	timeout time.Duration
	codecs  []CodecID
}

func (*OnceConn) isLink() {}

// Once creates a new [OnceConn] builder targeting the given address.
// The addr parameter supports "tcp://host:port", "uds://path",
// "unix://path", or a bare "host:port" (which defaults to TCP). The
// returned OnceConn can be passed to [SendRecvAs] to dial, exchange one
// request-reply, and close the connection.
func Once(addr string) *OnceConn {
	return &OnceConn{addr: addr, timeout: defaultOnceTimeout}
}

// WithTimeout overrides the default 5-second timeout for both connection
// establishment and reply wait.
func (o *OnceConn) WithTimeout(d time.Duration) *OnceConn {
	o.timeout = d
	return o
}

// WithCodecs overrides the default codec preference list (compact, map) for
// the short-lived connection's handshake negotiation.
func (o *OnceConn) WithCodecs(codecs ...CodecID) *OnceConn {
	o.codecs = append([]CodecID(nil), codecs...)
	return o
}

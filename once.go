package adaptivemsg

import "time"

const defaultOnceTimeout = 5 * time.Second

// OnceConn is a builder for a short-lived connection.
// Created by Once(addr) and configured with builder methods.
// Passed to SendRecvAs as a Link target.
type OnceConn struct {
	addr    string
	timeout time.Duration
	codecs  []CodecID
}

func (*OnceConn) isLink() {}

// Once returns a builder for a short-lived connection to addr.
// The returned OnceConn can be passed to SendRecvAs to dial, exchange
// one request-reply, and close the connection.
func Once(addr string) *OnceConn {
	return &OnceConn{addr: addr, timeout: defaultOnceTimeout}
}

// WithTimeout sets the dial and receive timeout.
func (o *OnceConn) WithTimeout(d time.Duration) *OnceConn {
	o.timeout = d
	return o
}

// WithCodecs sets the preferred codec list for the short-lived connection.
func (o *OnceConn) WithCodecs(codecs ...CodecID) *OnceConn {
	o.codecs = append([]CodecID(nil), codecs...)
	return o
}

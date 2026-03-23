package adaptivemsg

import (
	"net"
	"testing"
)

func mustCodec(t *testing.T, id CodecID) CodecImpl {
	t.Helper()
	codec, ok := codecByID(id)
	if !ok {
		t.Fatalf("codec %d not registered", id)
	}
	return codec
}

type shortWriteConn struct {
	net.Conn
	maxWrite int
}

func (c *shortWriteConn) Write(p []byte) (int, error) {
	if c.maxWrite > 0 && len(p) > c.maxWrite {
		p = p[:c.maxWrite]
	}
	return c.Conn.Write(p)
}

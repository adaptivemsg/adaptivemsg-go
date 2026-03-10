package adaptivemsg

import (
	"net"
	"time"
)

func dialTCP(addr string, timeout time.Duration) (net.Conn, error) {
	if timeout > 0 {
		dialer := net.Dialer{Timeout: timeout}
		return dialer.Dial("tcp", addr)
	}
	return net.Dial("tcp", addr)
}

func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func acceptTCP(listener net.Listener) (net.Conn, string, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, "", err
	}
	peer := ""
	if conn.RemoteAddr() != nil {
		peer = conn.RemoteAddr().String()
	}
	return conn, peer, nil
}

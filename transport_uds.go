package adaptivemsg

import (
	"net"
	"runtime"
	"strings"
	"time"
)

func dialUDS(path string, timeout time.Duration) (net.Conn, error) {
	udsPath, err := normalizeUDSPath(path)
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		dialer := net.Dialer{Timeout: timeout}
		return dialer.Dial("unix", udsPath)
	}
	return net.Dial("unix", udsPath)
}

func listenUDS(path string) (net.Listener, error) {
	udsPath, err := normalizeUDSPath(path)
	if err != nil {
		return nil, err
	}
	return net.Listen("unix", udsPath)
}

func acceptUDS(listener net.Listener) (net.Conn, string, error) {
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

func normalizeUDSPath(path string) (string, error) {
	if strings.HasPrefix(path, "unix://") {
		return normalizeUDSPath(strings.TrimPrefix(path, "unix://"))
	}
	if strings.HasPrefix(path, "uds://") {
		return normalizeUDSPath(strings.TrimPrefix(path, "uds://"))
	}
	if strings.HasPrefix(path, "@") {
		if runtime.GOOS != "linux" {
			return "", ErrUnsupported{Reason: "abstract UDS is only supported on linux"}
		}
		return "\x00" + path[1:], nil
	}
	return path, nil
}

package adaptivemsg

import (
	"net"
	"strings"
)

type Netconn struct {
	peerAddr string
}

func (n Netconn) PeerAddr() string {
	return n.peerAddr
}

type Server struct {
	registry      *Registry
	onConnect     func(Netconn) error
	onDisconnect  func(Netconn) error
	onNewStream   func(*Context)
	onCloseStream func(*Context)
}

func NewServer() *Server {
	return &Server{
		registry: NewServerRegistry(),
	}
}

func (s *Server) WithRegistry(registry *Registry) *Server {
	s.registry = registry
	return s
}

func (s *Server) Registry() *Registry {
	return s.registry
}

func (s *Server) OnConnect(f func(Netconn) error) *Server {
	s.onConnect = f
	return s
}

func (s *Server) OnDisconnect(f func(Netconn) error) *Server {
	s.onDisconnect = f
	return s
}

func (s *Server) OnNewStream(f func(*Context)) *Server {
	s.onNewStream = f
	return s
}

func (s *Server) OnCloseStream(f func(*Context)) *Server {
	s.onCloseStream = f
	return s
}

func (s *Server) Serve(addr string) error {
	if strings.HasPrefix(addr, "tcp://") {
		return s.serveTCP(strings.TrimPrefix(addr, "tcp://"))
	}
	if strings.HasPrefix(addr, "uds://") || strings.HasPrefix(addr, "unix://") {
		return s.serveUDS(addr)
	}
	return s.serveTCP(addr)
}

func (s *Server) serveTCP(addr string) error {
	listener, err := listenTCP(addr)
	if err != nil {
		return err
	}
	return s.serveWithAccept(func() (net.Conn, string, error) {
		return acceptTCP(listener)
	})
}

func (s *Server) serveUDS(addr string) error {
	listener, err := listenUDS(addr)
	if err != nil {
		return err
	}
	return s.serveWithAccept(func() (net.Conn, string, error) {
		return acceptUDS(listener)
	})
}

func (s *Server) serveWithAccept(accept func() (net.Conn, string, error)) error {
	for {
		conn, peer, err := accept()
		if err != nil {
			return err
		}
		netconn := Netconn{peerAddr: peer}
		go s.handleConn(conn, netconn)
	}
}

func (s *Server) handleConn(conn net.Conn, netconn Netconn) {
	pending := newPendingConnection(conn, s.registry, s.onNewStream, s.onCloseStream)
	if s.onConnect != nil {
		if err := s.onConnect(netconn); err != nil {
			_ = conn.Close()
			return
		}
	}
	connection, err := pending.StartServer(defaultMaxFrame)
	if err != nil {
		_ = conn.Close()
		return
	}
	connection.WaitClosed()
	if s.onDisconnect != nil {
		_ = s.onDisconnect(netconn)
	}
}

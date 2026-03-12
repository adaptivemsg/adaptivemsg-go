package adaptivemsg

import (
	"net"
	"strings"
)

// Netconn describes a peer connection.
type Netconn struct {
	peerAddr string
}

// PeerAddr returns the peer address, when known.
func (n Netconn) PeerAddr() string {
	return n.peerAddr
}

// Server accepts inbound connections and dispatches streams.
type Server struct {
	registry      *registry
	onConnect     func(Netconn) error
	onDisconnect  func(Netconn) error
	onNewStream   func(*StreamContext)
	onCloseStream func(*StreamContext)
}

// NewServer returns a server with default settings.
func NewServer() *Server {
	return &Server{
		registry: newRegistrySnapshot(),
	}
}

// OnConnect registers a callback for new connections.
func (s *Server) OnConnect(f func(Netconn) error) *Server {
	s.onConnect = f
	return s
}

// OnDisconnect registers a callback for closed connections.
func (s *Server) OnDisconnect(f func(Netconn) error) *Server {
	s.onDisconnect = f
	return s
}

// OnNewStream registers a callback for newly created streams.
func (s *Server) OnNewStream(f func(*StreamContext)) *Server {
	s.onNewStream = f
	return s
}

// OnCloseStream registers a callback for stream closures.
func (s *Server) OnCloseStream(f func(*StreamContext)) *Server {
	s.onCloseStream = f
	return s
}

// Serve listens on the address and serves connections.
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
	if s.registry == nil {
		s.registry = newRegistrySnapshot()
	}
	pending := newPendingConnection(conn, s.registry, s.onNewStream, s.onCloseStream)
	if s.onConnect != nil {
		if err := s.onConnect(netconn); err != nil {
			_ = conn.Close()
			return
		}
	}
	connection, err := pending.startServer(defaultMaxFrame)
	if err != nil {
		_ = conn.Close()
		return
	}
	connection.WaitClosed()
	if s.onDisconnect != nil {
		_ = s.onDisconnect(netconn)
	}
}

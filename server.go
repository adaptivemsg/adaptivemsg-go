package adaptivemsg

import (
	"net"
	"strings"
	"sync"
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
	codecs        []CodecID
	recovery      ServerRecoveryOptions
	recoveryMu    sync.Mutex
	recoveryConns map[recoveryToken]*Connection
	onConnect     func(Netconn) error
	onDisconnect  func(Netconn) error
	onNewStream   func(*StreamContext)
	onCloseStream func(*StreamContext)
}

// NewServer returns a server with default settings.
func NewServer() *Server {
	return &Server{
		registry:      newRegistrySnapshot(),
		codecs:        []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		recovery:      defaultServerRecoveryOptions(),
		recoveryConns: make(map[recoveryToken]*Connection),
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

// WithCodecs sets the supported codec list; client order determines selection.
func (s *Server) WithCodecs(codecs ...CodecID) *Server {
	s.codecs = append([]CodecID(nil), codecs...)
	return s
}

// WithRecovery configures recovery behavior and returns the server.
func (s *Server) WithRecovery(opts ServerRecoveryOptions) *Server {
	s.recovery = opts.normalized()
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
	config, err := handshakeServer(conn, s.codecs, defaultMaxFrame, s.recovery.Enable)
	if err != nil {
		_ = conn.Close()
		return
	}
	if config.version != protocolVersionV3 {
		pending := newPendingConnection(conn, s.registry, s.onNewStream, s.onCloseStream)
		if s.onConnect != nil {
			if err := s.onConnect(netconn); err != nil {
				_ = conn.Close()
				return
			}
		}
		connection := pending.startWithConfig(config)
		connection.WaitClosed()
		if s.onDisconnect != nil {
			_ = s.onDisconnect(netconn)
		}
		return
	}

	if err := s.handleRecoveryConn(conn, netconn, config); err != nil {
		_ = conn.Close()
	}
}

func (s *Server) handleRecoveryConn(conn net.Conn, netconn Netconn, config connConfig) error {
	req, err := readAttachRequest(conn)
	if err != nil {
		return err
	}

	switch req.mode {
	case attachModeNew:
		connectionID, err := newRecoveryToken()
		if err != nil {
			return err
		}
		resumeSecret, err := newRecoveryToken()
		if err != nil {
			return err
		}
		pending := newPendingConnection(conn, s.registry, s.onNewStream, s.onCloseStream)
		connection := pending.connection
		connection.config = config
		connection.recovery = newServerRecoveryState(s.recovery, s, connectionID, resumeSecret)
		negotiated := s.recovery.negotiated()
		s.storeRecoveryConnection(connectionID, connection)
		if err := writeAttachResponse(conn, attachResponse{
			status:       attachStatusOK,
			connectionID: connectionID,
			resumeSecret: resumeSecret,
			lastRecvSeq:  0,
			negotiated:   negotiated,
		}); err != nil {
			s.removeRecoveryConnection(connectionID, connection)
			return err
		}
		connection.attachTransport(conn, 0)
		if s.onConnect != nil {
			if err := s.onConnect(netconn); err != nil {
				connection.markClosed()
				return err
			}
		}
		connection.start()
		if s.onDisconnect != nil {
			go func() {
				connection.WaitClosed()
				_ = s.onDisconnect(netconn)
			}()
		}
		return nil

	case attachModeResume:
		connection, ok := s.lookupRecoveryConnection(req.connectionID)
		if !ok || connection == nil || connection.recovery == nil || connection.closed.Load() {
			_ = writeAttachResponse(conn, attachResponse{status: attachStatusRejected})
			return ErrResumeRejected{Reason: "connection not found"}
		}
		if connection.recovery.resumeSecret != req.resumeSecret {
			_ = writeAttachResponse(conn, attachResponse{status: attachStatusRejected})
			return ErrResumeRejected{Reason: "resume secret mismatch"}
		}
		if connection.config.codecID != config.codecID {
			_ = writeAttachResponse(conn, attachResponse{status: attachStatusRejected})
			return ErrResumeRejected{Reason: "codec mismatch"}
		}
		if err := writeAttachResponse(conn, attachResponse{
			status:       attachStatusOK,
			connectionID: connection.recovery.connectionID,
			resumeSecret: connection.recovery.resumeSecret,
			lastRecvSeq:  connection.recovery.lastReceived(),
			negotiated:   connection.recovery.negotiated(),
		}); err != nil {
			return err
		}
		connection.attachTransport(conn, req.lastRecvSeq)
		return nil
	default:
		_ = writeAttachResponse(conn, attachResponse{status: attachStatusRejected})
		return ErrResumeRejected{Reason: "invalid attach mode"}
	}
}

func (s *Server) storeRecoveryConnection(id recoveryToken, connection *Connection) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if s.recoveryConns == nil {
		s.recoveryConns = make(map[recoveryToken]*Connection)
	}
	s.recoveryConns[id] = connection
}

func (s *Server) lookupRecoveryConnection(id recoveryToken) (*Connection, bool) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	connection, ok := s.recoveryConns[id]
	return connection, ok
}

func (s *Server) removeRecoveryConnection(id recoveryToken, connection *Connection) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if current, ok := s.recoveryConns[id]; ok && current == connection {
		delete(s.recoveryConns, id)
	}
}

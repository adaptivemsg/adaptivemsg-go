package adaptivemsg

import (
	"net"
	"strings"
	"sync"
)

// Netconn is a lightweight descriptor for a peer connection, passed to
// [Server] lifecycle callbacks ([Server.OnConnect], [Server.OnDisconnect]).
// It carries metadata about the remote endpoint but does not expose the
// underlying transport directly.
type Netconn struct {
	peerAddr string
}

// PeerAddr returns the remote address of the peer as a string, for example
// "192.168.1.5:54321" for a TCP connection or a filesystem path for a Unix
// domain socket. The value is empty if the address could not be determined.
func (n Netconn) PeerAddr() string {
	return n.peerAddr
}

// Server accepts inbound connections and dispatches them to registered
// handlers and callbacks. It uses a builder pattern for configuration:
//
//	adaptivemsg.NewServer().
//		OnConnect(func(nc adaptivemsg.Netconn) error { return nil }).
//		WithCodecs(adaptivemsg.CodecMsgpackCompact).
//		Serve("tcp://0.0.0.0:9000")
//
// Each accepted connection is handled in its own goroutine. [Server.Serve]
// blocks for the lifetime of the server; it only returns if the underlying
// listener encounters an unrecoverable error.
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

// NewServer returns a Server with default settings: both MessagePack codecs
// supported ([CodecMsgpackCompact] and [CodecMsgpackMap]), recovery disabled,
// and no lifecycle callbacks registered.
func NewServer() *Server {
	return &Server{
		registry:      newRegistrySnapshot(),
		codecs:        []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		recovery:      defaultServerRecoveryOptions(),
		recoveryConns: make(map[recoveryToken]*Connection),
	}
}

// OnConnect registers a callback that is invoked each time a new connection
// is accepted and the handshake completes. The callback receives a [Netconn]
// descriptor for the peer. Returning a non-nil error rejects the connection
// and closes the underlying transport.
func (s *Server) OnConnect(f func(Netconn) error) *Server {
	s.onConnect = f
	return s
}

// OnDisconnect registers a callback that is invoked after a connection has
// fully closed. The callback receives the same [Netconn] descriptor that was
// passed to OnConnect. The return value is currently ignored but reserved for
// future use; callers should return nil.
func (s *Server) OnDisconnect(f func(Netconn) error) *Server {
	s.onDisconnect = f
	return s
}

// OnNewStream registers a callback that is invoked when a new stream is
// created on any connection managed by this server. This is useful for
// per-stream initialization such as attaching context or state to the
// [StreamContext].
func (s *Server) OnNewStream(f func(*StreamContext)) *Server {
	s.onNewStream = f
	return s
}

// OnCloseStream registers a callback that is invoked when a stream is
// destroyed, either because the peer closed it or because the parent
// connection closed. This is useful for per-stream cleanup.
func (s *Server) OnCloseStream(f func(*StreamContext)) *Server {
	s.onCloseStream = f
	return s
}

// WithCodecs sets the server's list of supported codecs. During the handshake
// the server walks the client's ordered preference list and selects the first
// codec that appears in both lists. If no common codec is found the handshake
// fails with [ErrNoCommonCodec].
func (s *Server) WithCodecs(codecs ...CodecID) *Server {
	s.codecs = append([]CodecID(nil), codecs...)
	return s
}

// WithRecovery enables the v3 protocol extension on the server side, allowing
// clients to reconnect and resume sessions after transient network failures.
// See [ServerRecoveryOptions] for tunables such as detached TTL, replay buffer
// limits, ACK intervals, and heartbeat configuration.
func (s *Server) WithRecovery(opts ServerRecoveryOptions) *Server {
	s.recovery = opts.normalized()
	return s
}

// Serve binds to addr and enters an accept loop, blocking for the lifetime of
// the server. Each accepted connection is handled in a dedicated goroutine
// that performs the protocol handshake and then runs the reader/writer loops.
//
// Supported address formats:
//
//   - "tcp://host:port" — TCP listener
//   - "uds:///path/to/sock" or "unix:///path/to/sock" — Unix domain socket
//   - "host:port" — bare host:port defaults to TCP
//
// Serve returns a non-nil error only if the listener itself fails (e.g. the
// address is already in use).
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

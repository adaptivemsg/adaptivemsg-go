package adaptivemsg

import (
	"context"
	"crypto/subtle"
	"net"
	"sync"
	"time"
)

// defaultHandshakeTimeout bounds the handshake and the v3 attach on an
// accepted connection, so a peer that connects and sends nothing does not
// hold a goroutine and a file descriptor. It leaves room for lossy links and
// for a server that a burst of reconnects keeps busy.
const defaultHandshakeTimeout = 2 * time.Minute

// Bounds for the wait between attempts after Accept fails, for example with
// EMFILE when the process is out of file descriptors; such errors clear once
// connections close.
const (
	acceptRetryMinBackoff = 5 * time.Millisecond
	acceptRetryMaxBackoff = time.Second
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
	registry         *registry
	codecs           []CodecID
	maxFrame         uint32
	handshakeTimeout time.Duration
	streamQueueSize  int
	recovery         ServerRecoveryOptions
	recoveryMu       sync.Mutex
	recoveryConns    map[recoveryToken]*Connection
	onConnect        func(Netconn) error
	onDisconnect     func(Netconn) error
	onNewStream      func(*StreamContext)
	onCloseStream    func(*StreamContext)

	// lifecycleMu guards what Shutdown stops: the listeners, the connections
	// still in the handshake, and the established connections.
	lifecycleMu sync.Mutex
	stopping    bool
	stopCh      chan struct{}
	listeners   map[net.Listener]struct{}
	handshakes  map[net.Conn]struct{}
	conns       map[*Connection]struct{}
	// running counts the goroutines that serve connections, including those
	// waiting to run OnDisconnect.
	running sync.WaitGroup
}

// NewServer returns a Server with default settings: both MessagePack codecs
// supported ([CodecMsgpackCompact] and [CodecMsgpackMap]), a 16 MiB max frame
// size, a 2-minute handshake timeout, recovery disabled, and no lifecycle
// callbacks registered.
func NewServer() *Server {
	return &Server{
		registry:         newRegistrySnapshot(),
		codecs:           []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		maxFrame:         defaultMaxFrame,
		handshakeTimeout: defaultHandshakeTimeout,
		recovery:         defaultServerRecoveryOptions(),
		recoveryConns:    make(map[recoveryToken]*Connection),
		stopCh:           make(chan struct{}),
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

// WithMaxFrame sets the maximum payload size per frame that the server
// accepts and advertises during the handshake. The effective limit for a
// connection is the minimum of the client's and the server's values. A
// larger frame closes the connection. The default is 16 MiB.
func (s *Server) WithMaxFrame(maxFrame uint32) *Server {
	s.maxFrame = maxFrame
	return s
}

// WithHandshakeTimeout sets how long an accepted connection may take to
// complete the handshake and, with recovery, the attach; the server closes a
// connection that takes longer. Zero or a negative value disables the limit.
// The default is 2 minutes. The limit no longer applies once [Server.OnConnect]
// runs.
func (s *Server) WithHandshakeTimeout(timeout time.Duration) *Server {
	s.handshakeTimeout = timeout
	return s
}

// WithStreamQueueSize sets how many received messages each stream buffers:
// messages waiting for a handler or Recv, and frames waiting to be decoded.
// When a stream's buffer is full, the connection stops reading until the
// stream catches up, which holds up its other streams too. A smaller buffer
// saves memory on connections with many streams but reaches that point
// sooner. The default is 1024; zero or a negative value keeps the default.
func (s *Server) WithStreamQueueSize(size int) *Server {
	s.streamQueueSize = size
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

// Serve binds to addr and enters an accept loop, blocking until
// [Server.Shutdown]. Each accepted connection is handled in a dedicated
// goroutine that performs the protocol handshake and then runs the
// reader/writer loops.
//
// Supported address formats:
//
//   - "tcp://host:port" — TCP listener
//   - "uds:///path/to/sock" or "unix:///path/to/sock" — Unix domain socket
//   - "host:port" — bare host:port defaults to TCP
//
// Serve returns an error if it cannot listen on addr, for example because the
// address is already in use, and [ErrServerClosed] after [Server.Shutdown]. A
// failed accept, for example when the process runs out of file descriptors,
// does not end Serve: it waits briefly and accepts again.
func (s *Server) Serve(addr string) error {
	if s.isStopping() {
		return ErrServerClosed{}
	}
	network, address, err := parseAddr(addr)
	if err != nil {
		return err
	}
	if network == "unix" {
		return s.serveUDS(address)
	}
	return s.serveTCP(address)
}

func (s *Server) serveTCP(addr string) error {
	listener, err := listenTCP(addr)
	if err != nil {
		return err
	}
	return s.serveListener(listener, func() (net.Conn, string, error) {
		return acceptTCP(listener)
	})
}

func (s *Server) serveUDS(addr string) error {
	listener, err := listenUDS(addr)
	if err != nil {
		return err
	}
	return s.serveListener(listener, func() (net.Conn, string, error) {
		return acceptUDS(listener)
	})
}

func (s *Server) serveListener(listener net.Listener, accept func() (net.Conn, string, error)) error {
	if !s.trackListener(listener) {
		_ = listener.Close()
		return ErrServerClosed{}
	}
	defer func() {
		s.untrackListener(listener)
		_ = listener.Close()
	}()
	var backoff time.Duration
	for {
		conn, peer, err := accept()
		if err != nil {
			if s.isStopping() {
				return ErrServerClosed{}
			}
			backoff = min(max(2*backoff, acceptRetryMinBackoff), acceptRetryMaxBackoff)
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-s.stopCh:
				timer.Stop()
				return ErrServerClosed{}
			}
			continue
		}
		backoff = 0
		if !s.beginHandshake(conn) {
			_ = conn.Close()
			return ErrServerClosed{}
		}
		go func() {
			defer s.running.Done()
			s.handleConn(conn, Netconn{peerAddr: peer})
		}()
	}
}

func (s *Server) handleConn(conn net.Conn, netconn Netconn) {
	defer s.endHandshake(conn)
	if s.handshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.handshakeTimeout))
	}
	config, err := handshakeServer(conn, s.codecs, s.maxFrame, s.recovery.Enable)
	if err != nil {
		_ = conn.Close()
		return
	}
	if config.version != protocolVersionV3 {
		_ = conn.SetDeadline(time.Time{})
		pending := s.pendingConnection(conn)
		if s.onConnect != nil {
			if err := s.onConnect(netconn); err != nil {
				_ = conn.Close()
				return
			}
		}
		connection := pending.startWithConfig(config)
		if !s.trackConn(connection, conn) {
			connection.Close()
		}
		s.awaitClose(connection, netconn)
		return
	}

	if err := s.handleRecoveryConn(conn, netconn, config); err != nil {
		_ = conn.Close()
	}
}

// pendingConnection wraps an accepted conn with the server's settings.
func (s *Server) pendingConnection(conn net.Conn) *pendingConnection {
	pending := newPendingConnection(conn, s.registry, s.onNewStream, s.onCloseStream)
	pending.connection.streamQueueSize = s.streamQueueSize
	return pending
}

func (s *Server) handleRecoveryConn(conn net.Conn, netconn Netconn, config connConfig) error {
	req, err := readAttachRequest(conn)
	if err != nil {
		return err
	}

	switch req.mode {
	case attachModeNew:
		return s.attachNew(conn, netconn, config)
	case attachModeResume:
		return s.attachResume(conn, config, req)
	default:
		return rejectAttach(conn, "invalid attach mode")
	}
}

// attachNew starts a new recovery connection.
func (s *Server) attachNew(conn net.Conn, netconn Netconn, config connConfig) error {
	connectionID, err := newRecoveryToken()
	if err != nil {
		return err
	}
	resumeSecret, err := newRecoveryToken()
	if err != nil {
		return err
	}
	connection := s.pendingConnection(conn).connection
	connection.config = config
	connection.recovery = newServerRecoveryState(s.recovery, s, connectionID, resumeSecret)
	s.storeRecoveryConnection(connectionID, connection)
	if err := writeAttachResponse(conn, attachResponse{
		status:       attachStatusOK,
		connectionID: connectionID,
		resumeSecret: resumeSecret,
		negotiated:   s.recovery.negotiated(),
	}); err != nil {
		s.removeRecoveryConnection(connectionID, connection)
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	connection.attachTransport(conn, 0)
	if s.onConnect != nil {
		if err := s.onConnect(netconn); err != nil {
			connection.markClosed()
			return err
		}
	}
	connection.start()
	if !s.trackConn(connection, conn) {
		connection.Close()
	}
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.awaitClose(connection, netconn)
	}()
	return nil
}

// attachResume moves conn to the connection that a resume request names.
func (s *Server) attachResume(conn net.Conn, config connConfig, req attachRequest) error {
	connection, ok := s.lookupRecoveryConnection(req.connectionID)
	if !ok || connection == nil || connection.recovery == nil || connection.closed.Load() {
		return rejectAttach(conn, "connection not found")
	}
	recovery := connection.recovery
	if subtle.ConstantTimeCompare(recovery.resumeSecret[:], req.resumeSecret[:]) != 1 {
		return rejectAttach(conn, "resume secret mismatch")
	}
	if connection.config.codecID != config.codecID {
		return rejectAttach(conn, "codec mismatch")
	}
	if err := writeAttachResponse(conn, attachResponse{
		status:       attachStatusOK,
		connectionID: recovery.connectionID,
		resumeSecret: recovery.resumeSecret,
		lastRecvSeq:  recovery.lastReceived(),
		negotiated:   recovery.negotiated(),
	}); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	connection.attachTransport(conn, req.lastRecvSeq)
	return nil
}

// rejectAttach tells the client that its attach request is refused.
func rejectAttach(conn net.Conn, reason string) error {
	_ = writeAttachResponse(conn, attachResponse{status: attachStatusRejected})
	return ErrResumeRejected{Reason: reason}
}

// awaitClose waits for connection to close, then runs OnDisconnect.
func (s *Server) awaitClose(connection *Connection, netconn Netconn) {
	connection.WaitClosed()
	s.untrackConn(connection)
	if s.onDisconnect != nil {
		_ = s.onDisconnect(netconn)
	}
}

// Shutdown stops the server. Every [Server.Serve] call stops accepting and
// returns [ErrServerClosed], connections still in the handshake are dropped,
// and established connections close as [Connection.Close] closes them:
// messages already queued are delivered, and recovery sessions end instead of
// waiting for the client to reconnect. Shutdown returns once every connection
// has closed and its OnDisconnect callback has returned, or when ctx is done,
// whichever comes first; in the latter case it returns ctx.Err() and the
// connections go on closing. Calling Shutdown more than once is safe.
func (s *Server) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	if !s.stopping {
		s.stopping = true
		if s.stopCh != nil {
			close(s.stopCh)
		}
	}
	listeners := make([]net.Listener, 0, len(s.listeners))
	for listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	handshakes := make([]net.Conn, 0, len(s.handshakes))
	for conn := range s.handshakes {
		handshakes = append(handshakes, conn)
	}
	conns := make([]*Connection, 0, len(s.conns))
	for connection := range s.conns {
		conns = append(conns, connection)
	}
	s.lifecycleMu.Unlock()

	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, conn := range handshakes {
		_ = conn.Close()
	}
	for _, connection := range conns {
		go connection.Close()
	}
	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) isStopping() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.stopping
}

// trackListener registers listener for Shutdown to close. It returns false
// once Shutdown has begun.
func (s *Server) trackListener(listener net.Listener) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return false
	}
	if s.listeners == nil {
		s.listeners = make(map[net.Listener]struct{})
	}
	s.listeners[listener] = struct{}{}
	return true
}

func (s *Server) untrackListener(listener net.Listener) {
	s.lifecycleMu.Lock()
	delete(s.listeners, listener)
	s.lifecycleMu.Unlock()
}

// beginHandshake registers an accepted conn for Shutdown to close and counts
// the goroutine that serves it. It returns false once Shutdown has begun.
func (s *Server) beginHandshake(conn net.Conn) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		return false
	}
	if s.handshakes == nil {
		s.handshakes = make(map[net.Conn]struct{})
	}
	s.handshakes[conn] = struct{}{}
	s.running.Add(1)
	return true
}

// endHandshake forgets conn once it is closed or owned by a connection.
func (s *Server) endHandshake(conn net.Conn) {
	s.lifecycleMu.Lock()
	delete(s.handshakes, conn)
	s.lifecycleMu.Unlock()
}

// trackConn hands conn over to the established connection that now owns it,
// for Shutdown to close. It returns false once Shutdown has begun; the caller
// then closes the connection.
func (s *Server) trackConn(connection *Connection, conn net.Conn) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	delete(s.handshakes, conn)
	if s.stopping {
		return false
	}
	if s.conns == nil {
		s.conns = make(map[*Connection]struct{})
	}
	s.conns[connection] = struct{}{}
	return true
}

func (s *Server) untrackConn(connection *Connection) {
	s.lifecycleMu.Lock()
	delete(s.conns, connection)
	s.lifecycleMu.Unlock()
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

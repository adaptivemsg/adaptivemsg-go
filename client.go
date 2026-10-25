package adaptivemsg

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// Client configures and dials outbound connections. Options are chained
// before calling [Client.Connect] or [Client.ConnectOnce]:
//
//	conn, err := adaptivemsg.NewClient().
//		WithConnectTimeout(5 * time.Second).
//		WithCodecs(adaptivemsg.CodecMsgpackCompact).
//		Connect("tcp://127.0.0.1:9000")
//
// The With methods modify the Client and return it for chaining. Finish
// configuring a Client before sharing it between goroutines; a configured
// Client is safe for concurrent use.
//
// Defaults: codecs [CodecMsgpackCompact, CodecMsgpackMap], max frame 16 MiB,
// no timeouts, recovery disabled.
type Client struct {
	connectTimeout time.Duration
	recvTimeout    time.Duration
	maxFrame       uint32
	codecs         []CodecID
	registry       *registry
	recovery       ClientRecoveryOptions
}

const (
	connectRetryMinBackoff = 100 * time.Millisecond
	connectRetryMaxBackoff = 2 * time.Second
)

// NewClient returns a Client with sensible defaults: both MessagePack codecs
// offered ([CodecMsgpackCompact] first, then [CodecMsgpackMap]), no timeouts,
// a 16 MiB max frame size, and recovery disabled.
func NewClient() *Client {
	return &Client{
		codecs:   []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		maxFrame: defaultMaxFrame,
		registry: newRegistrySnapshot(),
		recovery: defaultClientRecoveryOptions(),
	}
}

// WithConnectTimeout sets how long [Client.Connect] and [OnceConn.SendRecv]
// may take to establish a connection. Until then, failed dials such as
// connection refused are retried, waiting 100ms between attempts and doubling
// up to 2s, so the client can start before its server. Handshake failures are
// returned at once, and a request is never sent twice. With recovery enabled
// the timeout also limits each reconnect attempt. When the timeout is reached
// the call returns [ErrConnectTimeout]. A zero or negative value means a
// single attempt with no time limit.
func (c *Client) WithConnectTimeout(timeout time.Duration) *Client {
	c.connectTimeout = timeout
	return c
}

// WithRecvTimeout sets the initial receive timeout of every stream on
// connections made by this client, which includes waiting for the reply in
// [OnceConn.SendRecv]. [Connection.SetRecvTimeout] and [Stream.SetRecvTimeout]
// change it for a single stream. A zero or negative value means no timeout.
func (c *Client) WithRecvTimeout(timeout time.Duration) *Client {
	c.recvTimeout = timeout
	return c
}

// WithCodecs sets the client's ordered list of preferred codecs. During the
// handshake the server walks this list and selects the first codec it also
// supports, so place the most desirable codec first. If no common codec
// exists the handshake fails with [ErrNoCommonCodec].
func (c *Client) WithCodecs(codecs ...CodecID) *Client {
	c.codecs = append([]CodecID(nil), codecs...)
	return c
}

// WithMaxFrame sets the maximum payload size per frame that this client
// advertises to the peer. The effective limit for the connection is the
// minimum of both sides' values, so raising it also needs
// [Server.WithMaxFrame] on the server. The default is 16 MiB.
func (c *Client) WithMaxFrame(maxFrame uint32) *Client {
	c.maxFrame = maxFrame
	return c
}

// WithRecovery enables the v3 protocol extension that supports transparent
// reconnection and message replay after transient network failures. See
// [ClientRecoveryOptions] for tunables such as backoff intervals and replay
// buffer limits. When recovery is enabled the client will attempt the v3
// handshake first and fall back to v2 if the server does not support it.
func (c *Client) WithRecovery(opts ClientRecoveryOptions) *Client {
	c.recovery = opts.normalized()
	return c
}

// Connect dials addr, performs the protocol handshake (including codec
// negotiation), and returns a live [Connection] ready for messaging.
//
// Supported address formats:
//
//   - "tcp://host:port" — TCP connection
//   - "uds:///path/to/sock" or "unix:///path/to/sock" — Unix domain socket
//   - "host:port" — bare host:port defaults to TCP
//
// On failure Connect returns one of the typed errors: [ErrConnectTimeout] if
// the connect timeout is reached, [ErrNoCommonCodec] if no codec overlap
// exists, or a transport-level error from the operating system.
func (c *Client) Connect(addr string) (*Connection, error) {
	return c.connect(addr, c.recovery.Enable)
}

// ConnectOnce returns a [OnceConn] for exactly one request-reply exchange with
// addr. It does not dial; [OnceConn.SendRecv] connects, exchanges the request
// and reply, and closes the connection. Use [Client.Connect] to send several
// requests to the same server.
func (c *Client) ConnectOnce(addr string) *OnceConn {
	return &OnceConn{client: c, addr: addr}
}

// OnceConn is a connection for exactly one request-reply exchange, created by
// [Client.ConnectOnce]. It never uses recovery, even when [Client.WithRecovery]
// is set: recovery adds a round trip to every connection and does little for
// a single exchange.
type OnceConn struct {
	client *Client
	addr   string
	used   atomic.Bool
}

// SendRecv dials, sends msg on the default stream, waits for a reply of type
// T, and closes the connection. The connect timeout limits connecting and the
// recv timeout limits waiting for the reply. A OnceConn can be used only
// once: every call after the first returns [ErrClosed].
func (o *OnceConn) SendRecv[T any](msg Message) (T, error) {
	var zero T
	if o == nil || o.client == nil {
		return zero, ErrInvalidMessage{Reason: "client is nil"}
	}
	if o.used.Swap(true) {
		return zero, ErrClosed{}
	}
	conn, err := o.client.connect(o.addr, false)
	if err != nil {
		return zero, err
	}
	// Once SendRecv returns nothing is left to deliver, so close at once instead
	// of waiting, as Close does, for a peer that may not be responding.
	defer conn.markClosed()
	return conn.SendRecv[T](msg)
}

func (c *Client) connect(addr string, recovery bool) (*Connection, error) {
	var deadline time.Time
	if c.connectTimeout > 0 {
		deadline = time.Now().Add(c.connectTimeout)
	}
	backoff := connectRetryMinBackoff
	var lastErr error
	for {
		connection, err := c.connectAttempt(addr, recovery, deadline)
		if err == nil {
			return connection, nil
		}
		if lastErr != nil && errors.As(err, &ErrConnectTimeout{}) {
			return nil, fmt.Errorf("%w: %w", ErrConnectTimeout{}, lastErr)
		}
		if deadline.IsZero() || !isDialError(err) {
			return nil, err
		}
		lastErr = err
		time.Sleep(min(backoff, time.Until(deadline)))
		backoff = min(2*backoff, connectRetryMaxBackoff)
	}
}

func (c *Client) connectAttempt(addr string, recovery bool, deadline time.Time) (*Connection, error) {
	versions := []byte{protocolVersionV2}
	if recovery {
		versions = []byte{protocolVersionV3, protocolVersionV2}
	}
	var lastErr error
	for _, version := range versions {
		connection, err := c.connectVersion(addr, version, deadline)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		if version == protocolVersionV3 {
			var unsupported ErrUnsupportedFrameVersion
			if errors.As(err, &unsupported) && unsupported.Version == protocolVersionV2 {
				continue
			}
		}
		return nil, err
	}
	return nil, lastErr
}

func (c *Client) connectVersion(addr string, version byte, deadline time.Time) (*Connection, error) {
	if c.registry == nil {
		c.registry = newRegistrySnapshot()
	}
	conn, err := dialBefore(addr, deadline)
	if err != nil {
		return nil, err
	}
	pending := newPendingConnection(conn, c.registry, nil, nil)
	pending.connection.recvTimeout = c.recvTimeout
	config, err := handshakeClient(conn, c.codecs, c.maxFrame, version)
	if err != nil {
		_ = conn.Close()
		return nil, connectError(err, deadline)
	}
	if config.version != protocolVersionV3 {
		_ = conn.SetDeadline(time.Time{})
		return pending.startWithConfig(config), nil
	}
	connection, err := c.startRecoveryConnection(addr, pending, config)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		return nil, connectError(err, deadline)
	}
	return connection, nil
}

func (c *Client) startRecoveryConnection(addr string, pending *pendingConnection, config connConfig) (*Connection, error) {
	req := attachRequest{mode: attachModeNew}
	if err := writeAttachRequest(pending.connection.conn, req); err != nil {
		_ = pending.connection.conn.Close()
		return nil, err
	}
	resp, err := readAttachResponse(pending.connection.conn)
	if err != nil {
		_ = pending.connection.conn.Close()
		return nil, err
	}
	if resp.status != attachStatusOK {
		_ = pending.connection.conn.Close()
		return nil, ErrResumeRejected{Reason: "initial attach rejected"}
	}

	connection := pending.connection
	connection.config = config
	connection.recovery = newClientRecoveryState(c.recovery, resp.negotiated, addr, c.connectTimeout, resp.connectionID, resp.resumeSecret)
	connection.attachTransport(connection.conn, resp.lastRecvSeq)
	connection.start()
	return connection, nil
}

func (c *Connection) resumeClientTransport() (net.Conn, error) {
	if c == nil || c.recovery == nil {
		return nil, ErrClosed{}
	}
	var deadline time.Time
	if c.recovery.timeout > 0 {
		deadline = time.Now().Add(c.recovery.timeout)
	}
	conn, err := dialBefore(c.recovery.addr, deadline)
	if err != nil {
		return nil, err
	}
	config, err := handshakeClient(conn, []CodecID{c.config.codecID}, c.config.maxFrame, protocolVersionV3)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if config.codecID != c.config.codecID {
		_ = conn.Close()
		return nil, ErrResumeRejected{Reason: "codec changed on resume"}
	}
	req := attachRequest{
		mode:         attachModeResume,
		connectionID: c.recovery.connectionID,
		resumeSecret: c.recovery.resumeSecret,
		lastRecvSeq:  c.recovery.lastReceived(),
	}
	if err := writeAttachRequest(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := readAttachResponse(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.status != attachStatusOK {
		_ = conn.Close()
		return nil, ErrResumeRejected{}
	}
	c.recovery.setNegotiated(resp.negotiated)
	_ = conn.SetDeadline(time.Time{})
	c.attachTransport(conn, resp.lastRecvSeq)
	return conn, nil
}

// dialBefore dials addr and sets a deadline on the connection so that dialing
// and the handshake that follows finish by deadline. A zero deadline means no
// limit.
func dialBefore(addr string, deadline time.Time) (net.Conn, error) {
	var timeout time.Duration
	if !deadline.IsZero() {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return nil, ErrConnectTimeout{}
		}
	}
	conn, err := dialForAddr(addr, timeout)
	if err != nil {
		return nil, connectError(err, deadline)
	}
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
	}
	return conn, nil
}

func connectError(err error, deadline time.Time) error {
	if !deadline.IsZero() && isTimeout(err) {
		return ErrConnectTimeout{}
	}
	return err
}

// isDialError reports whether err means the server could not be reached, as
// opposed to a failed handshake with a server that answered.
func isDialError(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		return false
	}
	var addrErr *net.AddrError
	return !errors.As(err, &addrErr)
}

func dialForAddr(addr string, timeout time.Duration) (net.Conn, error) {
	network, address, err := parseAddr(addr)
	if err != nil {
		return nil, err
	}
	if network == "unix" {
		return dialUDS(address, timeout)
	}
	return dialTCP(address, timeout)
}

func parseAddr(addr string) (string, string, error) {
	if strings.HasPrefix(addr, "tcp://") {
		return "tcp", strings.TrimPrefix(addr, "tcp://"), nil
	}
	if strings.HasPrefix(addr, "uds://") {
		return "unix", strings.TrimPrefix(addr, "uds://"), nil
	}
	if strings.HasPrefix(addr, "unix://") {
		return "unix", strings.TrimPrefix(addr, "unix://"), nil
	}
	return "tcp", addr, nil
}

func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

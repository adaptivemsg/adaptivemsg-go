package adaptivemsg

import (
	"errors"
	"net"
	"strings"
	"time"
)

// Client configures and dials outbound connections. It uses a builder pattern
// so options can be chained before calling [Client.Connect]:
//
//	conn, err := adaptivemsg.NewClient().
//		WithTimeout(5 * time.Second).
//		WithCodecs(adaptivemsg.CodecMsgpackCompact).
//		Connect("tcp://127.0.0.1:9000")
//
// Defaults: codecs [CodecMsgpackCompact, CodecMsgpackMap], max frame
// unlimited (peer's limit applies), recovery disabled.
type Client struct {
	timeout  time.Duration
	maxFrame uint32
	codecs   []CodecID
	registry *registry
	recovery ClientRecoveryOptions
}

// NewClient returns a Client with sensible defaults: both MessagePack codecs
// offered ([CodecMsgpackCompact] first, then [CodecMsgpackMap]), no dial
// timeout, unlimited max frame size, and recovery disabled.
func NewClient() *Client {
	return &Client{
		codecs:   []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		maxFrame: defaultMaxFrame,
		registry: newRegistrySnapshot(),
		recovery: defaultClientRecoveryOptions(),
	}
}

// WithTimeout sets the timeout applied to the TCP dial (or Unix connect) and
// any subsequent TLS handshake. A zero or negative value disables the timeout,
// allowing the connection attempt to block indefinitely.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	c.timeout = timeout
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
// minimum of both sides' values. The default is math.MaxUint32 (no
// client-side limit), so the server's value takes precedence.
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
// the dial or handshake exceeds the configured timeout, [ErrNoCommonCodec] if
// no codec overlap exists, or a transport-level error from the operating system.
func (c *Client) Connect(addr string) (*Connection, error) {
	versions := []byte{protocolVersionV2}
	if c.recovery.Enable {
		versions = []byte{protocolVersionV3, protocolVersionV2}
	}
	var lastErr error
	for _, version := range versions {
		connection, err := c.connectVersion(addr, version)
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

func (c *Client) connectVersion(addr string, version byte) (*Connection, error) {
	if c.registry == nil {
		c.registry = newRegistrySnapshot()
	}
	conn, err := c.dial(addr)
	if err != nil {
		if c.timeout > 0 && isTimeout(err) {
			return nil, ErrConnectTimeout{}
		}
		return nil, err
	}
	deadlineSet := c.timeout > 0
	if deadlineSet {
		_ = conn.SetDeadline(time.Now().Add(c.timeout))
	}
	pending := newPendingConnection(conn, c.registry, nil, nil)
	config, err := handshakeClient(conn, c.codecs, c.maxFrame, version)
	if err != nil {
		_ = conn.Close()
		if c.timeout > 0 && isTimeout(err) {
			return nil, ErrConnectTimeout{}
		}
		return nil, err
	}
	if config.version != protocolVersionV3 {
		if deadlineSet {
			_ = conn.SetDeadline(time.Time{})
		}
		return pending.startWithConfig(config), nil
	}
	connection, err := c.startRecoveryConnection(addr, pending, config)
	if deadlineSet {
		_ = conn.SetDeadline(time.Time{})
	}
	return connection, err
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
	connection.recovery = newClientRecoveryState(c.recovery, resp.negotiated, addr, c.timeout, resp.connectionID, resp.resumeSecret)
	connection.attachTransport(connection.conn, resp.lastRecvSeq)
	connection.start()
	return connection, nil
}

func (c *Connection) resumeClientTransport() (net.Conn, error) {
	if c == nil || c.recovery == nil {
		return nil, ErrClosed{}
	}
	conn, err := dialForAddr(c.recovery.addr, c.recovery.timeout)
	if err != nil {
		return nil, err
	}
	deadlineSet := c.recovery.timeout > 0
	if deadlineSet {
		_ = conn.SetDeadline(time.Now().Add(c.recovery.timeout))
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
	if deadlineSet {
		_ = conn.SetDeadline(time.Time{})
	}
	c.attachTransport(conn, resp.lastRecvSeq)
	return conn, nil
}

func (c *Client) dial(addr string) (net.Conn, error) {
	return dialForAddr(addr, c.timeout)
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

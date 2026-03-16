package adaptivemsg

import (
	"errors"
	"net"
	"strings"
	"time"
)

// Client configures outbound connections.
type Client struct {
	timeout  time.Duration
	maxFrame uint32
	codecs   []CodecID
	registry *registry
}

// NewClient returns a client with default settings.
func NewClient() *Client {
	return &Client{
		codecs:   []CodecID{CodecMsgpackCompact, CodecMsgpackMap},
		maxFrame: defaultMaxFrame,
		registry: newRegistrySnapshot(),
	}
}

// WithTimeout sets the dial timeout and returns the client.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	c.timeout = timeout
	return c
}

// WithCodecs sets the preferred codec list; the server picks the first common codec in this order.
func (c *Client) WithCodecs(codecs ...CodecID) *Client {
	c.codecs = append([]CodecID(nil), codecs...)
	return c
}

// WithMaxFrame sets the maximum frame size advertised to the peer.
func (c *Client) WithMaxFrame(maxFrame uint32) *Client {
	c.maxFrame = maxFrame
	return c
}

// Connect dials the address and returns a live Connection.
func (c *Client) Connect(addr string) (*Connection, error) {
	if c.registry == nil {
		c.registry = newRegistrySnapshot()
	}
	network, address, err := parseAddr(addr)
	if err != nil {
		return nil, err
	}
	var conn net.Conn
	if network == "unix" {
		conn, err = dialUDS(address, c.timeout)
	} else {
		conn, err = dialTCP(address, c.timeout)
	}
	if err != nil {
		if c.timeout > 0 && isTimeout(err) {
			return nil, ErrConnectTimeout{}
		}
		return nil, err
	}
	if c.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.timeout))
	}
	pending := newPendingConnection(conn, c.registry, nil, nil)
	connection, err := pending.startClient(c.codecs, c.maxFrame)
	if c.timeout > 0 {
		_ = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		if c.timeout > 0 && isTimeout(err) {
			return nil, ErrConnectTimeout{}
		}
		return nil, err
	}
	return connection, nil
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

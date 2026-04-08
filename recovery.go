package adaptivemsg

import "time"

// ClientRecoveryOptions controls client-side recovery behavior for automatic
// reconnect and replay. When Enable is true, the client negotiates the v3
// protocol with the server, enabling transparent reconnection and message
// replay after transient network failures.
type ClientRecoveryOptions struct {
	// Enable activates recovery mode. Default: false.
	Enable bool
	// ReconnectMinBackoff is the minimum backoff duration between reconnect
	// attempts. Default: 100ms.
	ReconnectMinBackoff time.Duration
	// ReconnectMaxBackoff is the maximum backoff duration between reconnect
	// attempts. Default: 2s.
	ReconnectMaxBackoff time.Duration
	// MaxReplayBytes is the maximum number of bytes buffered for replay of
	// unacknowledged messages. Default: 8 MiB.
	MaxReplayBytes int64
}

// ServerRecoveryOptions controls server-side recovery behavior for connection
// retention and ACK policy. When Enable is true, the server supports the v3
// protocol with attach/resume semantics. ACK and heartbeat fields are
// authoritative for the connection and are sent to the client during
// attach/resume.
type ServerRecoveryOptions struct {
	// Enable activates recovery mode. Default: false.
	Enable bool
	// DetachedTTL is how long the server retains a detached connection's state
	// before discarding it. Default: 30s.
	DetachedTTL time.Duration
	// MaxReplayBytes is the maximum number of bytes buffered for replay of
	// unacknowledged messages. Default: 8 MiB.
	MaxReplayBytes int64
	// AckEvery sends a cumulative ACK every N data frames. Default: 64.
	AckEvery uint32
	// AckDelay is the delay before flushing a pending ACK. Default: 20ms.
	AckDelay time.Duration
	// HeartbeatInterval is the interval between heartbeat pings when the
	// connection is idle. Default: 30s.
	HeartbeatInterval time.Duration
	// HeartbeatTimeout closes the connection if no inbound frame is received
	// within this duration. Must be ≥ 2×HeartbeatInterval. Default: 90s.
	HeartbeatTimeout time.Duration
}

type negotiatedRecoveryOptions struct {
	AckEvery          uint32
	AckDelay          time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
}

func defaultClientRecoveryOptions() ClientRecoveryOptions {
	return ClientRecoveryOptions{
		ReconnectMinBackoff: 100 * time.Millisecond,
		ReconnectMaxBackoff: 2 * time.Second,
		MaxReplayBytes:      8 << 20,
	}
}

func defaultServerRecoveryOptions() ServerRecoveryOptions {
	return ServerRecoveryOptions{
		DetachedTTL:       30 * time.Second,
		MaxReplayBytes:    8 << 20,
		AckEvery:          64,
		AckDelay:          20 * time.Millisecond,
		HeartbeatInterval: 30 * time.Second,
		HeartbeatTimeout:  90 * time.Second,
	}
}

func (o ClientRecoveryOptions) normalized() ClientRecoveryOptions {
	def := defaultClientRecoveryOptions()
	if o.ReconnectMinBackoff <= 0 {
		o.ReconnectMinBackoff = def.ReconnectMinBackoff
	}
	if o.ReconnectMaxBackoff <= 0 {
		o.ReconnectMaxBackoff = def.ReconnectMaxBackoff
	}
	if o.ReconnectMaxBackoff < o.ReconnectMinBackoff {
		o.ReconnectMaxBackoff = o.ReconnectMinBackoff
	}
	if o.MaxReplayBytes <= 0 {
		o.MaxReplayBytes = def.MaxReplayBytes
	}
	return o
}

func (o ServerRecoveryOptions) normalized() ServerRecoveryOptions {
	def := defaultServerRecoveryOptions()
	if o.DetachedTTL <= 0 {
		o.DetachedTTL = def.DetachedTTL
	}
	if o.MaxReplayBytes <= 0 {
		o.MaxReplayBytes = def.MaxReplayBytes
	}
	if o.AckEvery == 0 {
		o.AckEvery = def.AckEvery
	}
	if o.AckDelay <= 0 {
		o.AckDelay = def.AckDelay
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = def.HeartbeatInterval
	}
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = def.HeartbeatTimeout
	}
	minTimeout := 2 * o.HeartbeatInterval
	if o.HeartbeatTimeout < minTimeout {
		o.HeartbeatTimeout = minTimeout
	}
	return o
}

func (o ServerRecoveryOptions) negotiated() negotiatedRecoveryOptions {
	normalized := o.normalized()
	return negotiatedRecoveryOptions{
		AckEvery:          normalized.AckEvery,
		AckDelay:          normalized.AckDelay,
		HeartbeatInterval: normalized.HeartbeatInterval,
		HeartbeatTimeout:  normalized.HeartbeatTimeout,
	}
}

func (o negotiatedRecoveryOptions) normalized() negotiatedRecoveryOptions {
	def := defaultServerRecoveryOptions().negotiated()
	if o.AckEvery == 0 {
		o.AckEvery = def.AckEvery
	}
	if o.AckDelay <= 0 {
		o.AckDelay = def.AckDelay
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = def.HeartbeatInterval
	}
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = def.HeartbeatTimeout
	}
	minTimeout := 2 * o.HeartbeatInterval
	if o.HeartbeatTimeout < minTimeout {
		o.HeartbeatTimeout = minTimeout
	}
	return o
}

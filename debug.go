package adaptivemsg

import (
	"encoding/hex"
	"sync/atomic"
	"time"
)

// DebugFailureCode is a string code identifying the subsystem and failure mode
// for diagnostic purposes. Values follow the pattern "subsystem.failure_kind".
type DebugFailureCode string

const (
	// DebugFailureNone indicates no failure has been recorded.
	DebugFailureNone DebugFailureCode = ""
	// DebugFailureStreamRecvTimeout indicates a stream receive timed out.
	DebugFailureStreamRecvTimeout DebugFailureCode = "stream.recv_timeout"
	// DebugFailureStreamEncode indicates message encoding failed on a stream.
	DebugFailureStreamEncode DebugFailureCode = "stream.encode"
	// DebugFailureStreamEnqueue indicates a failure to enqueue a frame for sending.
	DebugFailureStreamEnqueue DebugFailureCode = "stream.enqueue"
	// DebugFailureStreamProtocol indicates a protocol error on a stream (e.g. unknown message).
	DebugFailureStreamProtocol DebugFailureCode = "stream.protocol"
	// DebugFailureStreamProtocolReplySend indicates a failure to send a protocol error reply.
	DebugFailureStreamProtocolReplySend DebugFailureCode = "stream.protocol_reply_send"
	// DebugFailureStreamDecode indicates message decoding failed on a stream.
	DebugFailureStreamDecode DebugFailureCode = "stream.decode"
	// DebugFailureHandler indicates a handler returned an error.
	DebugFailureHandler DebugFailureCode = "handler.error"
	// DebugFailureWriterLoop indicates the connection writer goroutine failed.
	DebugFailureWriterLoop DebugFailureCode = "connection.writer"
	// DebugFailureReaderLoop indicates the connection reader goroutine failed.
	DebugFailureReaderLoop DebugFailureCode = "connection.reader"
	// DebugFailureReaderEnqueue indicates the reader failed to enqueue a payload to a stream.
	DebugFailureReaderEnqueue DebugFailureCode = "connection.reader_enqueue"
	// DebugFailureReconnectResume indicates a recovery resume attempt failed.
	DebugFailureReconnectResume DebugFailureCode = "recovery.resume"
	// DebugFailureReconnectTerminal indicates recovery gave up after exhausting retries.
	DebugFailureReconnectTerminal DebugFailureCode = "recovery.reconnect_terminal"
	// DebugFailureRecoveryAckWrite indicates a failure to write an ACK control frame.
	DebugFailureRecoveryAckWrite DebugFailureCode = "recovery.ack_write"
	// DebugFailureRecoveryResumeWrite indicates a failure to write a resume frame.
	DebugFailureRecoveryResumeWrite DebugFailureCode = "recovery.resume_write"
	// DebugFailureRecoveryLiveWrite indicates a failure to write a live data frame during recovery.
	DebugFailureRecoveryLiveWrite DebugFailureCode = "recovery.live_write"
	// DebugFailureRecoveryPingWrite indicates a failure to write a heartbeat ping.
	DebugFailureRecoveryPingWrite DebugFailureCode = "recovery.ping_write"
	// DebugFailureRecoveryRead indicates an error reading during recovery.
	DebugFailureRecoveryRead DebugFailureCode = "recovery.read"
	// DebugFailureRecoveryControl indicates an error processing a recovery control frame.
	DebugFailureRecoveryControl DebugFailureCode = "recovery.control"
	// DebugFailureRecoveryData indicates an error processing a recovery data frame.
	DebugFailureRecoveryData DebugFailureCode = "recovery.data"
)

type connectionDebugCounters struct {
	streamsOpened                 atomic.Uint64
	streamsClosed                 atomic.Uint64
	dataMessagesSent              atomic.Uint64
	dataMessagesReceived          atomic.Uint64
	framesWritten                 atomic.Uint64
	framesRead                    atomic.Uint64
	bytesWritten                  atomic.Uint64
	bytesRead                     atomic.Uint64
	controlFramesWritten          atomic.Uint64
	controlFramesRead             atomic.Uint64
	protocolErrors                atomic.Uint64
	protocolErrorReplySendFailure atomic.Uint64
	remoteErrors                  atomic.Uint64
	decodeErrors                  atomic.Uint64
	handlerCalls                  atomic.Uint64
	handlerErrors                 atomic.Uint64
	reconnectAttempts             atomic.Uint64
	reconnectSuccesses            atomic.Uint64
	reconnectFailures             atomic.Uint64
	transportAttaches             atomic.Uint64
	transportDetaches             atomic.Uint64
	lastFailureCodeValue          atomic.Value
	lastFailure                   atomic.Value
	lastFailureUnixNanos          atomic.Int64
}

type streamDebugCounters struct {
	dataMessagesSent              atomic.Uint64
	dataMessagesReceived          atomic.Uint64
	protocolErrors                atomic.Uint64
	protocolErrorReplySendFailure atomic.Uint64
	remoteErrors                  atomic.Uint64
	decodeErrors                  atomic.Uint64
	handlerCalls                  atomic.Uint64
	handlerErrors                 atomic.Uint64
	lastFailureCodeValue          atomic.Value
	lastFailure                   atomic.Value
	lastFailureUnixNanos          atomic.Int64
}

// ConnectionCounters holds point-in-time counters for a connection.
// All values are cumulative since the connection was created.
type ConnectionCounters struct {
	// StreamsOpened is the total number of streams opened.
	StreamsOpened uint64
	// StreamsClosed is the total number of streams closed.
	StreamsClosed uint64
	// DataMessagesSent is the total number of application-level messages sent.
	DataMessagesSent uint64
	// DataMessagesReceived is the total number of application-level messages received.
	DataMessagesReceived uint64
	// FramesWritten is the total number of wire frames written.
	FramesWritten uint64
	// FramesRead is the total number of wire frames read.
	FramesRead uint64
	// BytesWritten is the total number of bytes written to the transport.
	BytesWritten uint64
	// BytesRead is the total number of bytes read from the transport.
	BytesRead uint64
	// ControlFramesWritten is the total number of control frames written.
	ControlFramesWritten uint64
	// ControlFramesRead is the total number of control frames read.
	ControlFramesRead uint64
	// ProtocolErrors is the total number of protocol errors detected.
	ProtocolErrors uint64
	// ProtocolErrorReplySendFailure is how many times sending a protocol error reply failed.
	ProtocolErrorReplySendFailure uint64
	// RemoteErrors is the total number of remote ErrorReply messages received.
	RemoteErrors uint64
	// DecodeErrors is the total number of message decode failures.
	DecodeErrors uint64
	// HandlerCalls is the total number of handler invocations.
	HandlerCalls uint64
	// HandlerErrors is the total number of handler invocations that returned an error.
	HandlerErrors uint64
	// ReconnectAttempts is the total number of recovery reconnect attempts.
	ReconnectAttempts uint64
	// ReconnectSuccesses is the total number of successful recovery reconnects.
	ReconnectSuccesses uint64
	// ReconnectFailures is the total number of failed recovery reconnect attempts.
	ReconnectFailures uint64
	// TransportAttaches is the total number of times a transport was attached.
	TransportAttaches uint64
	// TransportDetaches is the total number of times a transport was detached.
	TransportDetaches uint64
}

// StreamCounters holds point-in-time counters for a single stream.
// All values are cumulative since the stream was opened.
type StreamCounters struct {
	// DataMessagesSent is the total number of application-level messages sent on this stream.
	DataMessagesSent uint64
	// DataMessagesReceived is the total number of application-level messages received on this stream.
	DataMessagesReceived uint64
	// ProtocolErrors is the total number of protocol errors detected on this stream.
	ProtocolErrors uint64
	// ProtocolErrorReplySendFailure is how many times sending a protocol error reply failed on this stream.
	ProtocolErrorReplySendFailure uint64
	// RemoteErrors is the total number of remote ErrorReply messages received on this stream.
	RemoteErrors uint64
	// DecodeErrors is the total number of message decode failures on this stream.
	DecodeErrors uint64
	// HandlerCalls is the total number of handler invocations on this stream.
	HandlerCalls uint64
	// HandlerErrors is the total number of handler invocations that returned an error on this stream.
	HandlerErrors uint64
}

// StreamDebugState is a debug snapshot of a stream's state at a point in time.
type StreamDebugState struct {
	// ID is the stream's numeric identifier within the connection.
	ID uint32
	// Closed is true if the stream has been closed.
	Closed bool
	// LastFailureCode is the code of the most recent failure on this stream.
	LastFailureCode DebugFailureCode
	// LastFailure is the human-readable description of the most recent failure.
	LastFailure string
	// LastFailureAt is the timestamp of the most recent failure.
	LastFailureAt time.Time
	// RecvTimeout is the stream's current receive timeout duration.
	RecvTimeout time.Duration
	// InboxDepth is the number of decoded messages waiting in the stream's inbox.
	InboxDepth int
	// IncomingDepth is the number of raw frames waiting to be decoded.
	IncomingDepth int
	// HandlerQDepth is the number of messages queued for handler dispatch.
	HandlerQDepth int
	// Counters holds the cumulative stream counters.
	Counters StreamCounters
}

// RecoveryDebugState is a debug snapshot of the recovery subsystem's state
// at a point in time.
type RecoveryDebugState struct {
	// Role is "client" or "server", indicating which recovery role this connection has.
	Role string
	// ConnectionID is the hex-encoded recovery connection identifier.
	ConnectionID string
	// TransportAttached is true if a live transport is currently connected.
	TransportAttached bool
	// TransportGen is the generation counter for transport attach/detach cycles.
	TransportGen uint64
	// ReconnectActive is true if a reconnect attempt is currently in progress.
	ReconnectActive bool
	// LastRecvSeq is the sequence number of the last received data frame.
	LastRecvSeq uint64
	// LastAckedSeq is the sequence number of the last acknowledged data frame.
	LastAckedSeq uint64
	// AckPending is the number of received frames not yet acknowledged.
	AckPending uint32
	// AckDue is true if an ACK is pending and should be sent soon.
	AckDue bool
	// AckEvery is the negotiated ACK frequency (every N data frames).
	AckEvery uint32
	// AckDelay is the negotiated delay before flushing a pending ACK.
	AckDelay time.Duration
	// HeartbeatInterval is the negotiated interval between heartbeat pings.
	HeartbeatInterval time.Duration
	// HeartbeatTimeout is the negotiated inactivity timeout for the connection.
	HeartbeatTimeout time.Duration
	// ReplayQueued is the number of frames currently buffered for replay.
	ReplayQueued int
	// ReplayBytes is the total size in bytes of frames buffered for replay.
	ReplayBytes int64
	// LiveQueueDepth is the number of frames in the live send queue.
	LiveQueueDepth int
	// ResumeQueueDepth is the number of frames queued for resume replay.
	ResumeQueueDepth int
}

// ConnectionDebugState is a debug snapshot of a connection at a point in time,
// including counters, stream states, and recovery state.
type ConnectionDebugState struct {
	// Closed is true if the connection has been closed.
	Closed bool
	// LastFailureCode is the code of the most recent failure on this connection.
	LastFailureCode DebugFailureCode
	// LastFailure is the human-readable description of the most recent failure.
	LastFailure string
	// LastFailureAt is the timestamp of the most recent failure.
	LastFailureAt time.Time
	// Protocol is the negotiated protocol version byte.
	Protocol byte
	// CodecID is the negotiated codec identifier.
	CodecID CodecID
	// CodecName is the human-readable name of the negotiated codec.
	CodecName string
	// MaxFrame is the negotiated maximum frame size in bytes.
	MaxFrame uint32
	// StreamCount is the current number of streams (open and closing).
	StreamCount int
	// NextSendSeq is the next sequence number to be assigned to an outbound frame.
	NextSendSeq uint64
	// Counters holds the cumulative connection counters.
	Counters ConnectionCounters
	// Streams contains a snapshot of each stream's debug state.
	Streams []StreamDebugState
	// Recovery contains the recovery subsystem state, or nil if recovery is not enabled.
	Recovery *RecoveryDebugState
}

// DebugState returns a point-in-time snapshot of the connection's debug
// counters, stream states, and recovery state.
func (c *Connection) DebugState() ConnectionDebugState {
	if c == nil {
		return ConnectionDebugState{}
	}

	c.streamsMu.Lock()
	streams := make([]StreamDebugState, 0, len(c.streams))
	for _, streamCtx := range c.streams {
		if streamCtx == nil || streamCtx.stream == nil || streamCtx.stream.core == nil {
			continue
		}
		streams = append(streams, streamCtx.stream.core.debugState())
	}
	streamCount := len(c.streams)
	c.streamsMu.Unlock()

	state := ConnectionDebugState{
		Closed:          c.closed.Load(),
		LastFailureCode: c.debug.lastFailureCode(),
		LastFailure:     c.debug.lastFailureReason(),
		LastFailureAt:   c.debug.lastFailureTime(),
		Protocol:        c.config.version,
		CodecID:         c.config.codecID,
		CodecName:       c.config.codecID.String(),
		MaxFrame:        c.config.maxFrame,
		StreamCount:     streamCount,
		NextSendSeq:     c.nextSendSeq.Load(),
		Counters:        c.debug.snapshot(),
		Streams:         streams,
	}
	if c.recovery != nil {
		recovery := c.recovery.debugState(c)
		state.Recovery = &recovery
	}
	return state
}

// DebugState returns a point-in-time snapshot of this stream's debug
// counters and state.
func (s *Stream[T]) DebugState() StreamDebugState {
	if s == nil || s.core == nil {
		return StreamDebugState{}
	}
	return s.core.debugState()
}

func (d *connectionDebugCounters) snapshot() ConnectionCounters {
	if d == nil {
		return ConnectionCounters{}
	}
	return ConnectionCounters{
		StreamsOpened:                 d.streamsOpened.Load(),
		StreamsClosed:                 d.streamsClosed.Load(),
		DataMessagesSent:              d.dataMessagesSent.Load(),
		DataMessagesReceived:          d.dataMessagesReceived.Load(),
		FramesWritten:                 d.framesWritten.Load(),
		FramesRead:                    d.framesRead.Load(),
		BytesWritten:                  d.bytesWritten.Load(),
		BytesRead:                     d.bytesRead.Load(),
		ControlFramesWritten:          d.controlFramesWritten.Load(),
		ControlFramesRead:             d.controlFramesRead.Load(),
		ProtocolErrors:                d.protocolErrors.Load(),
		ProtocolErrorReplySendFailure: d.protocolErrorReplySendFailure.Load(),
		RemoteErrors:                  d.remoteErrors.Load(),
		DecodeErrors:                  d.decodeErrors.Load(),
		HandlerCalls:                  d.handlerCalls.Load(),
		HandlerErrors:                 d.handlerErrors.Load(),
		ReconnectAttempts:             d.reconnectAttempts.Load(),
		ReconnectSuccesses:            d.reconnectSuccesses.Load(),
		ReconnectFailures:             d.reconnectFailures.Load(),
		TransportAttaches:             d.transportAttaches.Load(),
		TransportDetaches:             d.transportDetaches.Load(),
	}
}

func (d *streamDebugCounters) snapshot() StreamCounters {
	if d == nil {
		return StreamCounters{}
	}
	return StreamCounters{
		DataMessagesSent:              d.dataMessagesSent.Load(),
		DataMessagesReceived:          d.dataMessagesReceived.Load(),
		ProtocolErrors:                d.protocolErrors.Load(),
		ProtocolErrorReplySendFailure: d.protocolErrorReplySendFailure.Load(),
		RemoteErrors:                  d.remoteErrors.Load(),
		DecodeErrors:                  d.decodeErrors.Load(),
		HandlerCalls:                  d.handlerCalls.Load(),
		HandlerErrors:                 d.handlerErrors.Load(),
	}
}

func (s *streamCore) debugState() StreamDebugState {
	if s == nil {
		return StreamDebugState{}
	}
	handlerDepth := 0
	if s.handlerCh != nil {
		handlerDepth = len(s.handlerCh)
	}
	return StreamDebugState{
		ID:              s.id,
		Closed:          s.closed.Load(),
		LastFailureCode: s.debug.lastFailureCode(),
		LastFailure:     s.debug.lastFailureReason(),
		LastFailureAt:   s.debug.lastFailureTime(),
		RecvTimeout:     time.Duration(s.recvTimeoutNanos.Load()),
		InboxDepth:      len(s.inbox),
		IncomingDepth:   len(s.incoming),
		HandlerQDepth:   handlerDepth,
		Counters:        s.debug.snapshot(),
	}
}

func (r *recoveryState) debugState(c *Connection) RecoveryDebugState {
	if r == nil {
		return RecoveryDebugState{}
	}
	transportAttached := false
	transportGen := uint64(0)
	if c != nil {
		c.initTransportState()
		c.transportMu.Lock()
		transportAttached = c.conn != nil
		transportGen = c.transportGen
		c.transportMu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return RecoveryDebugState{
		Role:              r.role.String(),
		ConnectionID:      hex.EncodeToString(r.connectionID[:]),
		TransportAttached: transportAttached,
		TransportGen:      transportGen,
		ReconnectActive:   r.reconnectActive.Load(),
		LastRecvSeq:       r.lastRecvSeq,
		LastAckedSeq:      r.replay.lastAckedSeq(),
		AckPending:        r.ackPending,
		AckDue:            r.ackDue,
		AckEvery:          r.ackEvery.Load(),
		AckDelay:          time.Duration(r.ackDelayNanos.Load()),
		HeartbeatInterval: time.Duration(r.heartbeatIntv.Load()),
		HeartbeatTimeout:  time.Duration(r.heartbeatTO.Load()),
		ReplayQueued:      r.replay.count(),
		ReplayBytes:       r.replay.bytesUsedLocked(),
		LiveQueueDepth:    0,
		ResumeQueueDepth:  r.resumeQueue.len(),
	}
}

func (d *connectionDebugCounters) noteFailure(code DebugFailureCode, reason string) {
	if d == nil || (code == DebugFailureNone && reason == "") {
		return
	}
	if code != DebugFailureNone {
		d.lastFailureCodeValue.Store(string(code))
	}
	d.lastFailure.Store(reason)
	d.lastFailureUnixNanos.Store(time.Now().UnixNano())
}

func (d *connectionDebugCounters) lastFailureCode() DebugFailureCode {
	if d == nil {
		return DebugFailureNone
	}
	v := d.lastFailureCodeValue.Load()
	if v == nil {
		return DebugFailureNone
	}
	code, _ := v.(string)
	return DebugFailureCode(code)
}

func (d *connectionDebugCounters) lastFailureReason() string {
	if d == nil {
		return ""
	}
	v := d.lastFailure.Load()
	if v == nil {
		return ""
	}
	reason, _ := v.(string)
	return reason
}

func (d *connectionDebugCounters) lastFailureTime() time.Time {
	if d == nil {
		return time.Time{}
	}
	ns := d.lastFailureUnixNanos.Load()
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (d *streamDebugCounters) noteFailure(code DebugFailureCode, reason string) {
	if d == nil || (code == DebugFailureNone && reason == "") {
		return
	}
	if code != DebugFailureNone {
		d.lastFailureCodeValue.Store(string(code))
	}
	d.lastFailure.Store(reason)
	d.lastFailureUnixNanos.Store(time.Now().UnixNano())
}

func (d *streamDebugCounters) lastFailureCode() DebugFailureCode {
	if d == nil {
		return DebugFailureNone
	}
	v := d.lastFailureCodeValue.Load()
	if v == nil {
		return DebugFailureNone
	}
	code, _ := v.(string)
	return DebugFailureCode(code)
}

func (d *streamDebugCounters) lastFailureReason() string {
	if d == nil {
		return ""
	}
	v := d.lastFailure.Load()
	if v == nil {
		return ""
	}
	reason, _ := v.(string)
	return reason
}

func (d *streamDebugCounters) lastFailureTime() time.Time {
	if d == nil {
		return time.Time{}
	}
	ns := d.lastFailureUnixNanos.Load()
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (r recoveryRole) String() string {
	switch r {
	case recoveryRoleClient:
		return "client"
	case recoveryRoleServer:
		return "server"
	default:
		return "unknown"
	}
}

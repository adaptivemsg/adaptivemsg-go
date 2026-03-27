package adaptivemsg

import (
	"encoding/hex"
	"sync/atomic"
	"time"
)

type DebugFailureCode string

const (
	DebugFailureNone                    DebugFailureCode = ""
	DebugFailureStreamRecvTimeout       DebugFailureCode = "stream.recv_timeout"
	DebugFailureStreamEncode            DebugFailureCode = "stream.encode"
	DebugFailureStreamEnqueue           DebugFailureCode = "stream.enqueue"
	DebugFailureStreamProtocol          DebugFailureCode = "stream.protocol"
	DebugFailureStreamProtocolReplySend DebugFailureCode = "stream.protocol_reply_send"
	DebugFailureStreamDecode            DebugFailureCode = "stream.decode"
	DebugFailureHandler                 DebugFailureCode = "handler.error"
	DebugFailureWriterLoop              DebugFailureCode = "connection.writer"
	DebugFailureReaderLoop              DebugFailureCode = "connection.reader"
	DebugFailureReaderEnqueue           DebugFailureCode = "connection.reader_enqueue"
	DebugFailureReconnectResume         DebugFailureCode = "recovery.resume"
	DebugFailureReconnectTerminal       DebugFailureCode = "recovery.reconnect_terminal"
	DebugFailureRecoveryAckWrite        DebugFailureCode = "recovery.ack_write"
	DebugFailureRecoveryResumeWrite     DebugFailureCode = "recovery.resume_write"
	DebugFailureRecoveryLiveWrite       DebugFailureCode = "recovery.live_write"
	DebugFailureRecoveryPingWrite       DebugFailureCode = "recovery.ping_write"
	DebugFailureRecoveryRead            DebugFailureCode = "recovery.read"
	DebugFailureRecoveryControl         DebugFailureCode = "recovery.control"
	DebugFailureRecoveryData            DebugFailureCode = "recovery.data"
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

type ConnectionCounters struct {
	StreamsOpened                 uint64
	StreamsClosed                 uint64
	DataMessagesSent              uint64
	DataMessagesReceived          uint64
	FramesWritten                 uint64
	FramesRead                    uint64
	BytesWritten                  uint64
	BytesRead                     uint64
	ControlFramesWritten          uint64
	ControlFramesRead             uint64
	ProtocolErrors                uint64
	ProtocolErrorReplySendFailure uint64
	RemoteErrors                  uint64
	DecodeErrors                  uint64
	HandlerCalls                  uint64
	HandlerErrors                 uint64
	ReconnectAttempts             uint64
	ReconnectSuccesses            uint64
	ReconnectFailures             uint64
	TransportAttaches             uint64
	TransportDetaches             uint64
}

type StreamCounters struct {
	DataMessagesSent              uint64
	DataMessagesReceived          uint64
	ProtocolErrors                uint64
	ProtocolErrorReplySendFailure uint64
	RemoteErrors                  uint64
	DecodeErrors                  uint64
	HandlerCalls                  uint64
	HandlerErrors                 uint64
}

type StreamDebugState struct {
	ID              uint32
	Closed          bool
	LastFailureCode DebugFailureCode
	LastFailure     string
	LastFailureAt   time.Time
	RecvTimeout     time.Duration
	InboxDepth      int
	IncomingDepth   int
	HandlerQDepth   int
	Counters        StreamCounters
}

type RecoveryDebugState struct {
	Role              string
	ConnectionID      string
	TransportAttached bool
	TransportGen      uint64
	ReconnectActive   bool
	LastRecvSeq       uint64
	LastAckedSeq      uint64
	AckPending        uint32
	AckDue            bool
	AckEvery          uint32
	AckDelay          time.Duration
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
	ReplayQueued      int
	ReplayBytes       int64
	LiveQueueDepth    int
	ResumeQueueDepth  int
}

type ConnectionDebugState struct {
	Closed          bool
	LastFailureCode DebugFailureCode
	LastFailure     string
	LastFailureAt   time.Time
	Protocol        byte
	CodecID         CodecID
	CodecName       string
	MaxFrame        uint32
	StreamCount     int
	NextSendSeq     uint64
	Counters        ConnectionCounters
	Streams         []StreamDebugState
	Recovery        *RecoveryDebugState
}

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
		LiveQueueDepth:    r.liveQueue.len(),
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

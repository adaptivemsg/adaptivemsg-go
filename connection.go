package adaptivemsg

import (
	"bufio"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	streamQueueSize = 1024
	defaultStreamID = 0
	// maxStreamID is the last ID NewStream hands out; the only larger value is
	// controlStreamID.
	maxStreamID = controlStreamID - 1
)

type connConfig struct {
	version  byte
	codecID  CodecID
	codec    CodecImpl
	maxFrame uint32
}

type outboundFrame struct {
	streamID uint32
	seq      uint64
	flags    FrameFlags
	payload  []byte
	queued   chan error
}

type handlerJob struct {
	handler handlerFunc
	msg     Message
}

// Connection is a live, negotiated session between two peers. It is obtained
// from [Client.Connect] on the client side or passed to [Server] callbacks on
// the server side.
//
// A Connection doubles as the default stream (stream 0): the [Connection.Send],
// [Connection.SendRecv], [Connection.Recv], and [Connection.PeekWire] methods
// all operate on stream 0. For multiplexed messaging, create additional streams
// with [Connection.NewStream].
//
// All exported methods are safe for concurrent use. When the Connection is
// closed (by either side), all outstanding streams are torn down and any
// blocking Recv calls return [ErrClosed].
type Connection struct {
	conn              net.Conn
	writer            *bufio.Writer
	registry          *registry
	handlersEnabled   bool
	config            connConfig
	debug             connectionDebugCounters
	outbound          chan outboundFrame
	sendNotify        chan struct{}
	streams           map[uint32]*StreamContext
	streamsMu         sync.Mutex
	nextStreamID      atomic.Uint64
	defaultStreamOnce sync.Once
	defaultStreamView *Stream
	onNewStream       func(*StreamContext)
	onCloseStream     func(*StreamContext)
	nextSendSeq       atomic.Uint64
	transportOnce     sync.Once
	transportMu       sync.Mutex
	transportCond     *sync.Cond
	transportGen      uint64
	transportResume   uint64
	recovery          *recoveryState
	closeOnce         sync.Once
	closeCh           chan struct{}
	closed            atomic.Bool
}

type pendingConnection struct {
	connection *Connection
}

func newPendingConnection(conn net.Conn, registry *registry, onNewStream, onCloseStream func(*StreamContext)) *pendingConnection {
	if registry == nil {
		registry = newRegistrySnapshot()
	}
	connection := &Connection{
		conn:            conn,
		writer:          bufio.NewWriter(conn),
		registry:        registry,
		handlersEnabled: registry.hasHandlers(),
		outbound:        make(chan outboundFrame, streamQueueSize),
		sendNotify:      make(chan struct{}, 1),
		streams:         make(map[uint32]*StreamContext),
		onNewStream:     onNewStream,
		onCloseStream:   onCloseStream,
		closeCh:         make(chan struct{}),
	}
	connection.initTransportState()
	connection.nextStreamID.Store(0)
	return &pendingConnection{connection: connection}
}

func (p *pendingConnection) startWithConfig(config connConfig) *Connection {
	p.connection.config = config
	p.connection.start()
	return p.connection
}

func (p *pendingConnection) startClient(codecs []CodecID, maxFrame uint32, version byte) (*Connection, error) {
	config, err := handshakeClient(p.connection.conn, codecs, maxFrame, version)
	if err != nil {
		return nil, err
	}
	return p.startWithConfig(config), nil
}

func (p *pendingConnection) startServer(codecs []CodecID, maxFrame uint32, recoveryEnabled bool) (*Connection, error) {
	config, err := handshakeServer(p.connection.conn, codecs, maxFrame, recoveryEnabled)
	if err != nil {
		return nil, err
	}
	return p.startWithConfig(config), nil
}

func (c *Connection) start() {
	if c.isRecoveryEnabled() {
		go c.recoveryWriterLoop()
		go c.recoveryReaderLoop()
		return
	}
	// closeTransport clears c.conn and c.writer while the loops run, so the
	// loops must not read those fields.
	go c.writerLoop(c.writer)
	go c.readerLoop(c.conn)
}

func (c *Connection) nextOutboundSeq() uint64 {
	if c == nil || c.config.version != protocolVersionV3 {
		return 0
	}
	return c.nextSendSeq.Add(1)
}

func (c *Connection) isRecoveryEnabled() bool {
	return c != nil && c.recovery != nil && c.config.version == protocolVersionV3
}

// Close shuts down the connection and all of its streams. It closes the
// underlying transport, drains internal channels, and signals any goroutines
// blocked in [Connection.Recv] or [Connection.WaitClosed]. Close is
// non-blocking and idempotent; calling it more than once is safe.
func (c *Connection) Close() {
	c.markClosed()
}

// WaitClosed blocks until the connection has fully closed. It is commonly used
// in server handlers to keep the goroutine alive for the lifetime of the
// connection.
func (c *Connection) WaitClosed() {
	<-c.closeCh
}

// Send encodes msg and writes it on the default stream (stream 0). The call
// is fire-and-forget: it returns once the frame is queued for the writer
// goroutine. It returns [ErrClosed] if the connection has been shut down.
func (c *Connection) Send(msg Message) error {
	return c.defaultStream().Send(msg)
}

// SendRecv sends msg on the default stream and blocks until a reply of type T
// arrives. It returns [ErrClosed] if the connection closes before a reply is
// received.
func (c *Connection) SendRecv[T any](msg Message) (T, error) {
	return c.defaultStream().SendRecv[T](msg)
}

// Recv blocks until the next message arrives on the default stream and decodes
// it as type T. It returns [ErrClosed] if the connection closes and
// [ErrRecvTimeout] if a receive timeout (set via
// [Connection.SetRecvTimeout]) expires before a message arrives.
func (c *Connection) Recv[T any]() (T, error) {
	return c.defaultStream().Recv[T]()
}

// PeekWire returns the wire name of the next message on the default stream
// without consuming or fully decoding it. This is useful for branching on the
// message type before calling a typed receive.
func (c *Connection) PeekWire() (string, error) {
	return c.defaultStream().PeekWire()
}

// SetRecvTimeout sets the receive timeout for the default stream (stream 0).
// A zero or negative value disables the timeout, allowing [Connection.Recv]
// to block indefinitely.
func (c *Connection) SetRecvTimeout(timeout time.Duration) {
	c.defaultStream().SetRecvTimeout(timeout)
}

func (c *Connection) streamForID(streamID uint32) *Stream {
	if streamCtx := c.getStreamCtx(streamID, false); streamCtx != nil {
		return streamCtx.stream
	}
	return &Stream{id: streamID, connection: c}
}

// NewStream allocates a new stream ID and returns a [Stream] view scoped to
// that ID. Streams provide independent, multiplexed message channels over the
// same underlying connection. All streams are automatically closed when the
// parent Connection is closed.
//
// Stream IDs are never reused, so a connection can open at most 0xFFFFFFFE
// streams. After that, NewStream returns a stream whose operations report
// [ErrClosed], and [Connection.DebugState] reports
// [DebugFailureStreamIDsExhausted]. Open a new connection for further streams.
func (c *Connection) NewStream() *Stream {
	streamID := c.nextStreamID.Add(1)
	if streamID > uint64(maxStreamID) {
		c.debug.noteFailure(DebugFailureStreamIDsExhausted, "stream IDs exhausted")
		return closedStream(c)
	}
	return c.streamForID(uint32(streamID))
}

// closedStream returns a stream that is not registered on c and reports
// ErrClosed from every operation. Its ID is controlStreamID rather than 0
// because closing stream 0 would remove the default stream.
func closedStream(c *Connection) *Stream {
	inbox := make(chan rawMessage)
	close(inbox)
	s := &Stream{id: controlStreamID, connection: c, inbox: inbox}
	s.closed.Store(true)
	return s
}

func (c *Connection) defaultStream() *Stream {
	c.defaultStreamOnce.Do(func() {
		c.defaultStreamView = c.streamForID(defaultStreamID)
	})
	return c.defaultStreamView
}

func (c *Connection) markClosed() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.closeCh)
		c.closeTransport()
		if c.recovery != nil {
			c.recovery.onClosed(c)
		}
		c.closeAllStreams()
	})
}

func (c *Connection) closeAllStreams() {
	c.streamsMu.Lock()
	streams := c.streams
	c.streams = make(map[uint32]*StreamContext)
	c.streamsMu.Unlock()
	for _, streamCtx := range streams {
		if streamCtx.stream.close() {
			c.debug.streamsClosed.Add(1)
		}
		if c.onCloseStream != nil {
			c.onCloseStream(streamCtx)
		}
	}
}

func (c *Connection) removeStream(streamID uint32) {
	c.streamsMu.Lock()
	streamCtx := c.streams[streamID]
	delete(c.streams, streamID)
	c.streamsMu.Unlock()
	if streamCtx == nil {
		return
	}
	// Promote stream failure to connection so it survives stream removal.
	if code := streamCtx.stream.debug.lastFailureCode(); code != DebugFailureNone {
		c.debug.noteFailure(code, streamCtx.stream.debug.lastFailureReason())
	}
	if streamCtx.stream.close() {
		c.debug.streamsClosed.Add(1)
	}
	if c.onCloseStream != nil {
		c.onCloseStream(streamCtx)
	}
}

// openedStream reports whether NewStream on this side allocated streamID.
func (c *Connection) openedStream(streamID uint32) bool {
	return streamID != defaultStreamID && uint64(streamID) <= c.nextStreamID.Load()
}

// getStreamCtx returns the stream for streamID, creating it if needed. It
// returns nil if the connection is closed, or if inbound is set and the frame
// belongs to a stream this side allocated and already closed.
func (c *Connection) getStreamCtx(streamID uint32, inbound bool) *StreamContext {
	c.streamsMu.Lock()
	if c.closed.Load() {
		c.streamsMu.Unlock()
		return nil
	}
	if streamCtx, ok := c.streams[streamID]; ok {
		c.streamsMu.Unlock()
		return streamCtx
	}
	// Stream IDs are never reused, so a missing stream that this side opened
	// was closed on this side.
	if inbound && c.openedStream(streamID) {
		c.streamsMu.Unlock()
		return nil
	}
	streamCtx := c.newStreamCtx(streamID)
	c.streams[streamID] = streamCtx
	c.debug.streamsOpened.Add(1)
	c.streamsMu.Unlock()

	c.startStream(streamCtx)
	if c.onNewStream != nil {
		c.onNewStream(streamCtx)
	}
	return streamCtx
}

func (c *Connection) newStreamCtx(streamID uint32) *StreamContext {
	var handlerCh chan handlerJob
	if c.handlersEnabled {
		handlerCh = make(chan handlerJob, streamQueueSize)
	}
	return &StreamContext{
		stream: &Stream{
			id:         streamID,
			connection: c,
			inbox:      make(chan rawMessage, streamQueueSize),
			incoming:   make(chan []byte, streamQueueSize),
			handlerCh:  handlerCh,
		},
	}
}

func (c *Connection) startStream(streamCtx *StreamContext) {
	if streamCtx.stream.handlerCh != nil {
		go c.handlerLoop(streamCtx)
	}
	go c.decodeLoop(streamCtx)
}

func (c *Connection) handlerLoop(streamCtx *StreamContext) {
	stream := streamCtx.stream
	for job := range stream.handlerCh {
		stream.debug.handlerCalls.Add(1)
		c.debug.handlerCalls.Add(1)
		reply, err := job.handler(streamCtx, job.msg)
		if err != nil {
			stream.debug.noteFailure(DebugFailureHandler, "handler error: "+err.Error())
			c.debug.noteFailure(DebugFailureHandler, "handler error: "+err.Error())
			stream.debug.handlerErrors.Add(1)
			c.debug.handlerErrors.Add(1)
			_ = stream.sendBoxed(NewErrorReply("handler_error", err.Error()))
			continue
		}
		if reply == nil {
			_ = stream.sendBoxed(&OkReply{})
			continue
		}
		_ = stream.sendBoxed(reply)
	}
}

func (c *Connection) decodeLoop(streamCtx *StreamContext) {
	stream := streamCtx.stream
	for {
		select {
		case payload, ok := <-stream.incoming:
			if !ok {
				return
			}
			raw, err := newRawMessageFromPayload(c.config.codecID, payload)
			if err != nil {
				stream.noteDecodeError(err)
				stream.protocolError("codec_error", err.Error())
				return
			}
			if err := c.dispatchMessage(stream, raw); err != nil {
				stream.noteDecodeError(err)
				stream.protocolError("codec_error", err.Error())
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

func (c *Connection) dispatchMessage(stream *Stream, raw rawMessage) error {
	if handler, ok := c.registry.handler(raw.Wire); ok {
		msg, err := decodeRawWithRegistry(raw, c.registry)
		if err != nil {
			return err
		}
		if err := stream.handlerQ(handlerJob{handler: handler, msg: msg}); err != nil {
			if _, ok := err.(ErrClosed); ok {
				return nil
			}
			return err
		}
		return nil
	}
	if err := stream.inboxQ(raw); err != nil {
		if _, ok := err.(ErrClosed); ok {
			return nil
		}
		return err
	}
	return nil
}

func (c *Connection) writerLoop(w *bufio.Writer) {
	for {
		// Block until at least one frame is available.
		select {
		case <-c.closeCh:
			return
		case frame := <-c.outbound:
			if err := c.writeFrameNoFlush(w, frame.streamID, frame.seq, frame.flags, frame.payload); err != nil {
				c.debug.noteFailure(DebugFailureWriterLoop, "writer loop failed: "+err.Error())
				c.markClosed()
				return
			}
		}
		// Drain any queued frames without blocking.
		drain := true
		for drain {
			select {
			case frame := <-c.outbound:
				if err := c.writeFrameNoFlush(w, frame.streamID, frame.seq, frame.flags, frame.payload); err != nil {
					c.debug.noteFailure(DebugFailureWriterLoop, "writer loop failed: "+err.Error())
					c.markClosed()
					return
				}
			default:
				drain = false
			}
		}
		// Flush once for the entire batch.
		if err := w.Flush(); err != nil {
			c.debug.noteFailure(DebugFailureWriterLoop, "writer flush failed: "+err.Error())
			c.markClosed()
			return
		}
	}
}

func (c *Connection) readerLoop(conn net.Conn) {
	for {
		streamID, _, flags, payload, err := c.readFrameFrom(conn)
		if err != nil {
			c.debug.noteFailure(DebugFailureReaderLoop, "reader loop failed: "+err.Error())
			c.markClosed()
			return
		}
		if err := c.acceptFrame(streamID, flags, payload); err != nil {
			c.debug.noteFailure(DebugFailureReaderEnqueue, "reader enqueue failed: "+err.Error())
			c.markClosed()
			return
		}
	}
}

func (c *Connection) acceptFrame(streamID uint32, flags FrameFlags, payload []byte) error {
	fin := flags&FrameFlagFIN != 0 && streamID != defaultStreamID && streamID != controlStreamID
	if fin && len(payload) == 0 {
		c.removeStream(streamID)
		return nil
	}
	streamCtx := c.getStreamCtx(streamID, true)
	if streamCtx == nil {
		if c.closed.Load() {
			return ErrClosed{}
		}
		return nil
	}
	if len(payload) > 0 {
		if err := streamCtx.stream.incomingQ(payload); err != nil {
			if c.closed.Load() {
				return err
			}
			// The stream was closed after the lookup; drop the frame.
			return nil
		}
	}
	if fin {
		c.removeStream(streamID)
	}
	return nil
}

func (c *Connection) encodeMessage(msg Message) ([]byte, error) {
	if c.config.codec == nil {
		return nil, ErrUnsupportedCodec{Value: byte(c.config.codecID)}
	}
	return c.config.codec.Encode(msg)
}

func (c *Connection) enqueueFrame(frame outboundFrame) error {
	if c.isRecoveryEnabled() {
		frame.seq = 0
		frame.queued = make(chan error, 1)
		select {
		case <-c.closeCh:
			return ErrClosed{}
		case c.outbound <- frame:
		}
		c.signalSend()
		// The writer only takes frames while a transport is attached and exits
		// on close without draining outbound.
		select {
		case err := <-frame.queued:
			return err
		case <-c.closeCh:
			return ErrClosed{}
		}
	}
	select {
	case <-c.closeCh:
		return ErrClosed{}
	case c.outbound <- frame:
		return nil
	}
}

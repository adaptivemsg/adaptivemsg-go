package adaptivemsg

import (
	"bufio"
	"errors"
	"io"
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
	// closeFlushTimeout bounds how long Close waits for queued frames to be
	// written, and how long a clean close by the peer waits for the frames
	// received before it to be delivered.
	closeFlushTimeout = 5 * time.Second
)

type connConfig struct {
	version  byte
	codecID  CodecID
	codec    CodecImpl
	maxFrame uint32
}

// outboundFrame is a frame for the writer. A frame with closing set carries no
// data: the writer stops after writing the frames queued before it and closes
// closing.
type outboundFrame struct {
	streamID uint32
	seq      uint64
	flags    FrameFlags
	payload  []byte
	queued   chan error
	closing  chan struct{}
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
// All exported methods are safe for concurrent use, except that the default
// stream, like any [Stream], runs one Recv, PeekWire or SendRecv at a time;
// use [Connection.NewStream] for concurrent request-reply. When the
// Connection is closed (by either side), all outstanding streams are torn
// down and any blocking Recv calls return [ErrClosed].
type Connection struct {
	conn              net.Conn
	writer            *bufio.Writer
	registry          *registry
	handlersEnabled   bool
	recvTimeout       time.Duration
	config            connConfig
	debug             connectionDebugCounters
	outbound          chan outboundFrame
	sendNotify        chan struct{}
	streams           map[uint32]*StreamContext
	closedPeerStreams map[uint32]struct{}
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
	closing           atomic.Bool
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

// Close shuts down the connection and all of its streams. It first delivers
// the messages already queued by Send: it writes them, tells the peer that
// nothing more follows, and waits for the peer to close its side, at most 5
// seconds in total. In v3 the peer then releases the connection at once
// instead of keeping it for a reconnect. Close then closes the transport and
// wakes goroutines blocked in [Connection.Recv] or [Connection.WaitClosed].
// Send fails with [ErrClosed] once Close has begun. Close is idempotent;
// calling it more than once is safe.
func (c *Connection) Close() {
	if c.closing.CompareAndSwap(false, true) {
		c.closeGracefully()
	}
	c.markClosed()
}

// closeGracefully writes the frames queued so far, half-closes the transport
// and waits for the peer to close its side, at most closeFlushTimeout in
// total. Closing a socket that still holds unread data makes TCP reset the
// connection, and the peer then loses the frames it has not read yet.
func (c *Connection) closeGracefully() {
	if c.currentTransport() == nil {
		return
	}
	timer := time.NewTimer(closeFlushTimeout)
	defer timer.Stop()
	marker := outboundFrame{closing: make(chan struct{})}
	select {
	case c.outbound <- marker:
	case <-c.closeCh:
		return
	case <-timer.C:
		return
	}
	c.signalSend()
	select {
	case <-marker.closing:
	case <-c.closeCh:
		return
	case <-timer.C:
		return
	}
	conn, ok := c.currentTransport().(interface{ CloseWrite() error })
	if !ok || conn.CloseWrite() != nil {
		return
	}
	// The reader sees the peer's close and releases the connection.
	select {
	case <-c.closeCh:
	case <-timer.C:
	}
}

// closeByPeer handles a clean close by the peer. The frames received before
// it are delivered to their streams, waiting at most closeFlushTimeout, before
// the connection is released.
func (c *Connection) closeByPeer() {
	if !c.closing.CompareAndSwap(false, true) {
		// Close is waiting for the peer to finish.
		c.markClosed()
		return
	}
	c.streamsMu.Lock()
	streams := make([]*Stream, 0, len(c.streams))
	for _, streamCtx := range c.streams {
		streams = append(streams, streamCtx.stream)
	}
	c.streamsMu.Unlock()

	timer := time.NewTimer(closeFlushTimeout)
	defer timer.Stop()
	defer c.markClosed()
	// A nil payload is the FIN marker: each decode loop releases its stream
	// after delivering the frames queued before it.
	for _, stream := range streams {
		select {
		case stream.incoming <- nil:
		case <-stream.done:
		case <-timer.C:
			return
		}
	}
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-timer.C:
			return
		}
	}
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
// received. See [Stream.SendRecv].
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

// closePeerStream releases a stream that the peer opened. The peer is not told,
// so the ID stays reserved until the peer's FIN: later frames on it are dropped
// instead of opening the stream again.
func (c *Connection) closePeerStream(streamID uint32) {
	c.streamsMu.Lock()
	if _, ok := c.streams[streamID]; ok && streamID != defaultStreamID {
		if c.closedPeerStreams == nil {
			c.closedPeerStreams = make(map[uint32]struct{})
		}
		c.closedPeerStreams[streamID] = struct{}{}
	}
	c.streamsMu.Unlock()
	c.removeStream(streamID)
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
	// was closed on this side, as was one listed in closedPeerStreams.
	if inbound {
		if _, closed := c.closedPeerStreams[streamID]; closed || c.openedStream(streamID) {
			c.streamsMu.Unlock()
			return nil
		}
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
	stream := &Stream{
		id:         streamID,
		connection: c,
		inbox:      make(chan rawMessage, streamQueueSize),
		incoming:   make(chan []byte, streamQueueSize),
		handlerCh:  handlerCh,
		done:       make(chan struct{}),
	}
	stream.setRecvTimeout(c.recvTimeout)
	return &StreamContext{stream: stream}
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
	defer func() {
		close(stream.inbox)
		if stream.handlerCh != nil {
			close(stream.handlerCh)
		}
	}()
	for {
		select {
		case payload := <-stream.incoming:
			if payload == nil {
				c.removeStream(stream.id)
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
		case <-stream.done:
			return
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
		var frame outboundFrame
		select {
		case <-c.closeCh:
			return
		case frame = <-c.outbound:
		}
		// Write it and any other queued frames, then flush once for the batch.
		for {
			if frame.closing != nil {
				if err := w.Flush(); err != nil {
					c.debug.noteFailure(DebugFailureWriterLoop, "writer flush failed: "+err.Error())
				}
				close(frame.closing)
				return
			}
			if err := c.writeFrameNoFlush(w, frame.streamID, frame.seq, frame.flags, frame.payload); err != nil {
				c.debug.noteFailure(DebugFailureWriterLoop, "writer loop failed: "+err.Error())
				c.markClosed()
				return
			}
			select {
			case frame = <-c.outbound:
				continue
			default:
			}
			break
		}
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
		if errors.Is(err, io.EOF) {
			// The peer closed the transport at a frame boundary.
			c.closeByPeer()
			return
		}
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
		c.streamsMu.Lock()
		streamCtx := c.streams[streamID]
		delete(c.closedPeerStreams, streamID)
		c.streamsMu.Unlock()
		if streamCtx != nil {
			streamCtx.stream.queueFIN()
		}
		return nil
	}
	streamCtx := c.getStreamCtx(streamID, true)
	if streamCtx == nil {
		if c.closed.Load() {
			return ErrClosed{}
		}
		if fin {
			c.streamsMu.Lock()
			delete(c.closedPeerStreams, streamID)
			c.streamsMu.Unlock()
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
		streamCtx.stream.queueFIN()
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
	if c.closing.Load() {
		return ErrClosed{}
	}
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

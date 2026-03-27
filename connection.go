package adaptivemsg

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	streamQueueSize = 1024
	defaultStreamID = 0
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
	payload  []byte
}

type handlerJob struct {
	handler handlerFunc
	msg     Message
}

// Connection is a live session and also acts as the default stream.
type Connection struct {
	conn              net.Conn
	registry          *registry
	config            connConfig
	debug             connectionDebugCounters
	outbound          chan outboundFrame
	sendNotify        chan struct{}
	streams           map[uint32]*StreamContext
	streamsMu         sync.Mutex
	nextStreamID      atomic.Uint32
	defaultStreamOnce sync.Once
	defaultStreamView *Stream[Message]
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
		conn:          conn,
		registry:      registry,
		outbound:      make(chan outboundFrame, streamQueueSize),
		sendNotify:    make(chan struct{}, 1),
		streams:       make(map[uint32]*StreamContext),
		onNewStream:   onNewStream,
		onCloseStream: onCloseStream,
		closeCh:       make(chan struct{}),
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
	go c.writerLoop()
	go c.readerLoop()
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

// Close shuts down the connection and all streams.
func (c *Connection) Close() {
	c.markClosed()
}

// WaitClosed blocks until the connection closes.
func (c *Connection) WaitClosed() {
	<-c.closeCh
}

// Send writes a message on the default stream.
func (c *Connection) Send(msg Message) error {
	return c.defaultStream().Send(msg)
}

// SendRecv sends a message and waits for the reply on the default stream.
func (c *Connection) SendRecv(msg Message) (Message, error) {
	return c.defaultStream().SendRecv(msg)
}

// Recv reads the next message from the default stream.
func (c *Connection) Recv() (Message, error) {
	return c.defaultStream().Recv()
}

// PeekWire returns the next wire name on the default stream without decoding.
func (c *Connection) PeekWire() (string, error) {
	return c.defaultStream().PeekWire()
}

// SetRecvTimeout sets the default stream receive timeout.
func (c *Connection) SetRecvTimeout(timeout time.Duration) {
	c.defaultStream().SetRecvTimeout(timeout)
}

func (c *Connection) viewCore() *streamCore {
	if c == nil {
		return nil
	}
	return c.defaultStream().core
}

func (c *Connection) streamForID(streamID uint32) *streamCore {
	streamCtx := c.getStreamCtx(streamID)
	return streamCtx.stream.core
}

// NewStream allocates a new stream ID and returns a Stream view.
func (c *Connection) NewStream() *Stream[Message] {
	streamID := c.nextStreamID.Add(1)
	return &Stream[Message]{core: c.streamForID(streamID)}
}

func (c *Connection) defaultStream() *Stream[Message] {
	c.defaultStreamOnce.Do(func() {
		c.defaultStreamView = &Stream[Message]{core: c.streamForID(defaultStreamID)}
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
		if streamCtx.stream.core.close() {
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
	if streamCtx.stream.core.close() {
		c.debug.streamsClosed.Add(1)
	}
	if c.onCloseStream != nil {
		c.onCloseStream(streamCtx)
	}
}

func (c *Connection) getStreamCtx(streamID uint32) *StreamContext {
	c.streamsMu.Lock()
	streamCtx, ok := c.streams[streamID]
	if ok {
		c.streamsMu.Unlock()
		return streamCtx
	}
	streamCtx = c.makeStreamLocked(streamID)
	c.streamsMu.Unlock()
	if c.onNewStream != nil {
		c.onNewStream(streamCtx)
	}
	return streamCtx
}

func (c *Connection) makeStreamLocked(streamID uint32) *StreamContext {
	inbox := make(chan rawMessage, streamQueueSize)
	incoming := make(chan []byte, streamQueueSize)
	var handlerCh chan handlerJob
	if c.registry.hasHandlers() {
		handlerCh = make(chan handlerJob, streamQueueSize)
	}
	core := &streamCore{
		id:         streamID,
		connection: c,
		inbox:      inbox,
		incoming:   incoming,
		handlerCh:  handlerCh,
	}
	stream := &Stream[Message]{core: core}
	streamCtx := &StreamContext{
		stream: stream,
	}
	c.streams[streamID] = streamCtx
	c.debug.streamsOpened.Add(1)
	if handlerCh != nil {
		go c.handlerLoop(streamCtx)
	}
	go c.decodeLoop(streamCtx)
	return streamCtx
}

func (c *Connection) handlerLoop(streamCtx *StreamContext) {
	core := streamCtx.stream.core
	for job := range core.handlerCh {
		core.debug.handlerCalls.Add(1)
		c.debug.handlerCalls.Add(1)
		reply, err := job.handler(streamCtx, job.msg)
		if err != nil {
			core.debug.noteFailure(DebugFailureHandler, "handler error: "+err.Error())
			c.debug.noteFailure(DebugFailureHandler, "handler error: "+err.Error())
			core.debug.handlerErrors.Add(1)
			c.debug.handlerErrors.Add(1)
			_ = core.sendBoxed(NewErrorReply("handler_error", err.Error()))
			continue
		}
		if reply == nil {
			_ = core.sendBoxed(&OkReply{})
			continue
		}
		_ = core.sendBoxed(reply)
	}
}

func (c *Connection) decodeLoop(streamCtx *StreamContext) {
	core := streamCtx.stream.core
	for {
		select {
		case payload, ok := <-core.incoming:
			if !ok {
				return
			}
			raw, err := newRawMessageFromPayload(c.config.codecID, payload)
			if err != nil {
				core.noteDecodeError(err)
				core.protocolError("codec_error", err.Error())
				return
			}
			if err := c.dispatchMessage(core, raw); err != nil {
				core.noteDecodeError(err)
				core.protocolError("codec_error", err.Error())
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

func (c *Connection) dispatchMessage(stream *streamCore, raw rawMessage) error {
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

func (c *Connection) writerLoop() {
	if c.isRecoveryEnabled() {
		c.recoveryWriterLoop()
		return
	}
	for {
		select {
		case <-c.closeCh:
			return
		case frame := <-c.outbound:
			if err := c.writeFrame(frame.streamID, frame.seq, frame.payload); err != nil {
				c.debug.noteFailure(DebugFailureWriterLoop, "writer loop failed: "+err.Error())
				c.markClosed()
				return
			}
		}
	}
}

func (c *Connection) readerLoop() {
	if c.isRecoveryEnabled() {
		c.recoveryReaderLoop()
		return
	}
	for {
		streamID, _, payload, err := c.readFrame()
		if err != nil {
			c.debug.noteFailure(DebugFailureReaderLoop, "reader loop failed: "+err.Error())
			c.markClosed()
			return
		}
		streamCtx := c.getStreamCtx(streamID)
		if err := streamCtx.stream.core.incomingQ(payload); err != nil {
			c.debug.noteFailure(DebugFailureReaderEnqueue, "reader enqueue failed: "+err.Error())
			c.markClosed()
			return
		}
	}
}

func (c *Connection) encodeMessage(msg Message) ([]byte, error) {
	if c.config.codec == nil {
		return nil, ErrUnsupportedCodec{Value: byte(c.config.codecID)}
	}
	return c.config.codec.Encode(msg)
}

func (c *Connection) enqueueFrame(frame outboundFrame) error {
	if c.isRecoveryEnabled() {
		stored, err := c.recovery.replay.add(frame)
		if err != nil {
			return err
		}
		c.recovery.enqueueLive(stored)
		c.signalSend()
		return nil
	}
	select {
	case <-c.closeCh:
		return ErrClosed{}
	case c.outbound <- frame:
		return nil
	}
}

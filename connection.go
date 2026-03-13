package adaptivemsg

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	streamQueueSize    = 1024
	defaultStreamID    = 0
	protocolVersion    = 2
	frameHeaderLen     = 10
	handshakeHeaderLen = 12
	maxCodecCount      = 16
)

var handshakeMagic = [2]byte{'A', 'M'}

const defaultMaxFrame = ^uint32(0)

type connConfig struct {
	version  byte
	codecID  CodecID
	codec    CodecImpl
	maxFrame uint32
}

type outboundFrame struct {
	streamID uint32
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
	outbound          chan outboundFrame
	streams           map[uint32]*StreamContext
	streamsMu         sync.Mutex
	nextStreamID      atomic.Uint32
	defaultStreamOnce sync.Once
	defaultStreamView *Stream[Message]
	onNewStream       func(*StreamContext)
	onCloseStream     func(*StreamContext)
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
		streams:       make(map[uint32]*StreamContext),
		onNewStream:   onNewStream,
		onCloseStream: onCloseStream,
		closeCh:       make(chan struct{}),
	}
	connection.nextStreamID.Store(0)
	return &pendingConnection{connection: connection}
}

func (p *pendingConnection) startClient(codecs []CodecID, maxFrame uint32) (*Connection, error) {
	config, err := handshakeClient(p.connection.conn, codecs, maxFrame)
	if err != nil {
		return nil, err
	}
	p.connection.config = config
	p.connection.start()
	return p.connection, nil
}

func (p *pendingConnection) startServer(codecs []CodecID, maxFrame uint32) (*Connection, error) {
	config, err := handshakeServer(p.connection.conn, codecs, maxFrame)
	if err != nil {
		return nil, err
	}
	p.connection.config = config
	p.connection.start()
	return p.connection, nil
}

func (c *Connection) start() {
	go c.writerLoop()
	go c.readerLoop()
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
		_ = c.conn.Close()
		c.closeAllStreams()
	})
}

func (c *Connection) closeAllStreams() {
	c.streamsMu.Lock()
	streams := c.streams
	c.streams = make(map[uint32]*StreamContext)
	c.streamsMu.Unlock()
	for _, streamCtx := range streams {
		streamCtx.stream.core.close()
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
	streamCtx.stream.core.close()
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
	if handlerCh != nil {
		go c.handlerLoop(streamCtx)
	}
	go c.decodeLoop(streamCtx)
	return streamCtx
}

func (c *Connection) handlerLoop(streamCtx *StreamContext) {
	core := streamCtx.stream.core
	for job := range core.handlerCh {
		reply, err := job.handler(streamCtx, job.msg)
		if err != nil {
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
				core.protocolError("codec_error", err.Error())
				return
			}
			if err := c.dispatchMessage(core, raw); err != nil {
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
	for {
		select {
		case <-c.closeCh:
			return
		case frame := <-c.outbound:
			if err := c.writeFrame(frame.streamID, frame.payload); err != nil {
				c.markClosed()
				return
			}
		}
	}
}

func (c *Connection) readerLoop() {
	for {
		streamID, payload, err := c.readFrame()
		if err != nil {
			c.markClosed()
			return
		}
		streamCtx := c.getStreamCtx(streamID)
		if err := streamCtx.stream.core.incomingQ(payload); err != nil {
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
	select {
	case <-c.closeCh:
		return ErrClosed{}
	case c.outbound <- frame:
		return nil
	}
}

func (c *Connection) buildHeader(streamID uint32, payloadLen int) ([frameHeaderLen]byte, error) {
	if payloadLen > int(^uint32(0)) {
		return [frameHeaderLen]byte{}, ErrFrameTooLarge{Size: payloadLen}
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return [frameHeaderLen]byte{}, ErrFrameTooLarge{Size: payloadLen}
	}
	var header [frameHeaderLen]byte
	header[0] = c.config.version
	header[1] = 0
	binary.BigEndian.PutUint32(header[2:6], streamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(payloadLen))
	return header, nil
}

func (c *Connection) parseHeader(header [frameHeaderLen]byte) (uint32, int, error) {
	version := header[0]
	if version != c.config.version {
		return 0, 0, ErrUnsupportedFrameVersion{Version: version}
	}
	streamID := binary.BigEndian.Uint32(header[2:6])
	payloadLen := binary.BigEndian.Uint32(header[6:10])
	return streamID, int(payloadLen), nil
}

func (c *Connection) writeFrame(streamID uint32, payload []byte) error {
	header, err := c.buildHeader(streamID, len(payload))
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(header[:]); err != nil {
		return err
	}
	if _, err := c.conn.Write(payload); err != nil {
		return err
	}
	return nil
}

func (c *Connection) readFrame() (uint32, []byte, error) {
	var header [frameHeaderLen]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return 0, nil, err
	}
	streamID, payloadLen, err := c.parseHeader(header)
	if err != nil {
		return 0, nil, err
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return 0, nil, ErrFrameTooLarge{Size: payloadLen}
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, nil, err
	}
	return streamID, payload, nil
}

func handshakeClient(conn net.Conn, codecs []CodecID, maxFrame uint32) (connConfig, error) {
	if len(codecs) == 0 {
		return connConfig{}, ErrInvalidMessage{Reason: "codec list must be non-empty"}
	}
	if len(codecs) > maxCodecCount {
		return connConfig{}, ErrTooManyCodecs{Count: len(codecs)}
	}
	for _, codecID := range codecs {
		if codecID == 0 {
			return connConfig{}, ErrInvalidMessage{Reason: "codec ID must be non-zero"}
		}
		if _, ok := codecByID(codecID); !ok {
			return connConfig{}, ErrUnsupportedCodec{Value: byte(codecID)}
		}
	}

	request := make([]byte, handshakeHeaderLen)
	copy(request[0:2], handshakeMagic[:])
	request[2] = protocolVersion
	request[3] = byte(len(codecs))
	request[4] = 0
	request[5] = 0
	request[6] = 0
	request[7] = 0
	binary.BigEndian.PutUint32(request[8:12], maxFrame)
	if _, err := conn.Write(request); err != nil {
		return connConfig{}, err
	}
	list := make([]byte, len(codecs))
	for i, codecID := range codecs {
		list[i] = byte(codecID)
	}
	if _, err := conn.Write(list); err != nil {
		return connConfig{}, err
	}

	response := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(conn, response); err != nil {
		return connConfig{}, err
	}
	if response[0] != handshakeMagic[0] || response[1] != handshakeMagic[1] {
		return connConfig{}, ErrBadHandshakeMagic{}
	}
	accept := response[2]
	version := response[3]
	selected := CodecID(response[4])
	serverMax := binary.BigEndian.Uint32(response[8:12])
	if version != protocolVersion {
		return connConfig{}, ErrUnsupportedFrameVersion{Version: version}
	}
	if accept == 0 {
		return connConfig{}, ErrNoCommonCodec{}
	}
	if !containsCodec(codecs, selected) {
		return connConfig{}, ErrNoCommonCodec{}
	}
	codec, ok := codecByID(selected)
	if !ok {
		return connConfig{}, ErrUnsupportedCodec{Value: byte(selected)}
	}
	return connConfig{
		version:  version,
		codecID:  selected,
		codec:    codec,
		maxFrame: serverMax,
	}, nil
}

func handshakeServer(conn net.Conn, codecs []CodecID, maxFrame uint32) (connConfig, error) {
	if len(codecs) == 0 {
		return connConfig{}, ErrInvalidMessage{Reason: "codec list must be non-empty"}
	}
	if len(codecs) > maxCodecCount {
		return connConfig{}, ErrTooManyCodecs{Count: len(codecs)}
	}
	for _, codecID := range codecs {
		if codecID == 0 {
			return connConfig{}, ErrInvalidMessage{Reason: "codec ID must be non-zero"}
		}
		if _, ok := codecByID(codecID); !ok {
			return connConfig{}, ErrUnsupportedCodec{Value: byte(codecID)}
		}
	}

	request := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(conn, request); err != nil {
		return connConfig{}, err
	}
	if request[0] != handshakeMagic[0] || request[1] != handshakeMagic[1] {
		return connConfig{}, ErrBadHandshakeMagic{}
	}
	version := request[2]
	codecCount := int(request[3])
	clientMaxFrame := binary.BigEndian.Uint32(request[8:12])
	if version != protocolVersion {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrUnsupportedFrameVersion{Version: version}
	}
	if codecCount == 0 {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrNoCommonCodec{}
	}
	if codecCount > maxCodecCount {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrTooManyCodecs{Count: codecCount}
	}
	clientCodecs := make([]byte, codecCount)
	if _, err := io.ReadFull(conn, clientCodecs); err != nil {
		return connConfig{}, err
	}
	selected, ok := selectCodec(clientCodecs, codecs)
	if !ok {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrNoCommonCodec{}
	}
	negotiatedMax := uint32(0)
	if clientMaxFrame != 0 {
		if clientMaxFrame < maxFrame {
			negotiatedMax = clientMaxFrame
		} else {
			negotiatedMax = maxFrame
		}
	}
	if err := writeHandshakeReply(conn, 1, protocolVersion, selected, negotiatedMax); err != nil {
		return connConfig{}, err
	}
	codec, ok := codecByID(selected)
	if !ok {
		return connConfig{}, ErrUnsupportedCodec{Value: byte(selected)}
	}
	return connConfig{
		version:  protocolVersion,
		codecID:  selected,
		codec:    codec,
		maxFrame: negotiatedMax,
	}, nil
}

func writeHandshakeReply(conn net.Conn, accept byte, version byte, codecID CodecID, maxFrame uint32) error {
	response := make([]byte, handshakeHeaderLen)
	copy(response[0:2], handshakeMagic[:])
	response[2] = accept
	response[3] = version
	response[4] = byte(codecID)
	response[5] = 0
	response[6] = 0
	response[7] = 0
	binary.BigEndian.PutUint32(response[8:12], maxFrame)
	_, err := conn.Write(response)
	return err
}

func containsCodec(codecs []CodecID, id CodecID) bool {
	for _, codec := range codecs {
		if codec == id {
			return true
		}
	}
	return false
}

func selectCodec(clientCodecs []byte, supported []CodecID) (CodecID, bool) {
	for _, raw := range clientCodecs {
		for _, sup := range supported {
			if raw == byte(sup) {
				return sup, true
			}
		}
	}
	return 0, false
}

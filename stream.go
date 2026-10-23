package adaptivemsg

import (
	"reflect"
	"sync"
	"time"

	"sync/atomic"
)

// Stream is a single multiplexed stream on a [Connection].
//
// The type argument supplied to [Stream.SendRecv] or [Stream.Recv] determines
// how each incoming message is decoded. When that type is an interface (for
// example [Message]), the connection's message registry resolves the concrete
// type from the wire name. A concrete type (for example *EchoReply) is decoded
// directly without a registry lookup.
//
// Send is safe for concurrent use by multiple goroutines. Recv and SendRecv
// must not be called concurrently on the same Stream; doing so returns
// [ErrConcurrentRecv].
type Stream struct {
	id               uint32
	connection       *Connection
	debug            streamDebugCounters
	inbox            chan rawMessage
	incoming         chan []byte
	handlerCh        chan handlerJob
	recvTimeoutNanos atomic.Int64
	recvActive       atomic.Bool
	peeked           rawMessage
	hasPeeked        bool
	closed           atomic.Bool
	closeOnce        sync.Once
	sendMu           sync.Mutex
}

// ID returns the numeric stream identifier assigned by the connection.
// Stream IDs are unique within a single connection.
func (s *Stream) ID() uint32 {
	return s.id
}

// Close releases this stream. If this side opened the stream with
// [Connection.NewStream], Close first queues a FIN frame so the peer can
// release its copy. Only the opener sends FIN, so a stream opened by the peer
// is released locally, as is stream 0, whose lifetime follows the connection.
// After Close returns, subsequent Send and Recv calls on this stream return
// [ErrClosed]. Close is idempotent.
func (s *Stream) Close() {
	if s == nil || s.connection == nil {
		return
	}
	if !s.connection.openedStream(s.id) {
		s.connection.removeStream(s.id)
		return
	}
	s.sendMu.Lock()
	if s.closed.Load() {
		s.sendMu.Unlock()
		return
	}
	s.closed.Store(true)
	// The payload must be non-nil: the recovery writer treats a nil payload
	// as "no frame".
	err := s.connection.enqueueFrame(outboundFrame{
		streamID: s.id,
		flags:    FrameFlagFIN,
		payload:  []byte{},
	})
	s.sendMu.Unlock()
	s.connection.removeStream(s.id)
	if err != nil {
		s.debug.noteFailure(DebugFailureStreamEnqueue, "fin enqueue failed: "+err.Error())
	}
}

// SetRecvTimeout sets the maximum duration that [Stream.Recv] and
// [Stream.SendRecv] will block waiting for a message on this stream.
// A zero or negative value disables the timeout, causing receives to
// block indefinitely until a message arrives or the connection closes.
// The default is no timeout.
func (s *Stream) SetRecvTimeout(timeout time.Duration) {
	s.setRecvTimeout(timeout)
}

// Send encodes msg with the negotiated codec and enqueues the resulting
// frame for transmission on this stream. Send returns as soon as the frame
// is queued; it does not wait for the peer to receive it.
//
// Send is safe for concurrent use. It returns [ErrClosed] if the
// connection has been shut down.
func (s *Stream) Send(msg Message) error {
	return s.sendBoxed(msg)
}

// SendRecv sends msg on this stream and blocks until a reply of type T is
// received. If the peer handler returns an error, the reply is decoded as an
// [ErrorReply] and returned as [ErrRemote]. If the incoming message cannot be
// converted to T, [ErrTypeMismatch] is returned. The call also respects the
// receive timeout set by [Stream.SetRecvTimeout].
func (s *Stream) SendRecv[T any](msg Message) (T, error) {
	return sendRecvAs[T](s, msg)
}

func sendRecvAs[T any](stream *Stream, msg Message) (T, error) {
	var zero T
	if err := stream.sendBoxed(msg); err != nil {
		return zero, err
	}
	raw, err := stream.recvRaw()
	if err != nil {
		return zero, err
	}
	if raw.Wire == errorReplyWireName {
		reply, err := decodeRawAs[*ErrorReply](raw)
		if err != nil {
			stream.noteDecodeError(err)
			stream.protocolError("codec_error", err.Error())
			return zero, err
		}
		stream.noteRemoteError()
		return zero, ErrRemote{Code: reply.Code, Message: reply.Message}
	}
	return decodeStreamRawAs[T](stream, raw)
}

func decodeStreamRawAs[T any](stream *Stream, raw rawMessage) (T, error) {
	var zero T
	if isInterfaceType[T]() {
		decoded, err := decodeRawWithRegistry(raw, stream.connection.registry)
		if err != nil {
			stream.noteDecodeError(err)
			return zero, err
		}
		typed, ok := decoded.(T)
		if !ok {
			return zero, ErrTypeMismatch{Expected: expectedWireName[T](), Got: wireNameForValue(decoded)}
		}
		return typed, nil
	}
	typed, err := decodeRawAs[T](raw)
	if err != nil {
		stream.noteDecodeError(err)
		stream.protocolErrorFor(err)
		return zero, err
	}
	return typed, nil
}

// Recv blocks until the next message arrives on this stream and decodes it
// as type T. It returns [ErrRecvTimeout] if the receive timeout expires,
// [ErrClosed] if the connection is shut down, or [ErrTypeMismatch] if the
// decoded message cannot be converted to T.
//
// Only one goroutine may call Recv (or SendRecv) at a time; a concurrent
// call returns [ErrConcurrentRecv].
func (s *Stream) Recv[T any]() (T, error) {
	var zero T
	raw, err := s.recvRaw()
	if err != nil {
		return zero, err
	}
	return decodeStreamRawAs[T](s, raw)
}

// PeekWire returns the wire name of the next pending message without
// consuming it. The message remains available for a subsequent [Stream.Recv]
// or [Stream.SendRecv] call. PeekWire is useful for dispatching based on
// message type before committing to a decode.
func (s *Stream) PeekWire() (string, error) {
	return s.peekWire()
}

func (s *Stream) setRecvTimeout(timeout time.Duration) {
	if timeout <= 0 {
		s.recvTimeoutNanos.Store(0)
		return
	}
	s.recvTimeoutNanos.Store(timeout.Nanoseconds())
}

func (s *Stream) recvGuard() (func(), error) {
	if !s.recvActive.CompareAndSwap(false, true) {
		return nil, ErrConcurrentRecv{}
	}
	return func() {
		s.recvActive.Store(false)
	}, nil
}

func (s *Stream) recvRaw() (rawMessage, error) {
	release, err := s.recvGuard()
	if err != nil {
		return rawMessage{}, err
	}
	defer release()
	if s.hasPeeked {
		msg := s.peeked
		s.hasPeeked = false
		s.noteReceivedMessage()
		return msg, nil
	}
	return s.readRaw()
}

func (s *Stream) peekWire() (string, error) {
	msg, err := s.peekRaw()
	if err != nil {
		return "", err
	}
	return msg.Wire, nil
}

func (s *Stream) peekRaw() (rawMessage, error) {
	release, err := s.recvGuard()
	if err != nil {
		return rawMessage{}, err
	}
	defer release()
	if s.hasPeeked {
		return s.peeked, nil
	}
	msg, err := s.readRaw()
	if err != nil {
		return rawMessage{}, err
	}
	s.peeked = msg
	s.hasPeeked = true
	return msg, nil
}

func (s *Stream) readRaw() (rawMessage, error) {
	timeoutNanos := s.recvTimeoutNanos.Load()
	if timeoutNanos == 0 {
		select {
		case msg, ok := <-s.inbox:
			if !ok {
				return rawMessage{}, ErrClosed{}
			}
			s.noteReceivedMessage()
			return msg, nil
		case <-s.connection.closeCh:
			return rawMessage{}, ErrClosed{}
		}
	}
	timer := time.NewTimer(time.Duration(timeoutNanos))
	defer timer.Stop()
	select {
	case msg, ok := <-s.inbox:
		if !ok {
			return rawMessage{}, ErrClosed{}
		}
		s.noteReceivedMessage()
		return msg, nil
	case <-s.connection.closeCh:
		return rawMessage{}, ErrClosed{}
	case <-timer.C:
		s.debug.noteFailure(DebugFailureStreamRecvTimeout, "recv timeout")
		s.connection.debug.noteFailure(DebugFailureStreamRecvTimeout, "stream recv timeout")
		return rawMessage{}, ErrRecvTimeout{}
	}
}

func (s *Stream) sendBoxed(msg Message) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed.Load() {
		return ErrClosed{}
	}
	payload, err := s.connection.encodeMessage(msg)
	if err != nil {
		s.debug.noteFailure(DebugFailureStreamEncode, "encode failed: "+err.Error())
		s.connection.debug.noteFailure(DebugFailureStreamEncode, "encode failed: "+err.Error())
		return err
	}
	frame := outboundFrame{streamID: s.id, payload: payload}
	if err := s.connection.enqueueFrame(frame); err != nil {
		s.debug.noteFailure(DebugFailureStreamEnqueue, "enqueue failed: "+err.Error())
		s.connection.debug.noteFailure(DebugFailureStreamEnqueue, "enqueue failed: "+err.Error())
		return err
	}
	s.debug.dataMessagesSent.Add(1)
	s.connection.debug.dataMessagesSent.Add(1)
	return nil
}

func (s *Stream) inboxQ(msg rawMessage) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrClosed{}
		}
	}()
	select {
	case s.inbox <- msg:
		return nil
	case <-s.connection.closeCh:
		return ErrClosed{}
	}
}

func (s *Stream) incomingQ(payload []byte) (err error) {
	if s.incoming == nil {
		return ErrClosed{}
	}
	defer func() {
		if recover() != nil {
			err = ErrClosed{}
		}
	}()
	select {
	case s.incoming <- payload:
		return nil
	case <-s.connection.closeCh:
		return ErrClosed{}
	}
}

func (s *Stream) handlerQ(job handlerJob) (err error) {
	if s.handlerCh == nil {
		return ErrClosed{}
	}
	defer func() {
		if recover() != nil {
			err = ErrClosed{}
		}
	}()
	select {
	case s.handlerCh <- job:
		return nil
	case <-s.connection.closeCh:
		return ErrClosed{}
	}
}

func (s *Stream) close() bool {
	closed := false
	s.closeOnce.Do(func() {
		closed = true
		s.closed.Store(true)
		close(s.inbox)
		if s.incoming != nil {
			close(s.incoming)
		}
		if s.handlerCh != nil {
			close(s.handlerCh)
		}
	})
	return closed
}

func (s *Stream) protocolError(code, message string) {
	if code == "" {
		code = "protocol_error"
	}
	reason := code
	if message != "" {
		reason += ": " + message
	}
	s.debug.noteFailure(DebugFailureStreamProtocol, reason)
	s.connection.debug.noteFailure(DebugFailureStreamProtocol, "stream protocol error: "+reason)
	s.debug.protocolErrors.Add(1)
	s.connection.debug.protocolErrors.Add(1)
	if err := s.sendBoxed(NewErrorReply(code, message)); err != nil {
		s.debug.noteFailure(DebugFailureStreamProtocolReplySend, "protocol error reply send failed: "+err.Error())
		s.connection.debug.noteFailure(DebugFailureStreamProtocolReplySend, "protocol error reply send failed: "+err.Error())
		s.debug.protocolErrorReplySendFailure.Add(1)
		s.connection.debug.protocolErrorReplySendFailure.Add(1)
	}
	s.Close()
}

func (s *Stream) protocolErrorFor(err error) {
	if err == nil {
		return
	}
	if mismatch, ok := err.(ErrTypeMismatch); ok {
		s.protocolError("protocol_error", "expected "+mismatch.Expected+" got "+mismatch.Got)
		return
	}
	s.protocolError("codec_error", err.Error())
}

func expectedWireName[T any]() string {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() == reflect.Interface {
		return t.String()
	}
	info := messageTypeInfo(t)
	if info.err == nil && info.wire != "" {
		return info.wire
	}
	return t.String()
}

func isInterfaceType[T any]() bool {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return t.Kind() == reflect.Interface
}

var errorReplyWireName = expectedWireName[*ErrorReply]()

func (s *Stream) noteDecodeError(err error) {
	reason := "decode failed"
	if err != nil {
		reason = "decode failed: " + err.Error()
	}
	s.debug.noteFailure(DebugFailureStreamDecode, reason)
	s.connection.debug.noteFailure(DebugFailureStreamDecode, reason)
	s.debug.decodeErrors.Add(1)
	s.connection.debug.decodeErrors.Add(1)
}

func (s *Stream) noteRemoteError() {
	s.debug.remoteErrors.Add(1)
	s.connection.debug.remoteErrors.Add(1)
}

func (s *Stream) noteReceivedMessage() {
	s.debug.dataMessagesReceived.Add(1)
	s.connection.debug.dataMessagesReceived.Add(1)
}

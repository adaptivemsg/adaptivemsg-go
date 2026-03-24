package adaptivemsg

import (
	"reflect"
	"sync"
	"time"

	"sync/atomic"
)

type streamCore struct {
	id               uint32
	connection       *Connection
	inbox            chan rawMessage
	incoming         chan []byte
	handlerCh        chan handlerJob
	recvTimeoutNanos atomic.Int64
	recvActive       atomic.Bool
	peeked           rawMessage
	hasPeeked        bool
	closeOnce        sync.Once
}

// Stream is a typed view over a single stream ID.
type Stream[T any] struct {
	core *streamCore
}

type viewCoreProvider interface {
	viewCore() *streamCore
}

// StreamAs returns a typed Stream view for a stream provider.
func StreamAs[T any](v viewCoreProvider) *Stream[T] {
	if v == nil {
		return nil
	}
	if stream, ok := v.(*Stream[T]); ok {
		return stream
	}
	core := v.viewCore()
	if core == nil {
		return nil
	}
	return &Stream[T]{core: core}
}

// SendRecvAs sends a message and receives a typed reply on a stream provider.
func SendRecvAs[T any](v viewCoreProvider, msg Message) (T, error) {
	var zero T
	stream := StreamAs[T](v)
	if stream == nil {
		return zero, ErrInvalidMessage{Reason: "stream view is nil"}
	}
	return stream.SendRecv(msg)
}

func (s *Stream[T]) viewCore() *streamCore {
	if s == nil {
		return nil
	}
	return s.core
}

// ID returns the stream ID.
func (s *Stream[T]) ID() uint32 {
	return s.core.id
}

// Close removes the stream from its connection.
func (s *Stream[T]) Close() {
	s.core.connection.removeStream(s.core.id)
}

// SetRecvTimeout sets the receive timeout for this stream.
func (s *Stream[T]) SetRecvTimeout(timeout time.Duration) {
	s.core.setRecvTimeout(timeout)
}

// Send encodes and writes a message on this stream.
func (s *Stream[T]) Send(msg Message) error {
	return s.core.sendBoxed(msg)
}

// SendRecv sends a message and waits for a typed reply.
func (s *Stream[T]) SendRecv(msg Message) (T, error) {
	var zero T
	if err := s.core.sendBoxed(msg); err != nil {
		return zero, err
	}
	raw, err := s.core.recvRaw()
	if err != nil {
		return zero, err
	}
	if raw.Wire == errorReplyWireName {
		reply, err := decodeRawAs[*ErrorReply](raw)
		if err != nil {
			s.core.protocolError("codec_error", err.Error())
			return zero, err
		}
		return zero, ErrRemote{Code: reply.Code, Message: reply.Message}
	}
	if isInterfaceType[T]() {
		decoded, err := decodeRawWithRegistry(raw, s.core.connection.registry)
		if err != nil {
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
		s.core.protocolErrorFor(err)
		return zero, err
	}
	return typed, nil
}

// Recv reads and decodes the next message from this stream.
func (s *Stream[T]) Recv() (T, error) {
	var zero T
	raw, err := s.core.recvRaw()
	if err != nil {
		return zero, err
	}
	if isInterfaceType[T]() {
		decoded, err := decodeRawWithRegistry(raw, s.core.connection.registry)
		if err != nil {
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
		s.core.protocolErrorFor(err)
		return zero, err
	}
	return typed, nil
}

// PeekWire returns the next wire name without decoding.
func (s *Stream[T]) PeekWire() (string, error) {
	return s.core.peekWire()
}

func (s *streamCore) setRecvTimeout(timeout time.Duration) {
	if timeout <= 0 {
		s.recvTimeoutNanos.Store(0)
		return
	}
	s.recvTimeoutNanos.Store(timeout.Nanoseconds())
}

func (s *streamCore) recvGuard() (func(), error) {
	if !s.recvActive.CompareAndSwap(false, true) {
		return nil, ErrConcurrentRecv{}
	}
	return func() {
		s.recvActive.Store(false)
	}, nil
}

func (s *streamCore) recvRaw() (rawMessage, error) {
	release, err := s.recvGuard()
	if err != nil {
		return rawMessage{}, err
	}
	defer release()
	if s.hasPeeked {
		msg := s.peeked
		s.hasPeeked = false
		return msg, nil
	}
	return s.readRaw()
}

func (s *streamCore) peekWire() (string, error) {
	msg, err := s.peekRaw()
	if err != nil {
		return "", err
	}
	return msg.Wire, nil
}

func (s *streamCore) peekRaw() (rawMessage, error) {
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

func (s *streamCore) readRaw() (rawMessage, error) {
	timeoutNanos := s.recvTimeoutNanos.Load()
	if timeoutNanos == 0 {
		select {
		case msg, ok := <-s.inbox:
			if !ok {
				return rawMessage{}, ErrClosed{}
			}
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
		return msg, nil
	case <-s.connection.closeCh:
		return rawMessage{}, ErrClosed{}
	case <-timer.C:
		return rawMessage{}, ErrRecvTimeout{}
	}
}

func (s *streamCore) sendBoxed(msg Message) error {
	payload, err := s.connection.encodeMessage(msg)
	if err != nil {
		return err
	}
	frame := outboundFrame{streamID: s.id, seq: s.connection.nextOutboundSeq(), payload: payload}
	return s.connection.enqueueFrame(frame)
}

func (s *streamCore) inboxQ(msg rawMessage) (err error) {
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

func (s *streamCore) incomingQ(payload []byte) (err error) {
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

func (s *streamCore) handlerQ(job handlerJob) (err error) {
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

func (s *streamCore) close() {
	s.closeOnce.Do(func() {
		close(s.inbox)
		if s.incoming != nil {
			close(s.incoming)
		}
		if s.handlerCh != nil {
			close(s.handlerCh)
		}
	})
}

func (s *streamCore) protocolError(code, message string) {
	if code == "" {
		code = "protocol_error"
	}
	_ = s.sendBoxed(NewErrorReply(code, message))
	s.connection.removeStream(s.id)
}

func (s *streamCore) protocolErrorFor(err error) {
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

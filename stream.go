package adaptivemsg

import (
	"reflect"
	"time"

	"sync/atomic"
)

type streamCore struct {
	id               uint32
	connection       *Connection
	inbox            chan Message
	handlerCh        chan handlerJob
	recvTimeoutNanos atomic.Int64
	recvActive       atomic.Bool
}

type Stream[T any] struct {
	core *streamCore
}

type viewCoreProvider interface {
	viewCore() *streamCore
}

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

func (s *Stream[T]) ID() uint32 {
	return s.core.id
}

func (s *Stream[T]) Close() {
	s.core.connection.removeStream(s.core.id)
}

func (s *Stream[T]) SetRecvTimeout(timeout time.Duration) {
	s.core.setRecvTimeout(timeout)
}

func (s *Stream[T]) Send(msg Message) error {
	return s.core.sendBoxed(msg)
}

func (s *Stream[T]) SendRecvAny(msg Message) (Message, error) {
	return s.core.sendRecvAny(msg)
}

func (s *Stream[T]) SendRecv(msg Message) (T, error) {
	var zero T
	if err := ensureRegisteredForType[T](s.core.connection.registry); err != nil {
		return zero, err
	}
	reply, err := s.SendRecvAny(msg)
	if err != nil {
		return zero, err
	}
	typed, ok := reply.(T)
	if !ok {
		return zero, ErrTypeMismatch{Expected: expectedWireName[T](), Got: wireNameForValue(reply)}
	}
	return typed, nil
}

func (s *Stream[T]) Recv() (T, error) {
	var zero T
	if err := ensureRegisteredForType[T](s.core.connection.registry); err != nil {
		return zero, err
	}
	msg, err := s.core.recvMsg()
	if err != nil {
		return zero, err
	}
	typed, ok := msg.(T)
	if !ok {
		return zero, ErrTypeMismatch{Expected: expectedWireName[T](), Got: wireNameForValue(msg)}
	}
	return typed, nil
}

func (s *streamCore) setRecvTimeout(timeout time.Duration) {
	if timeout <= 0 {
		s.recvTimeoutNanos.Store(0)
		return
	}
	s.recvTimeoutNanos.Store(timeout.Nanoseconds())
}

func (s *streamCore) sendRecvAny(msg Message) (Message, error) {
	if err := s.sendBoxed(msg); err != nil {
		return nil, err
	}
	reply, err := s.recvMsg()
	if err != nil {
		return nil, err
	}
	if errReply, ok := reply.(*ErrorReply); ok {
		return nil, ErrRemote{Code: errReply.Code, Message: errReply.Message}
	}
	return reply, nil
}

func (s *streamCore) recvGuard() (func(), error) {
	if !s.recvActive.CompareAndSwap(false, true) {
		return nil, ErrConcurrentRecv{}
	}
	return func() {
		s.recvActive.Store(false)
	}, nil
}

func (s *streamCore) recvMsg() (Message, error) {
	release, err := s.recvGuard()
	if err != nil {
		return nil, err
	}
	defer release()
	timeoutNanos := s.recvTimeoutNanos.Load()
	if timeoutNanos == 0 {
		select {
		case msg, ok := <-s.inbox:
			if !ok {
				return nil, ErrClosed{}
			}
			return msg, nil
		case <-s.connection.closeCh:
			return nil, ErrClosed{}
		}
	}
	timer := time.NewTimer(time.Duration(timeoutNanos))
	defer timer.Stop()
	select {
	case msg, ok := <-s.inbox:
		if !ok {
			return nil, ErrClosed{}
		}
		return msg, nil
	case <-s.connection.closeCh:
		return nil, ErrClosed{}
	case <-timer.C:
		return nil, ErrRecvTimeout{}
	}
}

func (s *streamCore) sendBoxed(msg Message) error {
	payload, err := s.connection.encodeMessage(msg)
	if err != nil {
		return err
	}
	frame := outboundFrame{streamID: s.id, payload: payload}
	return s.connection.enqueueFrame(frame)
}

func (s *streamCore) inboxQ(msg Message) error {
	select {
	case s.inbox <- msg:
		return nil
	case <-s.connection.closeCh:
		return ErrClosed{}
	}
}

func (s *streamCore) handlerQ(job handlerJob) error {
	if s.handlerCh == nil {
		return ErrClosed{}
	}
	select {
	case s.handlerCh <- job:
		return nil
	case <-s.connection.closeCh:
		return ErrClosed{}
	}
}

func ensureRegisteredForType[T any](r *Registry) error {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() == reflect.Interface {
		return nil
	}
	return ensureRegisteredForReflectType(r, t)
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

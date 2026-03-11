package adaptivemsg

import (
	"sync"
	"sync/atomic"
)

type StreamContext struct {
	stream            *Stream[Message]
	mu                sync.Mutex
	data              any
	handlerTaskActive atomic.Bool
}

func (sc *StreamContext) SetContext(value any) {
	sc.mu.Lock()
	sc.data = value
	sc.mu.Unlock()
}

func (sc *StreamContext) GetContext() any {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.data
}

func ContextAs[T any](sc *StreamContext) (T, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	value, ok := sc.data.(T)
	var zero T
	if !ok {
		return zero, false
	}
	return value, true
}

func (sc *StreamContext) NewTask(fn func(*Stream[Message])) error {
	if !sc.handlerTaskActive.CompareAndSwap(false, true) {
		return ErrHandlerTaskBusy{}
	}
	go func() {
		defer sc.handlerTaskActive.Store(false)
		fn(sc.stream)
	}()
	return nil
}

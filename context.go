package adaptivemsg

import (
	"sync"
	"sync/atomic"
)

// StreamContext carries per-stream state for handlers.
type StreamContext struct {
	stream            *Stream[Message]
	mu                sync.Mutex
	data              any
	handlerTaskActive atomic.Bool
}

// SetContext stores a per-stream value for handlers.
func (sc *StreamContext) SetContext(value any) {
	sc.mu.Lock()
	sc.data = value
	sc.mu.Unlock()
}

// GetContext returns the raw per-stream context value.
func (sc *StreamContext) GetContext() any {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.data
}

// ContextAs retrieves a typed value from the stream context.
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

// NewTask starts a background task tied to the stream.
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

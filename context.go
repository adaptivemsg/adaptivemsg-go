package adaptivemsg

import (
	"sync"
	"sync/atomic"
)

// StreamContext carries per-stream state accessible to message handlers.
// Each stream has its own StreamContext instance. Handlers receive it as
// their first argument and can use it to store per-stream session state
// (e.g., authentication info, user ID) and to spawn a background task via
// [StreamContext.NewTask].
type StreamContext struct {
	stream            *Stream[Message]
	mu                sync.Mutex
	data              any
	handlerTaskActive atomic.Bool
}

// SetContext stores an arbitrary value in this stream's context. The value
// can later be retrieved with [StreamContext.GetContext] or [ContextAs].
// SetContext is safe for concurrent use.
func (sc *StreamContext) SetContext(value any) {
	sc.mu.Lock()
	sc.data = value
	sc.mu.Unlock()
}

// GetContext retrieves the value previously stored with [StreamContext.SetContext].
// It returns nil if no value has been set. GetContext is safe for concurrent use.
func (sc *StreamContext) GetContext() any {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.data
}

// ContextAs is a generic helper that retrieves the value stored in sc and
// type-asserts it to T. It returns (zero, false) if no value has been set
// or if the stored value is not of type T.
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

// NewTask spawns a background goroutine tied to this stream. The function
// fn receives the stream for bidirectional communication and runs until it
// returns. Only one task may be active per stream at a time; calling
// NewTask while a previous task is still running returns
// [ErrHandlerTaskBusy]. NewTask is useful for long-running handlers such
// as streaming responses.
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

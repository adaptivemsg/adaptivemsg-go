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
	stream            *Stream
	mu                sync.Mutex
	state             any
	handlerTaskActive atomic.Bool
}

// SetState stores an arbitrary per-stream value. The value can later be
// retrieved with [StreamContext.State]. SetState is safe for concurrent use.
func (sc *StreamContext) SetState(value any) {
	sc.mu.Lock()
	sc.state = value
	sc.mu.Unlock()
}

// State retrieves the value stored with [StreamContext.SetState] and
// type-asserts it to T. It returns (zero, false) if no value has been set or
// if the stored value is not of type T. State is safe for concurrent use.
func (sc *StreamContext) State[T any]() (T, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	value, ok := sc.state.(T)
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
func (sc *StreamContext) NewTask(fn func(*Stream)) error {
	if !sc.handlerTaskActive.CompareAndSwap(false, true) {
		return ErrHandlerTaskBusy{}
	}
	go func() {
		defer sc.handlerTaskActive.Store(false)
		fn(sc.stream)
	}()
	return nil
}

package adaptivemsg

import (
	"sync"
	"sync/atomic"
)

type Context struct {
	mu   sync.Mutex
	data any
}

func (c *Context) SetContext(value any) {
	c.mu.Lock()
	c.data = value
	c.mu.Unlock()
}

func (c *Context) GetContext() any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data
}

func ContextAs[T any](c *Context) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.data.(T)
	var zero T
	if !ok {
		return zero, false
	}
	return value, true
}

type StreamContext struct {
	stream            *Stream[Message]
	context           *Context
	handlerTaskActive atomic.Bool
}

func (sc *StreamContext) SetContext(value any) {
	sc.context.SetContext(value)
}

func (sc *StreamContext) Context() *Context {
	return sc.context
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

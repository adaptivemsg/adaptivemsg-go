package adaptivemsg

import (
	"reflect"
	"sync"
)

type handlerFunc func(*StreamContext, Message) (Message, error)

type registry struct {
	mu       sync.RWMutex
	handlers map[string]handlerFunc
	messages map[string]reflect.Type
}

func newRegistry() *registry {
	reg := &registry{
		handlers: make(map[string]handlerFunc),
		messages: make(map[string]reflect.Type),
	}
	_ = reg.registerMessage(&OkReply{})
	_ = reg.registerMessage(&ErrorReply{})
	return reg
}

var globalRegistry = newRegistry()

func newRegistrySnapshot() *registry {
	return cloneRegistry(globalRegistry)
}

func cloneRegistry(src *registry) *registry {
	if src == nil {
		return newRegistry()
	}
	src.mu.RLock()
	defer src.mu.RUnlock()
	reg := &registry{
		handlers: make(map[string]handlerFunc, len(src.handlers)),
		messages: make(map[string]reflect.Type, len(src.messages)),
	}
	for wire, handler := range src.handlers {
		reg.handlers[wire] = handler
	}
	for wire, typ := range src.messages {
		reg.messages[wire] = typ
	}
	return reg
}

func (r *registry) registerMessage(proto Message) error {
	t, err := messageTypeOf(proto)
	if err != nil {
		return err
	}
	wire, err := WireNameOf(proto)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.messages[wire] = t
	r.mu.Unlock()
	return nil
}

func (r *registry) registerHandler(wire string, handler handlerFunc) {
	r.mu.Lock()
	r.handlers[wire] = handler
	r.mu.Unlock()
}

func (r *registry) handler(wire string) (handlerFunc, bool) {
	r.mu.RLock()
	h, ok := r.handlers[wire]
	r.mu.RUnlock()
	return h, ok
}

func (r *registry) message(wire string) (reflect.Type, bool) {
	r.mu.RLock()
	typ, ok := r.messages[wire]
	r.mu.RUnlock()
	return typ, ok
}

func (r *registry) hasHandlers() bool {
	r.mu.RLock()
	count := len(r.handlers)
	r.mu.RUnlock()
	return count > 0
}

func newMessageForReflectType(t reflect.Type) (Message, error) {
	if t == nil {
		return nil, ErrInvalidMessage{Reason: "message type must be non-nil"}
	}
	var v reflect.Value
	switch t.Kind() {
	case reflect.Pointer:
		if t.Elem().Kind() != reflect.Struct {
			return nil, ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
		}
		v = reflect.New(t.Elem())
	case reflect.Struct:
		v = reflect.New(t)
	default:
		return nil, ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
	}
	return v.Interface(), nil
}

func newMessageForType[T any]() (Message, error) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return newMessageForReflectType(t)
}

func messageTypeOf(proto Message) (reflect.Type, error) {
	if proto == nil {
		return nil, ErrInvalidMessage{Reason: "message must be non-nil"}
	}
	rv := reflect.ValueOf(proto)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, ErrInvalidMessage{Reason: "message must be non-nil"}
	}
	t := rv.Type()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
	}
	return t, nil
}

func registerTypes(r *registry, protos ...Message) error {
	if r == nil {
		return ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	handleType := reflect.TypeOf((*interface {
		Handle(*StreamContext) (Message, error)
	})(nil)).Elem()
	for _, proto := range protos {
		if err := r.registerMessage(proto); err != nil {
			return err
		}
		protoVal := reflect.ValueOf(proto)
		if protoVal.Kind() == reflect.Pointer && protoVal.IsNil() {
			protoVal = reflect.New(protoVal.Type().Elem())
		}
		handlerVal := protoVal
		if !handlerVal.Type().Implements(handleType) {
			if handlerVal.Kind() == reflect.Struct {
				ptrVal := reflect.New(handlerVal.Type())
				ptrVal.Elem().Set(handlerVal)
				if ptrVal.Type().Implements(handleType) {
					handlerVal = ptrVal
				} else {
					continue
				}
			} else {
				continue
			}
		}
		wire, err := WireNameOf(handlerVal.Interface())
		if err != nil {
			return err
		}
		wireName := wire
		r.registerHandler(wireName, func(ctx *StreamContext, m Message) (Message, error) {
			handler, ok := m.(interface {
				Handle(*StreamContext) (Message, error)
			})
			if !ok {
				return nil, ErrTypeMismatch{Expected: wireName, Got: wireNameForValue(m)}
			}
			return handler.Handle(ctx)
		})
	}
	return nil
}

func registerGlobalTypes(protos ...Message) error {
	return registerTypes(globalRegistry, protos...)
}

// RegisterGlobalType registers a message type (and its handler, if implemented) globally.
func RegisterGlobalType[T any]() error {
	msg, err := newMessageForType[T]()
	if err != nil {
		return err
	}
	return registerGlobalTypes(msg)
}

// MustRegisterGlobalType registers a type globally and panics on failure.
func MustRegisterGlobalType[T any]() struct{} {
	if err := RegisterGlobalType[T](); err != nil {
		panic(err)
	}
	return struct{}{}
}

package adaptivemsg

import (
	"reflect"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

type handlerFunc func(*StreamContext, Message) (Message, error)

type registry struct {
	mu       sync.RWMutex
	handlers map[string]handlerFunc
	messages map[string]*messageFactory
}

type messageFactory struct {
	typ    reflect.Type
	fields []int
}

func newRegistry() *registry {
	reg := &registry{
		handlers: make(map[string]handlerFunc),
		messages: make(map[string]*messageFactory),
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
		messages: make(map[string]*messageFactory, len(src.messages)),
	}
	for wire, handler := range src.handlers {
		reg.handlers[wire] = handler
	}
	for wire, factory := range src.messages {
		fields := make([]int, len(factory.fields))
		copy(fields, factory.fields)
		reg.messages[wire] = &messageFactory{typ: factory.typ, fields: fields}
	}
	return reg
}

func (r *registry) registerMessage(proto Message) error {
	wire, factory, err := newMessageFactory(proto)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.messages[wire] = factory
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

func (r *registry) message(wire string) (*messageFactory, bool) {
	r.mu.RLock()
	factory, ok := r.messages[wire]
	r.mu.RUnlock()
	return factory, ok
}

func (r *registry) hasHandlers() bool {
	r.mu.RLock()
	count := len(r.handlers)
	r.mu.RUnlock()
	return count > 0
}

func newMessageFactory(proto Message) (string, *messageFactory, error) {
	t, err := messageTypeOf(proto)
	if err != nil {
		return "", nil, err
	}
	wire, err := WireNameOf(proto)
	if err != nil {
		return "", nil, err
	}
	fields, err := compactFieldIndices(t)
	if err != nil {
		return "", nil, err
	}
	return wire, &messageFactory{typ: t, fields: fields}, nil
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

func RegisterGlobalTypes(protos ...Message) error {
	return registerTypes(globalRegistry, protos...)
}

func RegisterGlobalType[T any]() error {
	msg, err := newMessageForType[T]()
	if err != nil {
		return err
	}
	return RegisterGlobalTypes(msg)
}

func MustRegisterGlobalType[T any]() struct{} {
	if err := RegisterGlobalType[T](); err != nil {
		panic(err)
	}
	return struct{}{}
}

func (f *messageFactory) decodeMap(raw msgpack.RawMessage) (Message, error) {
	msg := reflect.New(f.typ)
	if err := msgpack.Unmarshal(raw, msg.Interface()); err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	return msg.Interface(), nil
}

func (f *messageFactory) decodeCompact(values []msgpack.RawMessage) (Message, error) {
	if len(values) != len(f.fields) {
		return nil, ErrCompactFieldCount{Expected: len(f.fields), Got: len(values)}
	}
	msg := reflect.New(f.typ)
	msgValue := msg.Elem()
	for i, fieldIndex := range f.fields {
		field := msgValue.Field(fieldIndex)
		if !field.CanSet() {
			return nil, ErrInvalidMessage{Reason: "compact decode requires exported fields"}
		}
		if err := msgpack.Unmarshal(values[i], field.Addr().Interface()); err != nil {
			return nil, ErrCodec{Message: err.Error()}
		}
	}
	return msg.Interface(), nil
}

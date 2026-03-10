package adaptivemsg

import (
	"reflect"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
)

type HandlerFunc func(*StreamContext, Message) (Message, error)

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
	messages map[string]*messageFactory
}

type messageFactory struct {
	typ    reflect.Type
	fields []int
}

func NewRegistry() *Registry {
	reg := &Registry{
		handlers: make(map[string]HandlerFunc),
		messages: make(map[string]*messageFactory),
	}
	_ = reg.RegisterMessage(&OkReply{})
	_ = reg.RegisterMessage(&ErrorReply{})
	return reg
}

var globalRegistry = newGlobalRegistry()

func newGlobalRegistry() *Registry {
	reg := &Registry{
		handlers: make(map[string]HandlerFunc),
		messages: make(map[string]*messageFactory),
	}
	_ = reg.RegisterMessage(&OkReply{})
	_ = reg.RegisterMessage(&ErrorReply{})
	return reg
}

func NewServerRegistry() *Registry {
	return cloneRegistry(globalRegistry)
}

func cloneRegistry(src *Registry) *Registry {
	if src == nil {
		return NewRegistry()
	}
	src.mu.RLock()
	defer src.mu.RUnlock()
	reg := &Registry{
		handlers: make(map[string]HandlerFunc, len(src.handlers)),
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

func (r *Registry) RegisterMessage(proto Message) error {
	wire, factory, err := newMessageFactory(proto)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.messages[wire] = factory
	r.mu.Unlock()
	return nil
}

func (r *Registry) RegisterHandler(wire string, handler HandlerFunc) {
	r.mu.Lock()
	r.handlers[wire] = handler
	r.mu.Unlock()
}

func (r *Registry) Handler(wire string) (HandlerFunc, bool) {
	r.mu.RLock()
	h, ok := r.handlers[wire]
	r.mu.RUnlock()
	return h, ok
}

func (r *Registry) Message(wire string) (*messageFactory, bool) {
	r.mu.RLock()
	factory, ok := r.messages[wire]
	r.mu.RUnlock()
	return factory, ok
}

func (r *Registry) HasHandlers() bool {
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

func RegisterTypes(r *Registry, protos ...Message) error {
	if r == nil {
		return ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	for _, proto := range protos {
		if err := r.RegisterMessage(proto); err != nil {
			return err
		}
	}
	return nil
}

func EnsureRegistered[T any](r *Registry) error {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return ensureRegisteredForReflectType(r, t)
}

func Handle[T any](r *Registry, handler func(*StreamContext, *T) (Message, error)) error {
	if r == nil {
		return ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() == reflect.Pointer {
		return ErrInvalidMessage{Reason: "Handle expects a non-pointer type parameter"}
	}
	msg := new(T)
	if err := r.RegisterMessage(msg); err != nil {
		return err
	}
	wire, err := WireNameOf(msg)
	if err != nil {
		return err
	}
	r.RegisterHandler(wire, func(ctx *StreamContext, m Message) (Message, error) {
		typed, ok := m.(*T)
		if !ok {
			return nil, ErrTypeMismatch{Expected: wire, Got: wireNameForValue(m)}
		}
		return handler(ctx, typed)
	})
	return nil
}

func RegisterGlobalMethods(protos ...Message) error {
	return HandleMethods(globalRegistry, protos...)
}

func RegisterGlobalMethod[T any]() error {
	msg, err := newMessageForType[T]()
	if err != nil {
		return err
	}
	return RegisterGlobalMethods(msg)
}

func MustRegisterGlobalMethod[T any]() struct{} {
	if err := RegisterGlobalMethod[T](); err != nil {
		panic(err)
	}
	return struct{}{}
}

func HandleMethods(r *Registry, protos ...Message) error {
	if r == nil {
		return ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	handleType := reflect.TypeOf((*interface {
		Handle(*StreamContext) (Message, error)
	})(nil)).Elem()
	for _, proto := range protos {
		if proto == nil {
			return ErrInvalidMessage{Reason: "message must be non-nil"}
		}
		protoVal := reflect.ValueOf(proto)
		if protoVal.Kind() == reflect.Pointer && protoVal.IsNil() {
			protoVal = reflect.New(protoVal.Type().Elem())
		}
		if !protoVal.Type().Implements(handleType) {
			if protoVal.Kind() == reflect.Struct {
				ptrVal := reflect.New(protoVal.Type())
				ptrVal.Elem().Set(protoVal)
				if ptrVal.Type().Implements(handleType) {
					protoVal = ptrVal
				} else {
					return ErrInvalidMessage{Reason: "message must implement Handle(*StreamContext) (Message, error)"}
				}
			} else {
				return ErrInvalidMessage{Reason: "message must implement Handle(*StreamContext) (Message, error)"}
			}
		}
		protoMsg := protoVal.Interface()
		if err := r.RegisterMessage(protoMsg); err != nil {
			return err
		}
		wire, err := WireNameOf(protoMsg)
		if err != nil {
			return err
		}
		wireName := wire
		r.RegisterHandler(wireName, func(ctx *StreamContext, m Message) (Message, error) {
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

package adaptivemsg

import (
	"reflect"
	"strings"
)

type Message interface{}

type NamedMessage interface {
	WireName() string
}

type OkReply struct{}

func (*OkReply) WireName() string {
	return "am.message.OkReply"
}

type ErrorReply struct {
	Code    string `msgpack:"code"`
	Message string `msgpack:"message"`
}

func (*ErrorReply) WireName() string {
	return "am.message.ErrorReply"
}

func NewErrorReply(code, message string) *ErrorReply {
	return &ErrorReply{Code: code, Message: message}
}

func (e *ErrorReply) ErrorCode() string {
	return e.Code
}

func (e *ErrorReply) ErrorMessage() string {
	return e.Message
}

func WireNameOf(msg Message) (string, error) {
	if msg == nil {
		return "", ErrInvalidMessage{Reason: "message must be non-nil"}
	}
	if named, ok := msg.(NamedMessage); ok {
		name := named.WireName()
		if name == "" {
			return "", ErrInvalidMessage{Reason: "message wire name must be non-empty"}
		}
		return name, nil
	}
	t := reflect.TypeOf(msg)
	if t != nil && t.Kind() == reflect.Struct {
		ptr := reflect.New(t)
		if named, ok := ptr.Interface().(NamedMessage); ok {
			name := named.WireName()
			if name == "" {
				return "", ErrInvalidMessage{Reason: "message wire name must be non-empty"}
			}
			return name, nil
		}
	}
	return defaultWireNameForValue("am", msg)
}

func DefaultWireName(ns string, msg Message) string {
	name, _ := defaultWireNameForValue(ns, msg)
	return name
}

func defaultWireNameForValue(ns string, msg Message) (string, error) {
	if msg == nil {
		return "", ErrInvalidMessage{Reason: "message must be non-nil"}
	}
	return defaultWireNameForType(ns, reflect.TypeOf(msg))
}

func defaultWireNameForType(ns string, t reflect.Type) (string, error) {
	if t == nil {
		return "", ErrInvalidMessage{Reason: "message type must be non-nil"}
	}
	if ns == "" {
		ns = "am"
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return "", ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
	}
	pkgPath := t.PkgPath()
	leaf := "unknown"
	if pkgPath != "" {
		parts := strings.Split(pkgPath, "/")
		leaf = parts[len(parts)-1]
	}
	name := t.Name()
	if name == "" {
		name = "unknown"
	}
	return ns + "." + leaf + "." + name, nil
}

func wireNameForValue(msg Message) string {
	name, err := WireNameOf(msg)
	if err == nil && name != "" {
		return name
	}
	if msg == nil {
		return "nil"
	}
	return reflect.TypeOf(msg).String()
}

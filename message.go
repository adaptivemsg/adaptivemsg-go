package adaptivemsg

import (
	"reflect"
	"strings"
	"sync"
)

type wireNameCacheKey struct {
	ns string
	t  reflect.Type
}

var defaultWireNameCache sync.Map

// Message is the marker interface for payload types.
type Message interface{}

// NamedMessage overrides the default wire name for a message.
type NamedMessage interface {
	WireName() string
}

// OkReply is the default acknowledgement for handlers.
type OkReply struct{}

func (*OkReply) WireName() string {
	return "am.message.OkReply"
}

// ErrorReply is the standard error payload sent over the wire.
type ErrorReply struct {
	Code    string `am:"code"`
	Message string `am:"message"`
}

func (*ErrorReply) WireName() string {
	return "am.message.ErrorReply"
}

// NewErrorReply builds an ErrorReply with the given code and message.
func NewErrorReply(code, message string) *ErrorReply {
	return &ErrorReply{Code: code, Message: message}
}

// WireNameOf returns the wire name for a message value.
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
	cacheKey := wireNameCacheKey{ns: ns, t: t}
	if cached, ok := defaultWireNameCache.Load(cacheKey); ok {
		return cached.(string), nil
	}
	pkgPath := t.PkgPath()
	leaf := "unknown"
	if pkgPath != "" {
		parts := strings.Split(pkgPath, "/")
		leaf = strings.TrimSuffix(parts[len(parts)-1], "-go")
	}
	name := t.Name()
	if name == "" {
		name = "unknown"
	}
	wire := ns + "." + leaf + "." + name
	defaultWireNameCache.Store(cacheKey, wire)
	return wire, nil
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

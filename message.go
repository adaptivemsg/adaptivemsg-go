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

// Message is the marker interface that all wire payload types must satisfy.
// Any struct or pointer-to-struct value implicitly implements Message. The
// wire name used during encoding is derived automatically from the Go type
// name (namespace.package.TypeName) unless the type also implements
// [NamedMessage] to supply a custom name.
type Message interface{}

// NamedMessage is an optional interface that lets a [Message] type override
// the default wire name. Implement WireName to return a fixed string that
// identifies this message type on the wire. This is required for
// cross-language compatibility — the name returned must match the
// corresponding Rust wire name exactly.
type NamedMessage interface {
	WireName() string
}

// OkReply is the default acknowledgement sent when a message handler
// returns nil. Its wire name is "am.message.OkReply".
type OkReply struct{}

func (*OkReply) WireName() string {
	return "am.message.OkReply"
}

// ErrorReply is the standard error payload sent over the wire when a handler
// returns an error or when an explicit error must be communicated to the
// peer. Its wire name is "am.message.ErrorReply". On the receiving side an
// ErrorReply is surfaced as [ErrRemote] by [Stream.SendRecv] and
// [SendRecvAs].
type ErrorReply struct {
	Code    string `am:"code"`
	Message string `am:"message"`
}

func (*ErrorReply) WireName() string {
	return "am.message.ErrorReply"
}

// NewErrorReply is a convenience constructor that returns an [ErrorReply]
// with the given code and human-readable message.
func NewErrorReply(code, message string) *ErrorReply {
	return &ErrorReply{Code: code, Message: message}
}

// WireNameOf returns the wire name for a message value. If msg implements
// [NamedMessage], its WireName method is used. Otherwise the name is derived
// from the Go type information (namespace.package.TypeName). WireNameOf
// returns [ErrInvalidMessage] if msg is nil or not a struct (or pointer to
// struct).
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

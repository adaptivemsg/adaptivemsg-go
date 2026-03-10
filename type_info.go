package adaptivemsg

import (
	"reflect"
	"sync"
)

type typeInfo struct {
	wire  string
	proto Message
	err   error
}

var typeInfoCache sync.Map

func messageTypeInfo(t reflect.Type) *typeInfo {
	if t == nil {
		return &typeInfo{err: ErrInvalidMessage{Reason: "message type must be non-nil"}}
	}
	if cached, ok := typeInfoCache.Load(t); ok {
		return cached.(*typeInfo)
	}
	info := &typeInfo{}
	proto, err := newMessageForReflectType(t)
	if err != nil {
		info.err = err
		info.wire = t.String()
		typeInfoCache.Store(t, info)
		return info
	}
	info.proto = proto
	wire, err := WireNameOf(proto)
	if err != nil {
		info.err = err
		info.wire = t.String()
		typeInfoCache.Store(t, info)
		return info
	}
	info.wire = wire
	typeInfoCache.Store(t, info)
	return info
}

func ensureRegisteredForReflectType(r *Registry, t reflect.Type) error {
	if r == nil {
		return ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	info := messageTypeInfo(t)
	if info.err != nil {
		return info.err
	}
	if _, ok := r.Message(info.wire); ok {
		return nil
	}
	return r.RegisterMessage(info.proto)
}

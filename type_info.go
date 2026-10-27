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

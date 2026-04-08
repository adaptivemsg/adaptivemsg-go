package adaptivemsg

import "sync"

var codecRegistry = struct {
	mu     sync.RWMutex
	codecs map[CodecID]CodecImpl
}{
	codecs: make(map[CodecID]CodecImpl),
}

// RegisterCodec installs a codec implementation in the global codec registry.
// The codec's ID must be non-zero and not already registered. RegisterCodec
// is safe for concurrent use. Built-in codecs (compact, map) are registered
// automatically via init().
func RegisterCodec(codec CodecImpl) error {
	if codec == nil {
		return ErrInvalidMessage{Reason: "codec must be non-nil"}
	}
	id := codec.ID()
	if id == 0 {
		return ErrInvalidMessage{Reason: "codec ID must be non-zero"}
	}
	if codec.Name() == "" {
		return ErrInvalidMessage{Reason: "codec name must be non-empty"}
	}
	codecRegistry.mu.Lock()
	defer codecRegistry.mu.Unlock()
	if _, exists := codecRegistry.codecs[id]; exists {
		return ErrInvalidMessage{Reason: "codec already registered"}
	}
	codecRegistry.codecs[id] = codec
	return nil
}

// MustRegisterCodec is like [RegisterCodec] but panics on error. It returns
// an empty struct so it can be used as a package-level variable for init-time
// registration.
func MustRegisterCodec(codec CodecImpl) struct{} {
	if err := RegisterCodec(codec); err != nil {
		panic(err)
	}
	return struct{}{}
}

func codecByID(id CodecID) (CodecImpl, bool) {
	codecRegistry.mu.RLock()
	codec, ok := codecRegistry.codecs[id]
	codecRegistry.mu.RUnlock()
	return codec, ok
}

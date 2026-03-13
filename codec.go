package adaptivemsg

import "sync"

// CodecID identifies a payload encoding format.
type CodecID byte

const (
	// CodecMsgpackCompact encodes messages as a MessagePack compact array envelope.
	CodecMsgpackCompact CodecID = 1
	// CodecMsgpackMap encodes messages as a MessagePack map envelope.
	CodecMsgpackMap CodecID = 2
)

// String returns the human-readable codec name.
func (c CodecID) String() string {
	switch c {
	case CodecMsgpackCompact:
		return "compact"
	case CodecMsgpackMap:
		return "map"
	default:
		return "unknown"
	}
}

// Envelope is a codec-specific envelope that preserves the wire name and raw body.
type Envelope struct {
	Wire string
	Body any
}

// CodecImpl defines a codec that can extract wire names without full decode.
type CodecImpl interface {
	ID() CodecID
	Name() string
	Encode(Message) ([]byte, error)
	DecodeEnvelope([]byte) (Envelope, error)
	DecodeInto(body any, dst any) error
}

var codecRegistry = struct {
	mu     sync.RWMutex
	codecs map[CodecID]CodecImpl
}{
	codecs: make(map[CodecID]CodecImpl),
}

// RegisterCodec installs a codec implementation globally.
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

// MustRegisterCodec registers a codec and panics on failure.
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

package adaptivemsg

// CodecID is a numeric identifier for a payload encoding format.
// Built-in values (compact, map) are defined in codec_msgpack.go.
// Custom codecs can be registered via [RegisterCodec].
type CodecID byte

// String returns the human-readable codec name (e.g. "compact", "map")
// or "unknown" if the codec ID is not registered.
func (c CodecID) String() string {
	if codec, ok := codecByID(c); ok {
		if name := codec.Name(); name != "" {
			return name
		}
	}
	return "unknown"
}

// Envelope is an intermediate decode result that preserves the wire name and
// raw body without fully deserializing the message payload. Wire is the
// message type name. Body holds codec-specific raw data: for map codec it is
// a msgpack.RawMessage, for compact codec it is a []msgpack.RawMessage.
type Envelope struct {
	// Wire is the message type name extracted from the payload.
	Wire string
	// Body holds the codec-specific raw data for deferred decoding.
	Body any
}

// CodecImpl is the interface for pluggable payload codecs. Implementations
// must be safe for concurrent use by multiple goroutines. Encode serializes
// a message and returns a caller-owned byte slice. DecodeEnvelope extracts
// the wire name and raw body without full deserialization. DecodeInto
// decodes a raw body (from an [Envelope]) into a destination struct.
type CodecImpl interface {
	ID() CodecID
	Name() string
	// Encode returns a payload owned by the caller. Implementations must not
	// mutate or reuse the returned backing storage after Encode returns.
	Encode(Message) ([]byte, error)
	DecodeEnvelope([]byte) (Envelope, error)
	DecodeInto(body any, dst any) error
}

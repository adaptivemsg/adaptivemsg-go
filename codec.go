package adaptivemsg

// CodecID identifies a payload encoding format.
type CodecID byte

// String returns the registered codec name or "unknown".
func (c CodecID) String() string {
	if codec, ok := codecByID(c); ok {
		if name := codec.Name(); name != "" {
			return name
		}
	}
	return "unknown"
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

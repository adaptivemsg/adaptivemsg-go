package adaptivemsg

import (
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
)

// Codec selects the payload encoding format.
type Codec uint8

const (
	// CodecCompact encodes messages as a compact array envelope.
	CodecCompact Codec = 1
	// CodecMap encodes messages as a map envelope.
	CodecMap     Codec = 2
)

// String returns the human-readable codec name.
func (c Codec) String() string {
	switch c {
	case CodecCompact:
		return "compact"
	case CodecMap:
		return "map"
	default:
		return "unknown"
	}
}

func (c Codec) toByte() byte {
	return byte(c)
}

func codecFromByte(value byte) (Codec, bool) {
	switch value {
	case byte(CodecCompact):
		return CodecCompact, true
	case byte(CodecMap):
		return CodecMap, true
	default:
		return 0, false
	}
}

type mapEnvelope struct {
	Type string `msgpack:"type"`
	Data any    `msgpack:"data"`
}

type mapEnvelopeRaw struct {
	Type string             `msgpack:"type"`
	Data msgpack.RawMessage `msgpack:"data"`
}

func encodeMap(msg Message) ([]byte, error) {
	wire, err := WireNameOf(msg)
	if err != nil {
		return nil, err
	}
	env := mapEnvelope{
		Type: wire,
		Data: msg,
	}
	payload, err := msgpack.Marshal(&env)
	if err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	return payload, nil
}

func decodeMapEnvelope(payload []byte) (string, msgpack.RawMessage, error) {
	var env mapEnvelopeRaw
	if err := msgpack.Unmarshal(payload, &env); err != nil {
		return "", nil, ErrCodec{Message: err.Error()}
	}
	if env.Type == "" {
		return "", nil, ErrCodec{Message: "map payload missing type"}
	}
	return env.Type, env.Data, nil
}

func encodeCompact(msg Message) ([]byte, error) {
	if msg == nil {
		return nil, ErrCodec{Message: "compact encode requires non-nil message"}
	}
	wire, err := WireNameOf(msg)
	if err != nil {
		return nil, err
	}
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, ErrCodec{Message: "compact encode requires non-nil message"}
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, ErrCodec{Message: "compact encode requires struct message"}
	}
	indices, err := compactFieldIndices(rv.Type())
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, 1+len(indices))
	items = append(items, wire)
	for _, idx := range indices {
		items = append(items, rv.Field(idx).Interface())
	}
	payload, err := msgpack.Marshal(items)
	if err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	return payload, nil
}

func decodeCompactEnvelope(payload []byte) (string, []msgpack.RawMessage, error) {
	var rawItems []msgpack.RawMessage
	if err := msgpack.Unmarshal(payload, &rawItems); err != nil {
		return "", nil, ErrCodec{Message: err.Error()}
	}
	if len(rawItems) == 0 {
		return "", nil, ErrCodec{Message: "compact payload must be a non-empty array"}
	}
	var name string
	if err := msgpack.Unmarshal(rawItems[0], &name); err != nil {
		return "", nil, ErrCodec{Message: "compact message name must be a string"}
	}
	return name, rawItems[1:], nil
}

func compactFieldIndices(t reflect.Type) ([]int, error) {
	if t.Kind() != reflect.Struct {
		return nil, ErrInvalidMessage{Reason: "compact encoding requires struct type"}
	}
	indices := make([]int, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			return nil, ErrInvalidMessage{Reason: "compact encoding does not support unexported fields"}
		}
		indices = append(indices, i)
	}
	return indices, nil
}

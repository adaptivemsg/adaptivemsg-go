package adaptivemsg

import (
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
)

type msgpackMapCodec struct{}

func init() {
	_ = RegisterCodec(msgpackMapCodec{})
	_ = RegisterCodec(msgpackCompactCodec{})
}

func (msgpackMapCodec) ID() CodecID {
	return CodecMsgpackMap
}

func (msgpackMapCodec) Name() string {
	return "map"
}

func (msgpackMapCodec) Encode(msg Message) ([]byte, error) {
	return encodeMap(msg)
}

func (msgpackMapCodec) DecodeEnvelope(payload []byte) (Envelope, error) {
	wire, data, err := decodeMapEnvelope(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Wire: wire, Body: data}, nil
}

func (msgpackMapCodec) DecodeInto(body any, dst any) error {
	raw, ok := body.(msgpack.RawMessage)
	if !ok {
		return ErrCodec{Message: "map body must be msgpack raw message"}
	}
	if err := msgpack.Unmarshal(raw, dst); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	return nil
}

type msgpackCompactCodec struct{}

func (msgpackCompactCodec) ID() CodecID {
	return CodecMsgpackCompact
}

func (msgpackCompactCodec) Name() string {
	return "compact"
}

func (msgpackCompactCodec) Encode(msg Message) ([]byte, error) {
	return encodeCompact(msg)
}

func (msgpackCompactCodec) DecodeEnvelope(payload []byte) (Envelope, error) {
	wire, values, err := decodeCompactEnvelope(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Wire: wire, Body: values}, nil
}

func (msgpackCompactCodec) DecodeInto(body any, dst any) error {
	raw, ok := body.([]msgpack.RawMessage)
	if !ok {
		return ErrCodec{Message: "compact body must be msgpack raw message list"}
	}
	return decodeCompactInto(raw, dst)
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

func decodeCompactInto(raw []msgpack.RawMessage, dst any) error {
	if dst == nil {
		return ErrInvalidMessage{Reason: "compact decode requires non-nil destination"}
	}
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return ErrInvalidMessage{Reason: "compact decode requires pointer destination"}
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return ErrInvalidMessage{Reason: "compact decode requires struct destination"}
	}
	fields, err := compactFieldIndices(elem.Type())
	if err != nil {
		return err
	}
	if len(raw) != len(fields) {
		return ErrCompactFieldCount{Expected: len(fields), Got: len(raw)}
	}
	for i, fieldIndex := range fields {
		field := elem.Field(fieldIndex)
		if !field.CanSet() {
			return ErrInvalidMessage{Reason: "compact decode requires exported fields"}
		}
		if err := msgpack.Unmarshal(raw[i], field.Addr().Interface()); err != nil {
			return ErrCodec{Message: err.Error()}
		}
	}
	return nil
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

package adaptivemsg

import (
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
)

type rawMessage struct {
	Wire    string
	Codec   Codec
	Map     msgpack.RawMessage
	Compact []msgpack.RawMessage
}

func decodeRawAs[T any](raw rawMessage) (T, error) {
	var zero T
	t := reflect.TypeOf((*T)(nil)).Elem()
	if t.Kind() == reflect.Interface {
		return zero, ErrInvalidMessage{Reason: "message type must be concrete"}
	}
	expected := expectedWireName[T]()
	if raw.Wire != expected {
		return zero, ErrTypeMismatch{Expected: expected, Got: raw.Wire}
	}
	msgPtr, err := newMessageValueForType(t)
	if err != nil {
		return zero, err
	}
	if err := decodeRawIntoValue(raw, msgPtr); err != nil {
		return zero, err
	}
	if t.Kind() == reflect.Pointer {
		return msgPtr.Interface().(T), nil
	}
	return msgPtr.Elem().Interface().(T), nil
}

func newRawMessageFromPayload(codec Codec, payload []byte) (rawMessage, error) {
	switch codec {
	case CodecMap:
		wire, data, err := decodeMapEnvelope(payload)
		if err != nil {
			return rawMessage{}, err
		}
		return rawMessage{Wire: wire, Codec: CodecMap, Map: data}, nil
	case CodecCompact:
		wire, values, err := decodeCompactEnvelope(payload)
		if err != nil {
			return rawMessage{}, err
		}
		return rawMessage{Wire: wire, Codec: CodecCompact, Compact: values}, nil
	default:
		return rawMessage{}, ErrUnsupportedCodec{Value: codec.toByte()}
	}
}

func decodeRawWithRegistry(raw rawMessage, reg *registry) (Message, error) {
	if reg == nil {
		return nil, ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	factory, ok := reg.message(raw.Wire)
	if !ok {
		return nil, ErrUnknownMessage{Name: raw.Wire}
	}
	switch raw.Codec {
	case CodecMap:
		return factory.decodeMap(raw.Map)
	case CodecCompact:
		return factory.decodeCompact(raw.Compact)
	default:
		return nil, ErrUnsupportedCodec{Value: raw.Codec.toByte()}
	}
}

func newMessageValueForType(t reflect.Type) (reflect.Value, error) {
	switch t.Kind() {
	case reflect.Pointer:
		if t.Elem().Kind() != reflect.Struct {
			return reflect.Value{}, ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
		}
		return reflect.New(t.Elem()), nil
	case reflect.Struct:
		return reflect.New(t), nil
	default:
		return reflect.Value{}, ErrInvalidMessage{Reason: "message must be struct or pointer to struct"}
	}
}

func decodeRawIntoValue(raw rawMessage, msgPtr reflect.Value) error {
	switch raw.Codec {
	case CodecMap:
		if err := msgpack.Unmarshal(raw.Map, msgPtr.Interface()); err != nil {
			return ErrCodec{Message: err.Error()}
		}
		return nil
	case CodecCompact:
		t := msgPtr.Type()
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		fields, err := compactFieldIndices(t)
		if err != nil {
			return err
		}
		if len(raw.Compact) != len(fields) {
			return ErrCompactFieldCount{Expected: len(fields), Got: len(raw.Compact)}
		}
		msgValue := msgPtr.Elem()
		for i, fieldIndex := range fields {
			field := msgValue.Field(fieldIndex)
			if !field.CanSet() {
				return ErrInvalidMessage{Reason: "compact decode requires exported fields"}
			}
			if err := msgpack.Unmarshal(raw.Compact[i], field.Addr().Interface()); err != nil {
				return ErrCodec{Message: err.Error()}
			}
		}
		return nil
	default:
		return ErrUnsupportedCodec{Value: raw.Codec.toByte()}
	}
}

package adaptivemsg

import "reflect"

type rawMessage struct {
	Wire  string
	Codec CodecID
	Body  any
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
	codec, ok := codecByID(raw.Codec)
	if !ok {
		return zero, ErrUnsupportedCodec{Value: byte(raw.Codec)}
	}
	if err := codec.DecodeInto(raw.Body, msgPtr.Interface()); err != nil {
		return zero, err
	}
	if t.Kind() == reflect.Pointer {
		return msgPtr.Interface().(T), nil
	}
	return msgPtr.Elem().Interface().(T), nil
}

func newRawMessageFromPayload(codecID CodecID, payload []byte) (rawMessage, error) {
	codec, ok := codecByID(codecID)
	if !ok {
		return rawMessage{}, ErrUnsupportedCodec{Value: byte(codecID)}
	}
	env, err := codec.DecodeEnvelope(payload)
	if err != nil {
		return rawMessage{}, err
	}
	return rawMessage{Wire: env.Wire, Codec: codecID, Body: env.Body}, nil
}

func decodeRawWithRegistry(raw rawMessage, reg *registry) (Message, error) {
	if reg == nil {
		return nil, ErrInvalidMessage{Reason: "registry must be non-nil"}
	}
	t, ok := reg.message(raw.Wire)
	if !ok {
		return nil, ErrUnknownMessage{Name: raw.Wire}
	}
	msg, err := newMessageForReflectType(t)
	if err != nil {
		return nil, err
	}
	codec, ok := codecByID(raw.Codec)
	if !ok {
		return nil, ErrUnsupportedCodec{Value: byte(raw.Codec)}
	}
	if err := codec.DecodeInto(raw.Body, msg); err != nil {
		return nil, err
	}
	return msg, nil
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

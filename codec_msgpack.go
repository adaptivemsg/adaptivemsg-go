package adaptivemsg

import (
	"bytes"
	"encoding"
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

const (
	// CodecMsgpackCompact encodes messages as a MessagePack compact array envelope.
	CodecMsgpackCompact CodecID = 1
	// CodecMsgpackMap encodes messages as a MessagePack map envelope.
	CodecMsgpackMap CodecID = 2
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
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(dst); err != nil {
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
	Type string `am:"type"`
	Data any    `am:"data"`
}

type mapEnvelopeRaw struct {
	Type string             `am:"type"`
	Data msgpack.RawMessage `am:"data"`
}

var (
	msgpackMarshalerType   = reflect.TypeOf((*msgpack.Marshaler)(nil)).Elem()
	msgpackUnmarshalerType = reflect.TypeOf((*msgpack.Unmarshaler)(nil)).Elem()
	msgpackCustomEncType   = reflect.TypeOf((*msgpack.CustomEncoder)(nil)).Elem()
	msgpackCustomDecType   = reflect.TypeOf((*msgpack.CustomDecoder)(nil)).Elem()
	binaryMarshalerType    = reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem()
	binaryUnmarshalerType  = reflect.TypeOf((*encoding.BinaryUnmarshaler)(nil)).Elem()
	textMarshalerType      = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType    = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func encodeMap(msg Message) ([]byte, error) {
	wire, err := WireNameOf(msg)
	if err != nil {
		return nil, err
	}
	env := mapEnvelope{
		Type: wire,
		Data: msg,
	}
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetCustomStructTag("am")
	if err := enc.Encode(&env); err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	payload := buf.Bytes()
	if err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	return payload, nil
}

func decodeMapEnvelope(payload []byte) (string, msgpack.RawMessage, error) {
	var env mapEnvelopeRaw
	dec := msgpack.NewDecoder(bytes.NewReader(payload))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(&env); err != nil {
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
		value, err := compactEncodeValue(rv.Field(idx))
		if err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	enc.SetCustomStructTag("am")
	if err := enc.Encode(items); err != nil {
		return nil, ErrCodec{Message: err.Error()}
	}
	return buf.Bytes(), nil
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
		if err := decodeCompactValue(raw[i], field); err != nil {
			return err
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

func compactEncodeValue(value reflect.Value) (any, error) {
	if !value.IsValid() {
		return nil, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil, nil
		}
		return compactEncodeValue(value.Elem())
	case reflect.Pointer:
		if value.IsNil() {
			return nil, nil
		}
		elem := value.Elem()
		if elem.Kind() == reflect.Struct && !isCompactStructType(elem.Type()) {
			return value.Interface(), nil
		}
		return compactEncodeValue(elem)
	case reflect.Struct:
		if !isCompactStructType(value.Type()) {
			if value.CanAddr() && implementsAnyPointer(value.Type(),
				msgpackMarshalerType,
				msgpackUnmarshalerType,
				msgpackCustomEncType,
				msgpackCustomDecType,
				binaryMarshalerType,
				binaryUnmarshalerType,
				textMarshalerType,
				textUnmarshalerType,
			) {
				return value.Addr().Interface(), nil
			}
			return value.Interface(), nil
		}
		indices, err := compactFieldIndices(value.Type())
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(indices))
		for _, idx := range indices {
			item, err := compactEncodeValue(value.Field(idx))
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case reflect.Slice:
		if value.IsNil() {
			return nil, nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			buf := make([]byte, value.Len())
			reflect.Copy(reflect.ValueOf(buf), value)
			return buf, nil
		}
		items := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			item, err := compactEncodeValue(value.Index(i))
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return items, nil
	case reflect.Array:
		items := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			item, err := compactEncodeValue(value.Index(i))
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return items, nil
	case reflect.Map:
		if value.IsNil() {
			return nil, nil
		}
		keyType := value.Type().Key()
		if keyType.Kind() == reflect.Struct || (keyType.Kind() == reflect.Pointer && keyType.Elem().Kind() == reflect.Struct) {
			return nil, ErrInvalidMessage{Reason: "compact encoding does not support struct map keys"}
		}
		out := make(map[any]any, value.Len())
		for _, key := range value.MapKeys() {
			encodedKey, err := compactEncodeValue(key)
			if err != nil {
				return nil, err
			}
			if !isComparableKey(encodedKey) {
				return nil, ErrInvalidMessage{Reason: "compact encoding requires comparable map keys"}
			}
			encodedValue, err := compactEncodeValue(value.MapIndex(key))
			if err != nil {
				return nil, err
			}
			out[encodedKey] = encodedValue
		}
		return out, nil
	default:
		return value.Interface(), nil
	}
}

func decodeCompactValue(raw msgpack.RawMessage, dst reflect.Value) error {
	if !dst.CanSet() {
		return ErrInvalidMessage{Reason: "compact decode requires settable destination"}
	}
	switch dst.Kind() {
	case reflect.Interface:
		if isNilRaw(raw) {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		var out any
		dec := msgpack.NewDecoder(bytes.NewReader(raw))
		dec.SetCustomStructTag("am")
		if err := dec.Decode(&out); err != nil {
			return ErrCodec{Message: err.Error()}
		}
		dst.Set(reflect.ValueOf(out))
		return nil
	case reflect.Pointer:
		if isNilRaw(raw) {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		elem := reflect.New(dst.Type().Elem()).Elem()
		if err := decodeCompactValue(raw, elem); err != nil {
			return err
		}
		dst.Set(elem.Addr())
		return nil
	case reflect.Struct:
		if !isCompactStructType(dst.Type()) {
			return decodeMsgpackInto(raw, dst)
		}
		return decodeCompactStruct(raw, dst)
	case reflect.Slice:
		if isNilRaw(raw) {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		if dst.Type().Elem().Kind() == reflect.Uint8 {
			var data []byte
			dec := msgpack.NewDecoder(bytes.NewReader(raw))
			dec.SetCustomStructTag("am")
			if err := dec.Decode(&data); err != nil {
				return ErrCodec{Message: err.Error()}
			}
			dst.Set(reflect.ValueOf(data).Convert(dst.Type()))
			return nil
		}
		return decodeCompactSlice(raw, dst)
	case reflect.Array:
		if isNilRaw(raw) {
			return ErrCodec{Message: "compact decode cannot assign nil to array"}
		}
		return decodeCompactArray(raw, dst)
	case reflect.Map:
		if isNilRaw(raw) {
			dst.Set(reflect.Zero(dst.Type()))
			return nil
		}
		return decodeCompactMap(raw, dst)
	default:
		if isNilRaw(raw) {
			return ErrCodec{Message: "compact decode cannot assign nil to value"}
		}
		return decodeMsgpackInto(raw, dst)
	}
}

func decodeCompactStruct(raw msgpack.RawMessage, dst reflect.Value) error {
	var items []msgpack.RawMessage
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(&items); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	fields, err := compactFieldIndices(dst.Type())
	if err != nil {
		return err
	}
	if len(items) != len(fields) {
		return ErrCompactFieldCount{Expected: len(fields), Got: len(items)}
	}
	for i, fieldIndex := range fields {
		field := dst.Field(fieldIndex)
		if !field.CanSet() {
			return ErrInvalidMessage{Reason: "compact decode requires exported fields"}
		}
		if err := decodeCompactValue(items[i], field); err != nil {
			return err
		}
	}
	return nil
}

func decodeCompactSlice(raw msgpack.RawMessage, dst reflect.Value) error {
	var items []msgpack.RawMessage
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(&items); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	slice := reflect.MakeSlice(dst.Type(), len(items), len(items))
	for i := range items {
		if err := decodeCompactValue(items[i], slice.Index(i)); err != nil {
			return err
		}
	}
	dst.Set(slice)
	return nil
}

func decodeCompactArray(raw msgpack.RawMessage, dst reflect.Value) error {
	var items []msgpack.RawMessage
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(&items); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	if len(items) != dst.Len() {
		return ErrCompactFieldCount{Expected: dst.Len(), Got: len(items)}
	}
	for i := range items {
		if err := decodeCompactValue(items[i], dst.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func decodeCompactMap(raw msgpack.RawMessage, dst reflect.Value) error {
	if !dst.CanSet() {
		return ErrInvalidMessage{Reason: "compact decode requires settable map"}
	}
	keyType := dst.Type().Key()
	if keyType.Kind() == reflect.Struct || (keyType.Kind() == reflect.Pointer && keyType.Elem().Kind() == reflect.Struct) {
		return ErrInvalidMessage{Reason: "compact decode does not support struct map keys"}
	}
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	length, err := dec.DecodeMapLen()
	if err != nil {
		return ErrCodec{Message: err.Error()}
	}
	if length == -1 {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}
	result := reflect.MakeMapWithSize(dst.Type(), length)
	for i := 0; i < length; i++ {
		key := reflect.New(keyType).Elem()
		if err := dec.Decode(key.Addr().Interface()); err != nil {
			return ErrCodec{Message: err.Error()}
		}
		if keyType.Kind() == reflect.Interface && !isComparableKey(key.Interface()) {
			return ErrInvalidMessage{Reason: "compact decode requires comparable map keys"}
		}
		rawValue, err := dec.DecodeRaw()
		if err != nil {
			return ErrCodec{Message: err.Error()}
		}
		val := reflect.New(dst.Type().Elem()).Elem()
		if err := decodeCompactValue(rawValue, val); err != nil {
			return err
		}
		result.SetMapIndex(key, val)
	}
	dst.Set(result)
	return nil
}

func decodeMsgpackInto(raw msgpack.RawMessage, dst reflect.Value) error {
	dec := msgpack.NewDecoder(bytes.NewReader(raw))
	dec.SetCustomStructTag("am")
	if err := dec.Decode(dst.Addr().Interface()); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	return nil
}

func isCompactStructType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	if implementsAny(t,
		msgpackMarshalerType,
		msgpackUnmarshalerType,
		msgpackCustomEncType,
		msgpackCustomDecType,
		binaryMarshalerType,
		binaryUnmarshalerType,
		textMarshalerType,
		textUnmarshalerType,
	) {
		return false
	}
	_, err := compactFieldIndices(t)
	return err == nil
}

func implementsAny(t reflect.Type, ifaces ...reflect.Type) bool {
	for _, iface := range ifaces {
		if t.Implements(iface) {
			return true
		}
		if reflect.PointerTo(t).Implements(iface) {
			return true
		}
	}
	return false
}

func implementsAnyPointer(t reflect.Type, ifaces ...reflect.Type) bool {
	ptr := reflect.PointerTo(t)
	for _, iface := range ifaces {
		if ptr.Implements(iface) {
			return true
		}
	}
	return false
}

func isNilRaw(raw msgpack.RawMessage) bool {
	return len(raw) == 1 && raw[0] == msgpcode.Nil
}

func isComparableKey(key any) bool {
	if key == nil {
		return false
	}
	return reflect.TypeOf(key).Comparable()
}

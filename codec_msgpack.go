package adaptivemsg

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"reflect"
	"sync"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

const (
	// CodecMsgpackCompact (ID=1) encodes messages as a compact MessagePack array
	// envelope: [wireName, field1, field2, ...]. This format is more
	// space-efficient than map encoding but requires field order to match
	// between sender and receiver.
	CodecMsgpackCompact CodecID = 1
	// CodecMsgpackMap (ID=2) encodes messages as a MessagePack map envelope:
	// {"type": wireName, "data": {field: value, ...}}. This format is more
	// verbose than compact but tolerant of field order changes between sender
	// and receiver.
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
	d := getDecoder(raw)
	defer putDecoder(d)
	if err := d.dec.Decode(dst); err != nil {
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
	wire, body, err := decodeCompactEnvelope(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Wire: wire, Body: body}, nil
}

func (msgpackCompactCodec) DecodeInto(body any, dst any) error {
	b, ok := body.(compactBody)
	if !ok {
		return ErrCodec{Message: "compact body must be the fields of a compact payload"}
	}
	return decodeCompactInto(b, dst)
}

type mapEnvelope struct {
	Type string `am:"type"`
	Data any    `am:"data"`
}

// compactBody is the part of a compact payload after the wire name: count
// encoded fields.
type compactBody struct {
	fields []byte
	count  int
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
	marshalerTypes         = []reflect.Type{
		msgpackMarshalerType,
		msgpackUnmarshalerType,
		msgpackCustomEncType,
		msgpackCustomDecType,
		binaryMarshalerType,
		binaryUnmarshalerType,
		textMarshalerType,
		textUnmarshalerType,
	}
)

// compactTypeInfo is what the compact codec needs to know about a struct type.
// It is computed once per type.
type compactTypeInfo struct {
	// fields are the indices of the encoded fields, in order.
	fields []int
	// err tells why the type cannot be encoded field by field.
	err error
	// marshals reports that T or *T encodes itself, so the codec leaves it to
	// msgpack.
	marshals bool
	// ptrMarshals reports that *T encodes itself.
	ptrMarshals bool
}

// compact reports whether values of the type are encoded as an array of
// their fields.
func (i *compactTypeInfo) compact() bool {
	return i.err == nil && !i.marshals
}

var compactTypes sync.Map // reflect.Type -> *compactTypeInfo

func compactInfoFor(t reflect.Type) *compactTypeInfo {
	if cached, ok := compactTypes.Load(t); ok {
		return cached.(*compactTypeInfo)
	}
	info := &compactTypeInfo{}
	if t.Kind() != reflect.Struct {
		info.err = ErrInvalidMessage{Reason: "compact encoding requires struct type"}
	} else {
		info.fields = make([]int, 0, t.NumField())
		for i := range t.NumField() {
			if t.Field(i).PkgPath != "" {
				info.err = ErrInvalidMessage{Reason: "compact encoding does not support unexported fields"}
				info.fields = nil
				break
			}
			info.fields = append(info.fields, i)
		}
		ptr := reflect.PointerTo(t)
		for _, iface := range marshalerTypes {
			if ptr.Implements(iface) {
				info.ptrMarshals = true
			}
			if t.Implements(iface) || ptr.Implements(iface) {
				info.marshals = true
			}
		}
	}
	actual, _ := compactTypes.LoadOrStore(t, info)
	return actual.(*compactTypeInfo)
}

func compactFieldIndices(t reflect.Type) ([]int, error) {
	info := compactInfoFor(t)
	return info.fields, info.err
}

// pooledEncoder and pooledDecoder keep a msgpack encoder or decoder with its
// buffer or reader for reuse.
type pooledEncoder struct {
	enc *msgpack.Encoder
	buf bytes.Buffer
}

type pooledDecoder struct {
	dec *msgpack.Decoder
	r   bytes.Reader
}

// maxPooledBuffer keeps a huge message from pinning its buffer in the pool.
const maxPooledBuffer = 64 << 10

var (
	encoderPool = sync.Pool{New: func() any {
		e := &pooledEncoder{}
		e.enc = msgpack.NewEncoder(&e.buf)
		return e
	}}
	decoderPool = sync.Pool{New: func() any {
		d := &pooledDecoder{}
		d.dec = msgpack.NewDecoder(&d.r)
		return d
	}}
)

func getEncoder() *pooledEncoder {
	e := encoderPool.Get().(*pooledEncoder)
	e.buf.Reset()
	e.enc.Reset(&e.buf)
	e.enc.SetCustomStructTag("am")
	return e
}

// bytes returns a copy of what e encoded, owned by the caller, and returns e
// to the pool.
func (e *pooledEncoder) bytes() []byte {
	out := bytes.Clone(e.buf.Bytes())
	putEncoder(e)
	return out
}

func putEncoder(e *pooledEncoder) {
	if e.buf.Cap() <= maxPooledBuffer {
		encoderPool.Put(e)
	}
}

func getDecoder(data []byte) *pooledDecoder {
	d := decoderPool.Get().(*pooledDecoder)
	d.r.Reset(data)
	d.dec.Reset(&d.r)
	d.dec.SetCustomStructTag("am")
	return d
}

func putDecoder(d *pooledDecoder) {
	d.r.Reset(nil)
	decoderPool.Put(d)
}

func encodeMap(msg Message) ([]byte, error) {
	wire, err := WireNameOf(msg)
	if err != nil {
		return nil, err
	}
	e := getEncoder()
	if err := e.enc.Encode(&mapEnvelope{Type: wire, Data: msg}); err != nil {
		putEncoder(e)
		return nil, ErrCodec{Message: err.Error()}
	}
	return e.bytes(), nil
}

// decodeMapEnvelope returns the wire name and the encoded data of a map
// payload. The data is a slice of payload.
func decodeMapEnvelope(payload []byte) (string, msgpack.RawMessage, error) {
	d := getDecoder(payload)
	defer putDecoder(d)
	n, err := d.dec.DecodeMapLen()
	if err != nil {
		return "", nil, ErrCodec{Message: err.Error()}
	}
	var wire string
	var data msgpack.RawMessage
	for range max(n, 0) {
		key, err := d.dec.DecodeString()
		if err != nil {
			return "", nil, ErrCodec{Message: err.Error()}
		}
		switch key {
		case "type":
			if wire, err = d.dec.DecodeString(); err != nil {
				return "", nil, ErrCodec{Message: err.Error()}
			}
		case "data":
			start := len(payload) - d.r.Len()
			if err := d.dec.Skip(); err != nil {
				return "", nil, ErrCodec{Message: err.Error()}
			}
			data = payload[start : len(payload)-d.r.Len()]
		default:
			if err := d.dec.Skip(); err != nil {
				return "", nil, ErrCodec{Message: err.Error()}
			}
		}
	}
	if wire == "" {
		return "", nil, ErrCodec{Message: "map payload missing type"}
	}
	return wire, data, nil
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
	fields, err := compactFieldIndices(rv.Type())
	if err != nil {
		return nil, err
	}
	e := getEncoder()
	if err := encodeCompactFields(e.enc, wire, rv, fields); err != nil {
		putEncoder(e)
		return nil, err
	}
	return e.bytes(), nil
}

func encodeCompactFields(enc *msgpack.Encoder, wire string, rv reflect.Value, fields []int) error {
	if err := enc.EncodeArrayLen(1 + len(fields)); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	if err := enc.EncodeString(wire); err != nil {
		return ErrCodec{Message: err.Error()}
	}
	for _, idx := range fields {
		if err := encodeCompactValue(enc, rv.Field(idx)); err != nil {
			return err
		}
	}
	return nil
}

// encodeCompactValue encodes a value with every struct in it, however deep,
// as an array of its fields. Types that encode themselves, and values that
// are not structs or containers of them, go to msgpack as they are.
func encodeCompactValue(enc *msgpack.Encoder, value reflect.Value) error {
	if !value.IsValid() || isNilValue(value) {
		return codecErr(enc.EncodeNil())
	}
	switch value.Kind() {
	case reflect.Interface:
		return encodeCompactValue(enc, value.Elem())
	case reflect.Pointer:
		elem := value.Elem()
		if elem.Kind() == reflect.Struct && !compactInfoFor(elem.Type()).compact() {
			return codecErr(enc.EncodeValue(value))
		}
		return encodeCompactValue(enc, elem)
	case reflect.Struct:
		return encodeCompactStruct(enc, value)
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return codecErr(enc.EncodeBytes(value.Bytes()))
		}
		return encodeCompactElements(enc, value)
	case reflect.Array:
		return encodeCompactElements(enc, value)
	case reflect.Map:
		return encodeCompactMap(enc, value)
	default:
		return codecErr(enc.EncodeValue(value))
	}
}

// isNilValue reports whether value is a nil interface, pointer, slice or map.
func isNilValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Slice, reflect.Map:
		return value.IsNil()
	}
	return false
}

func encodeCompactStruct(enc *msgpack.Encoder, value reflect.Value) error {
	info := compactInfoFor(value.Type())
	if !info.compact() {
		if value.CanAddr() && info.ptrMarshals {
			return codecErr(enc.EncodeValue(value.Addr()))
		}
		return codecErr(enc.EncodeValue(value))
	}
	if err := enc.EncodeArrayLen(len(info.fields)); err != nil {
		return codecErr(err)
	}
	for _, idx := range info.fields {
		if err := encodeCompactValue(enc, value.Field(idx)); err != nil {
			return err
		}
	}
	return nil
}

func encodeCompactMap(enc *msgpack.Encoder, value reflect.Value) error {
	if isStructKey(value.Type().Key()) {
		return ErrInvalidMessage{Reason: "compact encoding does not support struct map keys"}
	}
	if err := enc.EncodeMapLen(value.Len()); err != nil {
		return codecErr(err)
	}
	iter := value.MapRange()
	for iter.Next() {
		if !compactKeyComparable(iter.Key()) {
			return ErrInvalidMessage{Reason: "compact encoding requires comparable map keys"}
		}
		if err := encodeCompactValue(enc, iter.Key()); err != nil {
			return err
		}
		if err := encodeCompactValue(enc, iter.Value()); err != nil {
			return err
		}
	}
	return nil
}

// isStructKey reports whether a map key type is a struct or a pointer to
// one, which compact encoding would turn into an array.
func isStructKey(keyType reflect.Type) bool {
	if keyType.Kind() == reflect.Pointer {
		keyType = keyType.Elem()
	}
	return keyType.Kind() == reflect.Struct
}

func encodeCompactElements(enc *msgpack.Encoder, value reflect.Value) error {
	if err := enc.EncodeArrayLen(value.Len()); err != nil {
		return codecErr(err)
	}
	for i := range value.Len() {
		if err := encodeCompactValue(enc, value.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

// compactKeyComparable reports whether a map key stays comparable once
// encoded: a nil key does not, nor does one that becomes an array.
func compactKeyComparable(key reflect.Value) bool {
	for key.Kind() == reflect.Interface || key.Kind() == reflect.Pointer {
		if key.IsNil() {
			return false
		}
		key = key.Elem()
	}
	switch key.Kind() {
	case reflect.Array:
		return false
	case reflect.Struct:
		return !compactInfoFor(key.Type()).compact()
	}
	return true
}

func codecErr(err error) error {
	if err == nil {
		return nil
	}
	return ErrCodec{Message: err.Error()}
}

// decodeCompactEnvelope returns the wire name and the fields of a compact
// payload. The fields are a slice of payload.
func decodeCompactEnvelope(payload []byte) (string, compactBody, error) {
	n, rest, ok := readArrayHeader(payload)
	if !ok || n == 0 {
		return "", compactBody{}, ErrCodec{Message: "compact payload must be a non-empty array"}
	}
	name, rest, ok := readStringValue(rest)
	if !ok {
		return "", compactBody{}, ErrCodec{Message: "compact message name must be a string"}
	}
	return name, compactBody{fields: rest, count: n - 1}, nil
}

// readArrayHeader reads a msgpack array header.
func readArrayHeader(b []byte) (int, []byte, bool) {
	if len(b) == 0 {
		return 0, nil, false
	}
	c := b[0]
	switch {
	case msgpcode.IsFixedArray(c):
		return int(c & msgpcode.FixedArrayMask), b[1:], true
	case c == msgpcode.Array16 && len(b) >= 3:
		return int(binary.BigEndian.Uint16(b[1:3])), b[3:], true
	case c == msgpcode.Array32 && len(b) >= 5:
		return int(binary.BigEndian.Uint32(b[1:5])), b[5:], true
	}
	return 0, nil, false
}

// readStringValue reads a msgpack string. Like msgpack's own string decoding
// it also accepts bin.
func readStringValue(b []byte) (string, []byte, bool) {
	if len(b) == 0 {
		return "", nil, false
	}
	c := b[0]
	var n, header int
	switch {
	case msgpcode.IsFixedString(c):
		n, header = int(c&msgpcode.FixedStrMask), 1
	case (c == msgpcode.Str8 || c == msgpcode.Bin8) && len(b) >= 2:
		n, header = int(b[1]), 2
	case (c == msgpcode.Str16 || c == msgpcode.Bin16) && len(b) >= 3:
		n, header = int(binary.BigEndian.Uint16(b[1:3])), 3
	case (c == msgpcode.Str32 || c == msgpcode.Bin32) && len(b) >= 5:
		n, header = int(binary.BigEndian.Uint32(b[1:5])), 5
	default:
		return "", nil, false
	}
	if len(b)-header < n {
		return "", nil, false
	}
	return string(b[header : header+n]), b[header+n:], true
}

func decodeCompactInto(body compactBody, dst any) error {
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
	if body.count != len(fields) {
		return ErrCompactFieldCount{Expected: len(fields), Got: body.count}
	}
	d := getDecoder(body.fields)
	defer putDecoder(d)
	for _, fieldIndex := range fields {
		if err := decodeCompactValue(d.dec, elem.Field(fieldIndex)); err != nil {
			return err
		}
	}
	return nil
}

// decodeCompactValue decodes the next value, encoded by encodeCompactValue,
// into dst.
func decodeCompactValue(dec *msgpack.Decoder, dst reflect.Value) error {
	if !dst.CanSet() {
		return ErrInvalidMessage{Reason: "compact decode requires settable destination"}
	}
	code, err := dec.PeekCode()
	if err != nil {
		return codecErr(err)
	}
	if code == msgpcode.Nil {
		switch dst.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Slice, reflect.Map:
			dst.Set(reflect.Zero(dst.Type()))
			return codecErr(dec.DecodeNil())
		case reflect.Array:
			return ErrCodec{Message: "compact decode cannot assign nil to array"}
		case reflect.Struct:
			// Decoded below, like any other struct value.
		default:
			return ErrCodec{Message: "compact decode cannot assign nil to value"}
		}
	}
	switch dst.Kind() {
	case reflect.Interface:
		var out any
		if err := dec.Decode(&out); err != nil {
			return codecErr(err)
		}
		dst.Set(reflect.ValueOf(out))
		return nil
	case reflect.Pointer:
		elem := reflect.New(dst.Type().Elem()).Elem()
		if err := decodeCompactValue(dec, elem); err != nil {
			return err
		}
		dst.Set(elem.Addr())
		return nil
	case reflect.Struct:
		return decodeCompactStruct(dec, dst)
	case reflect.Slice:
		return decodeCompactSlice(dec, dst)
	case reflect.Array:
		return decodeCompactArray(dec, dst)
	case reflect.Map:
		return decodeCompactMap(dec, dst)
	default:
		return codecErr(dec.Decode(dst.Addr().Interface()))
	}
}

func decodeCompactStruct(dec *msgpack.Decoder, dst reflect.Value) error {
	info := compactInfoFor(dst.Type())
	if !info.compact() {
		return codecErr(dec.Decode(dst.Addr().Interface()))
	}
	n, err := dec.DecodeArrayLen()
	if err != nil {
		return codecErr(err)
	}
	n = max(n, 0)
	if n != len(info.fields) {
		return ErrCompactFieldCount{Expected: len(info.fields), Got: n}
	}
	for _, fieldIndex := range info.fields {
		if err := decodeCompactValue(dec, dst.Field(fieldIndex)); err != nil {
			return err
		}
	}
	return nil
}

func decodeCompactSlice(dec *msgpack.Decoder, dst reflect.Value) error {
	if dst.Type().Elem().Kind() == reflect.Uint8 {
		var data []byte
		if err := dec.Decode(&data); err != nil {
			return codecErr(err)
		}
		dst.Set(reflect.ValueOf(data).Convert(dst.Type()))
		return nil
	}
	n, err := dec.DecodeArrayLen()
	if err != nil {
		return codecErr(err)
	}
	n = max(n, 0)
	slice := reflect.MakeSlice(dst.Type(), n, n)
	for i := range n {
		if err := decodeCompactValue(dec, slice.Index(i)); err != nil {
			return err
		}
	}
	dst.Set(slice)
	return nil
}

func decodeCompactArray(dec *msgpack.Decoder, dst reflect.Value) error {
	n, err := dec.DecodeArrayLen()
	if err != nil {
		return codecErr(err)
	}
	if n != dst.Len() {
		return ErrCompactFieldCount{Expected: dst.Len(), Got: max(n, 0)}
	}
	for i := range n {
		if err := decodeCompactValue(dec, dst.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func decodeCompactMap(dec *msgpack.Decoder, dst reflect.Value) error {
	keyType := dst.Type().Key()
	if isStructKey(keyType) {
		return ErrInvalidMessage{Reason: "compact decode does not support struct map keys"}
	}
	length, err := dec.DecodeMapLen()
	if err != nil {
		return codecErr(err)
	}
	if length == -1 {
		dst.Set(reflect.Zero(dst.Type()))
		return nil
	}
	result := reflect.MakeMapWithSize(dst.Type(), length)
	for range length {
		key := reflect.New(keyType).Elem()
		if err := dec.Decode(key.Addr().Interface()); err != nil {
			return codecErr(err)
		}
		if keyType.Kind() == reflect.Interface && !isComparableKey(key.Interface()) {
			return ErrInvalidMessage{Reason: "compact decode requires comparable map keys"}
		}
		val := reflect.New(dst.Type().Elem()).Elem()
		if err := decodeCompactValue(dec, val); err != nil {
			return err
		}
		result.SetMapIndex(key, val)
	}
	dst.Set(result)
	return nil
}

func isComparableKey(key any) bool {
	if key == nil {
		return false
	}
	return reflect.TypeOf(key).Comparable()
}

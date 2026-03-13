package adaptivemsg

import (
	"errors"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

type codecTestNestedInner struct {
	Count int `msgpack:"count"`
}

type codecTestNested struct {
	Name  string               `msgpack:"name"`
	Inner codecTestNestedInner `msgpack:"inner"`
}

func (*codecTestNested) WireName() string {
	return "am.test.Nested"
}

type codecTestCompact struct {
	A string `msgpack:"a"`
	B int    `msgpack:"b"`
}

func (*codecTestCompact) WireName() string {
	return "am.test.Compact"
}

type codecTestCompactHidden struct {
	a string `msgpack:"a"`
}

func (*codecTestCompactHidden) WireName() string {
	return "am.test.CompactHidden"
}

type codecTestCompactTwo struct {
	A string `msgpack:"a"`
	B string `msgpack:"b"`
}

func (*codecTestCompactTwo) WireName() string {
	return "am.test.CompactTwo"
}

func TestMapEnvelopeRoundTrip(t *testing.T) {
	payload, err := encodeMap(&codecTestNested{
		Name:  "hello",
		Inner: codecTestNestedInner{Count: 7},
	})
	if err != nil {
		t.Fatalf("encodeMap: %v", err)
	}

	wire, raw, err := decodeMapEnvelope(payload)
	if err != nil {
		t.Fatalf("decodeMapEnvelope: %v", err)
	}
	if wire != "am.test.Nested" {
		t.Fatalf("decodeMapEnvelope got %q want %q", wire, "am.test.Nested")
	}

	var decoded codecTestNested
	if err := msgpack.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if decoded.Name != "hello" || decoded.Inner.Count != 7 {
		t.Fatalf("decoded mismatch: %#v", decoded)
	}
}

func TestMapEnvelopeMissingType(t *testing.T) {
	payload, err := msgpack.Marshal(map[string]any{
		"data": map[string]any{"name": "hello"},
	})
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	_, _, err = decodeMapEnvelope(payload)
	var codec ErrCodec
	if !errors.As(err, &codec) {
		t.Fatalf("expected ErrCodec, got %v", err)
	}
	if codec.Message != "map payload missing type" {
		t.Fatalf("unexpected ErrCodec message: %q", codec.Message)
	}
}

func TestCompactEnvelopeRoundTrip(t *testing.T) {
	payload, err := encodeCompact(&codecTestCompact{A: "hi", B: 42})
	if err != nil {
		t.Fatalf("encodeCompact: %v", err)
	}

	wire, values, err := decodeCompactEnvelope(payload)
	if err != nil {
		t.Fatalf("decodeCompactEnvelope: %v", err)
	}
	if wire != "am.test.Compact" {
		t.Fatalf("decodeCompactEnvelope got %q want %q", wire, "am.test.Compact")
	}
	raw := rawMessage{Wire: wire, Codec: CodecMsgpackCompact, Body: values}
	decoded, err := decodeRawAs[*codecTestCompact](raw)
	if err != nil {
		t.Fatalf("decodeRawAs: %v", err)
	}
	if decoded.A != "hi" || decoded.B != 42 {
		t.Fatalf("decoded mismatch: %#v", decoded)
	}
}

func TestCompactEnvelopeErrors(t *testing.T) {
	payload, err := msgpack.Marshal([]any{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	_, _, err = decodeCompactEnvelope(payload)
	var codec ErrCodec
	if !errors.As(err, &codec) {
		t.Fatalf("expected ErrCodec, got %v", err)
	}
	if codec.Message != "compact payload must be a non-empty array" {
		t.Fatalf("unexpected ErrCodec message: %q", codec.Message)
	}

	payload, err = msgpack.Marshal([]any{123})
	if err != nil {
		t.Fatalf("marshal non-string: %v", err)
	}
	_, _, err = decodeCompactEnvelope(payload)
	if !errors.As(err, &codec) {
		t.Fatalf("expected ErrCodec, got %v", err)
	}
	if codec.Message != "compact message name must be a string" {
		t.Fatalf("unexpected ErrCodec message: %q", codec.Message)
	}
}

func TestCompactEncodeUnexportedField(t *testing.T) {
	_, err := encodeCompact(&codecTestCompactHidden{a: "hidden"})
	var invalid ErrInvalidMessage
	if !errors.As(err, &invalid) {
		t.Fatalf("expected ErrInvalidMessage, got %v", err)
	}
}

func TestCompactFieldCountMismatch(t *testing.T) {
	rawField, err := msgpack.Marshal("one")
	if err != nil {
		t.Fatalf("marshal field: %v", err)
	}
	raw := rawMessage{
		Wire:  "am.test.CompactTwo",
		Codec: CodecMsgpackCompact,
		Body:  []msgpack.RawMessage{rawField},
	}
	_, err = decodeRawAs[*codecTestCompactTwo](raw)
	var count ErrCompactFieldCount
	if !errors.As(err, &count) {
		t.Fatalf("expected ErrCompactFieldCount, got %v", err)
	}
}

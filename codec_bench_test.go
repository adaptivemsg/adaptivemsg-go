package adaptivemsg

import "testing"

type benchCodecInner struct {
	Text string `am:"text"`
	Flag bool   `am:"flag"`
}

type benchCodecMessage struct {
	ID     int64             `am:"id"`
	Name   string            `am:"name"`
	Tags   []string          `am:"tags"`
	Data   []byte            `am:"data"`
	Score  float64           `am:"score"`
	Inner  benchCodecInner   `am:"inner"`
	Labels map[string]string `am:"labels"`
}

func (*benchCodecMessage) WireName() string {
	return "am.bench.CodecMessage"
}

func benchCodecValue() *benchCodecMessage {
	return &benchCodecMessage{
		ID:     42,
		Name:   "bench",
		Tags:   []string{"a", "bb", "ccc"},
		Data:   make([]byte, 64),
		Score:  0.5,
		Inner:  benchCodecInner{Text: "inner", Flag: true},
		Labels: map[string]string{"env": "prod", "zone": "a"},
	}
}

func benchCodec(b *testing.B, id CodecID) CodecImpl {
	b.Helper()
	codec, ok := codecByID(id)
	if !ok {
		b.Fatalf("codec %d not registered", id)
	}
	return codec
}

func benchmarkCodecEncode(b *testing.B, id CodecID) {
	codec := benchCodec(b, id)
	msg := benchCodecValue()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := codec.Encode(msg); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkCodecDecode(b *testing.B, id CodecID) {
	codec := benchCodec(b, id)
	payload, err := codec.Encode(benchCodecValue())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		env, err := codec.DecodeEnvelope(payload)
		if err != nil {
			b.Fatal(err)
		}
		var out benchCodecMessage
		if err := codec.DecodeInto(env.Body, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodecEncodeCompact(b *testing.B) { benchmarkCodecEncode(b, CodecMsgpackCompact) }
func BenchmarkCodecEncodeMap(b *testing.B)     { benchmarkCodecEncode(b, CodecMsgpackMap) }
func BenchmarkCodecDecodeCompact(b *testing.B) { benchmarkCodecDecode(b, CodecMsgpackCompact) }
func BenchmarkCodecDecodeMap(b *testing.B)     { benchmarkCodecDecode(b, CodecMsgpackMap) }

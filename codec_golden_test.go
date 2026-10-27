package adaptivemsg

import (
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

// The expected bytes were produced by the compact encoder before it wrote to
// the msgpack encoder directly instead of building a tree of values; both
// must give the same bytes. Maps hold one entry each, because the order of
// map entries is random.
const (
	goldenShapesCompact = "dc0024b4616d2e746573742e476f6c64656e536861706573cd012cd3fffffffffffffffbd0fdcdfde8cf0000010000000000ca3fc00000cbc002000000000000c3a26869d92d6120737472696e672074686174206973206c6f6e676572207468616e207468697274792d6f6e65206279746573c0c400c4030102ff94cc09cc08cc07cc06c007c092a170d2ffffffff9301ffcd03e8c0909292a161d20000000192a162d20000000293c40101c0c40081a16b0181a17892a179d200000003c005a17392a169d200000004c081a576616c7565a26f6bd30000000059682f00cc03a36c626c91c3"
	goldenShapesMap     = "82a474797065b4616d2e746573742e476f6c64656e536861706573a464617461de0023a3696e74cd012ca36e6567d3fffffffffffffffba26938d0fda3753136cdfde8a3753634cf0000010000000000a3663332ca3fc00000a3663634cbc002000000000000a4626f6f6cc3a573686f7274a26869a46c6f6e67d92d6120737472696e672074686174206973206c6f6e676572207468616e207468697274792d6f6e65206279746573a96e696c5f6279746573c0a5656d707479c400a56279746573c4030102ffa3617272c40409080706a76e696c5f707472c0a370747207a96e696c5f696e6e6572c0a7705f696e6e657282a474657874a170a16ed2ffffffffa4696e74739301ffcd03e8a86e696c5f73747273c0a76e6f5f7374727390a6696e6e6572739282a474657874a161a16ed20000000182a474657874a162a16ed200000002a66368756e6b7393c40101c0c400a36f6e6581a16b01a96f6e655f696e6e657281a17882a474657874a179a16ed200000003a76e696c5f6d6170c0a7616e795f696e7405a7616e795f737472a173a9616e795f696e6e657282a474657874a169a16ed200000004a7616e795f6e696cc0a6637573746f6d81a576616c7565a26f6ba3647572d30000000059682f00a5636f6c6f72cc03a56c6162656ca36c626ca165c3"
)

type goldenColor uint8
type goldenLabel string

type goldenInner struct {
	Text string `am:"text"`
	N    int32  `am:"n"`
}

type GoldenEmbedded struct {
	E bool `am:"e"`
}

type goldenShapes struct {
	Int      int                    `am:"int"`
	Neg      int64                  `am:"neg"`
	I8       int8                   `am:"i8"`
	U16      uint16                 `am:"u16"`
	U64      uint64                 `am:"u64"`
	F32      float32                `am:"f32"`
	F64      float64                `am:"f64"`
	Bool     bool                   `am:"bool"`
	Short    string                 `am:"short"`
	Long     string                 `am:"long"`
	NilBytes []byte                 `am:"nil_bytes"`
	Empty    []byte                 `am:"empty"`
	Bytes    []byte                 `am:"bytes"`
	Arr      [4]byte                `am:"arr"`
	NilPtr   *int                   `am:"nil_ptr"`
	Ptr      *int                   `am:"ptr"`
	NilInner *goldenInner           `am:"nil_inner"`
	PInner   *goldenInner           `am:"p_inner"`
	Ints     []int                  `am:"ints"`
	NilStrs  []string               `am:"nil_strs"`
	NoStrs   []string               `am:"no_strs"`
	Inners   []goldenInner          `am:"inners"`
	Chunks   [][]byte               `am:"chunks"`
	One      map[string]int         `am:"one"`
	OneInner map[string]goldenInner `am:"one_inner"`
	NilMap   map[string]int         `am:"nil_map"`
	AnyInt   any                    `am:"any_int"`
	AnyStr   any                    `am:"any_str"`
	AnyInner any                    `am:"any_inner"`
	AnyNil   any                    `am:"any_nil"`
	Custom   codecTestCustomInner   `am:"custom"`
	Dur      time.Duration          `am:"dur"`
	Color    goldenColor            `am:"color"`
	Label    goldenLabel            `am:"label"`
	GoldenEmbedded
}

func (*goldenShapes) WireName() string { return "am.test.GoldenShapes" }

func goldenShapesValue() *goldenShapes {
	seven := 7
	return &goldenShapes{
		Int: 300, Neg: -5, I8: -3, U16: 65000, U64: 1 << 40, F32: 1.5, F64: -2.25, Bool: true,
		Short: "hi", Long: "a string that is longer than thirty-one bytes",
		Empty: []byte{}, Bytes: []byte{1, 2, 255}, Arr: [4]byte{9, 8, 7, 6},
		Ptr: &seven, PInner: &goldenInner{Text: "p", N: -1},
		Ints: []int{1, -1, 1000}, NoStrs: []string{},
		Inners:   []goldenInner{{Text: "a", N: 1}, {Text: "b", N: 2}},
		Chunks:   [][]byte{{1}, nil, {}},
		One:      map[string]int{"k": 1},
		OneInner: map[string]goldenInner{"x": {Text: "y", N: 3}},
		AnyInt:   5, AnyStr: "s", AnyInner: goldenInner{Text: "i", N: 4},
		Custom: codecTestCustomInner{Value: "ok"}, Dur: 1500 * time.Millisecond,
		Color: 3, Label: "lbl", GoldenEmbedded: GoldenEmbedded{E: true},
	}
}

func TestCodecGoldenShapes(t *testing.T) {
	for _, tc := range []struct {
		codec  CodecID
		golden string
	}{
		{CodecMsgpackCompact, goldenShapesCompact},
		{CodecMsgpackMap, goldenShapesMap},
	} {
		t.Run(tc.codec.String(), func(t *testing.T) {
			codec := mustCodec(t, tc.codec)
			payload, err := codec.Encode(goldenShapesValue())
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if got := hex.EncodeToString(payload); got != tc.golden {
				t.Fatalf("encoding changed\n got %s\nwant %s", got, tc.golden)
			}

			raw, err := newRawMessageFromPayload(codec, payload)
			if err != nil {
				t.Fatalf("newRawMessageFromPayload: %v", err)
			}
			decoded, err := decodeRawAs[*goldenShapes](raw)
			if err != nil {
				t.Fatalf("decodeRawAs: %v", err)
			}
			want := goldenShapesValue()
			// Interface fields come back as msgpack's own types.
			want.AnyInt, want.AnyStr, want.AnyInner = decoded.AnyInt, decoded.AnyStr, decoded.AnyInner
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded %+v\nwant %+v", decoded, want)
			}
		})
	}
}

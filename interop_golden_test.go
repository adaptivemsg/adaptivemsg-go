package adaptivemsg

import (
	"encoding/hex"
	"reflect"
	"testing"
)

// The Rust runtime's interop_golden_test.rs encodes the same messages and
// checks them against the same bytes, so a change on either side that breaks
// the other shows up in both test suites.
const (
	interopGoldenCompact = "97b2616d2e696e7465726f702e4d657373616765a16ec40301020392a161a162c092a174c3cb3fe0000000000000"
	interopGoldenMap     = "82a474797065b2616d2e696e7465726f702e4d657373616765a46461746186a46e616d65a16ea464617461c403010203a47461677392a161a162a46e6f7465c0a5696e6e657282a474657874a174a4666c6167c3a5726174696fcb3fe0000000000000"

	interopGoldenBlobCompact = "95af616d2e696e7465726f702e426c6f62c4020102c4010492c40103c40081a16bc4020506"
	interopGoldenBlobMap     = "82a474797065af616d2e696e7465726f702e426c6f62a46461746184a464617461c4020102a56d61796265c40104a66368756e6b7392c40103c400a762795f6e616d6581a16bc4020506"
)

type interopInner struct {
	Text string `am:"text"`
	Flag bool   `am:"flag"`
}

type interopMessage struct {
	Name  string       `am:"name"`
	Data  []byte       `am:"data"`
	Tags  []string     `am:"tags"`
	Note  *string      `am:"note"`
	Inner interopInner `am:"inner"`
	Ratio float64      `am:"ratio"`
}

func (*interopMessage) WireName() string {
	return "am.interop.Message"
}

func interopGoldenMessage() *interopMessage {
	return &interopMessage{
		Name:  "n",
		Data:  []byte{1, 2, 3},
		Tags:  []string{"a", "b"},
		Inner: interopInner{Text: "t", Flag: true},
		Ratio: 0.5,
	}
}

// interopBlob has a field of each byte form that amgen-go maps to its own
// serde_bytes form in Rust.
type interopBlob struct {
	Data   []byte            `am:"data"`
	Maybe  *[]byte           `am:"maybe"`
	Chunks [][]byte          `am:"chunks"`
	ByName map[string][]byte `am:"by_name"`
}

func (*interopBlob) WireName() string {
	return "am.interop.Blob"
}

func interopGoldenBlob() *interopBlob {
	maybe := []byte{4}
	return &interopBlob{
		Data:   []byte{1, 2},
		Maybe:  &maybe,
		Chunks: [][]byte{{3}, {}},
		ByName: map[string][]byte{"k": {5, 6}},
	}
}

func TestInteropGoldenEncoding(t *testing.T) {
	checkInteropGolden(t, interopGoldenMessage(), interopGoldenCompact, interopGoldenMap)
}

func TestInteropGoldenByteFields(t *testing.T) {
	checkInteropGolden(t, interopGoldenBlob(), interopGoldenBlobCompact, interopGoldenBlobMap)
}

func checkInteropGolden[T Message](t *testing.T, want T, compact, mapped string) {
	t.Helper()
	for _, tc := range []struct {
		codec  CodecID
		golden string
	}{
		{CodecMsgpackCompact, compact},
		{CodecMsgpackMap, mapped},
	} {
		t.Run(tc.codec.String(), func(t *testing.T) {
			codec := mustCodec(t, tc.codec)
			payload, err := codec.Encode(want)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if got := hex.EncodeToString(payload); got != tc.golden {
				t.Fatalf("encoding changed\n got %s\nwant %s", got, tc.golden)
			}

			golden, err := hex.DecodeString(tc.golden)
			if err != nil {
				t.Fatalf("DecodeString: %v", err)
			}
			raw, err := newRawMessageFromPayload(codec, golden)
			if err != nil {
				t.Fatalf("newRawMessageFromPayload: %v", err)
			}
			decoded, err := decodeRawAs[T](raw)
			if err != nil {
				t.Fatalf("decodeRawAs: %v", err)
			}
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded %+v, want %+v", decoded, want)
			}
		})
	}
}

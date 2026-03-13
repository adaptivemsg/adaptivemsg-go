package adaptivemsg

import "testing"

func mustCodec(t *testing.T, id CodecID) CodecImpl {
	t.Helper()
	codec, ok := codecByID(id)
	if !ok {
		t.Fatalf("codec %d not registered", id)
	}
	return codec
}

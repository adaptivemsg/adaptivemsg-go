package adaptivemsg

import "testing"

type testCtx struct {
	Value string
}

func TestStreamContextState(t *testing.T) {
	ctx := &StreamContext{}
	expected := &testCtx{Value: "ok"}
	ctx.SetState(expected)

	got, ok := ctx.State[*testCtx]()
	if !ok {
		t.Fatalf("State returned false")
	}
	if got != expected {
		t.Fatalf("State got %v want %v", got, expected)
	}

	if _, ok := ctx.State[string](); ok {
		t.Fatalf("State for wrong type should return false")
	}
}

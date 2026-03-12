package adaptivemsg

import "testing"

type testCtx struct {
	Value string
}

func TestContextAs(t *testing.T) {
	ctx := &StreamContext{}
	expected := &testCtx{Value: "ok"}
	ctx.SetContext(expected)

	if got := ctx.GetContext(); got != expected {
		t.Fatalf("GetContext got %v want %v", got, expected)
	}

	got, ok := ContextAs[*testCtx](ctx)
	if !ok {
		t.Fatalf("ContextAs returned false")
	}
	if got != expected {
		t.Fatalf("ContextAs got %v want %v", got, expected)
	}

	if _, ok := ContextAs[string](ctx); ok {
		t.Fatalf("ContextAs for wrong type should return false")
	}
}

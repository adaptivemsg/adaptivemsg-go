package adaptivemsg

import (
	"errors"
	"testing"
)

type testHandlerMsg struct{}

func (*testHandlerMsg) WireName() string {
	return "am.test.HandlerMsg"
}

func (*testHandlerMsg) Handle(*StreamContext) (Message, error) {
	return &OkReply{}, nil
}

func TestRegisterGlobalTypeRegistersHandler(t *testing.T) {
	orig := globalRegistry
	globalRegistry = newRegistry()
	t.Cleanup(func() {
		globalRegistry = orig
	})
	if err := RegisterGlobalType[testHandlerMsg](); err != nil {
		t.Fatalf("RegisterGlobalType: %v", err)
	}
	wire, err := WireNameOf(&testHandlerMsg{})
	if err != nil {
		t.Fatalf("WireNameOf: %v", err)
	}
	if _, ok := globalRegistry.handler(wire); !ok {
		t.Fatalf("handler not registered for %q", wire)
	}
}

func TestRegisterGlobalTypeInvalid(t *testing.T) {
	orig := globalRegistry
	globalRegistry = newRegistry()
	t.Cleanup(func() {
		globalRegistry = orig
	})
	err := RegisterGlobalType[int]()
	if err == nil {
		t.Fatalf("expected error")
	}
	var invalid ErrInvalidMessage
	if !errors.As(err, &invalid) {
		t.Fatalf("expected ErrInvalidMessage, got %v", err)
	}
}

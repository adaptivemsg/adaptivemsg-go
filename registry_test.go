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

func TestRegisterRegistersHandler(t *testing.T) {
	orig := globalRegistry
	globalRegistry = newRegistry()
	t.Cleanup(func() {
		globalRegistry = orig
	})
	if err := Register[testHandlerMsg](); err != nil {
		t.Fatalf("Register: %v", err)
	}
	wire, err := WireNameOf(&testHandlerMsg{})
	if err != nil {
		t.Fatalf("WireNameOf: %v", err)
	}
	if _, ok := globalRegistry.handler(wire); !ok {
		t.Fatalf("handler not registered for %q", wire)
	}
}

func TestRegisterInvalid(t *testing.T) {
	orig := globalRegistry
	globalRegistry = newRegistry()
	t.Cleanup(func() {
		globalRegistry = orig
	})
	err := Register[int]()
	if err == nil {
		t.Fatalf("expected error")
	}
	var invalid ErrInvalidMessage
	if !errors.As(err, &invalid) {
		t.Fatalf("expected ErrInvalidMessage, got %v", err)
	}
}

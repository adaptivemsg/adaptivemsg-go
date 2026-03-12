package adaptivemsg

import (
	"errors"
	"testing"
)

type messageTestNamed struct{}

func (*messageTestNamed) WireName() string {
	return "am.test.NamedMsg"
}

type messageTestEmptyWire struct{}

func (*messageTestEmptyWire) WireName() string {
	return ""
}

type messageTestDefault struct{}

func TestWireNameOfNamed(t *testing.T) {
	name, err := WireNameOf(&messageTestNamed{})
	if err != nil {
		t.Fatalf("WireNameOf: %v", err)
	}
	if name != "am.test.NamedMsg" {
		t.Fatalf("WireNameOf got %q want %q", name, "am.test.NamedMsg")
	}

	name, err = WireNameOf(messageTestNamed{})
	if err != nil {
		t.Fatalf("WireNameOf value: %v", err)
	}
	if name != "am.test.NamedMsg" {
		t.Fatalf("WireNameOf value got %q want %q", name, "am.test.NamedMsg")
	}
}

func TestWireNameOfDefault(t *testing.T) {
	name, err := WireNameOf(&messageTestDefault{})
	if err != nil {
		t.Fatalf("WireNameOf: %v", err)
	}
	if name != "am.adaptivemsg.messageTestDefault" {
		t.Fatalf("WireNameOf got %q want %q", name, "am.adaptivemsg.messageTestDefault")
	}
}

func TestWireNameOfErrors(t *testing.T) {
	if _, err := WireNameOf(nil); err == nil {
		t.Fatalf("expected error for nil message")
	} else {
		var invalid ErrInvalidMessage
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidMessage, got %v", err)
		}
	}

	if _, err := WireNameOf(&messageTestEmptyWire{}); err == nil {
		t.Fatalf("expected error for empty wire name")
	} else {
		var invalid ErrInvalidMessage
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidMessage, got %v", err)
		}
	}

	if _, err := WireNameOf("not a struct"); err == nil {
		t.Fatalf("expected error for non-struct message")
	} else {
		var invalid ErrInvalidMessage
		if !errors.As(err, &invalid) {
			t.Fatalf("expected ErrInvalidMessage, got %v", err)
		}
	}
}

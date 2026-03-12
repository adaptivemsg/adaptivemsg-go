package adaptivemsg

import (
	"errors"
	"net"
	"runtime"
	"testing"
)

func TestParseAddr(t *testing.T) {
	cases := []struct {
		input   string
		network string
		addr    string
	}{
		{input: "tcp://127.0.0.1:1234", network: "tcp", addr: "127.0.0.1:1234"},
		{input: "uds:///tmp/socket", network: "unix", addr: "/tmp/socket"},
		{input: "unix:///tmp/socket", network: "unix", addr: "/tmp/socket"},
		{input: "127.0.0.1:1234", network: "tcp", addr: "127.0.0.1:1234"},
	}

	for _, tc := range cases {
		network, addr, err := parseAddr(tc.input)
		if err != nil {
			t.Fatalf("parseAddr %q: %v", tc.input, err)
		}
		if network != tc.network || addr != tc.addr {
			t.Fatalf("parseAddr %q got %q %q want %q %q", tc.input, network, addr, tc.network, tc.addr)
		}
	}
}

func TestNormalizeUDSPath(t *testing.T) {
	got, err := normalizeUDSPath("unix:///tmp/test.sock")
	if err != nil {
		t.Fatalf("normalizeUDSPath unix: %v", err)
	}
	if got != "/tmp/test.sock" {
		t.Fatalf("normalizeUDSPath unix got %q want %q", got, "/tmp/test.sock")
	}

	got, err = normalizeUDSPath("uds:///tmp/test.sock")
	if err != nil {
		t.Fatalf("normalizeUDSPath uds: %v", err)
	}
	if got != "/tmp/test.sock" {
		t.Fatalf("normalizeUDSPath uds got %q want %q", got, "/tmp/test.sock")
	}

	got, err = normalizeUDSPath("@abstract")
	if runtime.GOOS == "linux" {
		if err != nil {
			t.Fatalf("normalizeUDSPath abstract: %v", err)
		}
		if got != "\x00abstract" {
			t.Fatalf("normalizeUDSPath abstract got %q want %q", got, "\x00abstract")
		}
	} else {
		var unsupported ErrUnsupportedTransport
		if !errors.As(err, &unsupported) {
			t.Fatalf("expected ErrUnsupportedTransport, got %v", err)
		}
	}
}

func TestIsTimeout(t *testing.T) {
	if !isTimeout(&net.DNSError{IsTimeout: true}) {
		t.Fatalf("expected timeout true")
	}
	if isTimeout(errors.New("no")) {
		t.Fatalf("expected timeout false")
	}
}

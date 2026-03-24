package adaptivemsg

import (
	"errors"
	"testing"
)

func TestReplayBufferAddTracksBytesAndSnapshot(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	frame := outboundFrame{
		streamID: 7,
		seq:      1,
		payload:  []byte("hello"),
	}

	stored, err := buffer.add(frame)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	size, err := replayEntrySize(protocolVersionV3, frame)
	if err != nil {
		t.Fatalf("replayEntrySize: %v", err)
	}
	if got := buffer.bytesUsed(); got != size {
		t.Fatalf("bytesUsed got %d want %d", got, size)
	}

	if got := string(stored.payload); got != "hello" {
		t.Fatalf("stored payload got %q want %q", got, "hello")
	}
	frames := buffer.snapshotFrom(0)
	if len(frames) != 1 {
		t.Fatalf("snapshot length got %d want 1", len(frames))
	}
	if got := string(frames[0].payload); got != "hello" {
		t.Fatalf("buffer payload mutated, got %q want %q", got, "hello")
	}
}

func TestReplayBufferAckDropsCumulativePrefix(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	frames := []outboundFrame{
		{streamID: 1, seq: 1, payload: []byte("one")},
		{streamID: 2, seq: 2, payload: []byte("two")},
		{streamID: 1, seq: 3, payload: []byte("three")},
	}

	var expectedRemaining int64
	for i, frame := range frames {
		if _, err := buffer.add(frame); err != nil {
			t.Fatalf("add frame %d: %v", i, err)
		}
		if i >= 2 {
			size, err := replayEntrySize(protocolVersionV3, frame)
			if err != nil {
				t.Fatalf("replayEntrySize: %v", err)
			}
			expectedRemaining += size
		}
	}

	dropped := buffer.ack(2)
	if dropped == 0 {
		t.Fatalf("ack dropped 0 bytes, want > 0")
	}
	if got := buffer.lastAckedSeq(); got != 2 {
		t.Fatalf("lastAckedSeq got %d want 2", got)
	}
	if got := buffer.bytesUsed(); got != expectedRemaining {
		t.Fatalf("bytesUsed after ack got %d want %d", got, expectedRemaining)
	}

	remaining := buffer.snapshotFrom(0)
	if len(remaining) != 1 {
		t.Fatalf("remaining frames got %d want 1", len(remaining))
	}
	if remaining[0].seq != 3 {
		t.Fatalf("remaining seq got %d want 3", remaining[0].seq)
	}
}

func TestReplayBufferSnapshotFromSeq(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	for _, frame := range []outboundFrame{
		{streamID: 1, seq: 10, payload: []byte("a")},
		{streamID: 1, seq: 11, payload: []byte("b")},
		{streamID: 2, seq: 12, payload: []byte("c")},
	} {
		if _, err := buffer.add(frame); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	frames := buffer.snapshotFrom(10)
	if len(frames) != 2 {
		t.Fatalf("snapshot length got %d want 2", len(frames))
	}
	if frames[0].seq != 11 || frames[1].seq != 12 {
		t.Fatalf("snapshot seqs got %d,%d want 11,12", frames[0].seq, frames[1].seq)
	}
}

func TestReplayBufferFull(t *testing.T) {
	frame := outboundFrame{
		streamID: 1,
		seq:      1,
		payload:  []byte("payload"),
	}
	size, err := replayEntrySize(protocolVersionV3, frame)
	if err != nil {
		t.Fatalf("replayEntrySize: %v", err)
	}

	buffer := newReplayBuffer(protocolVersionV3, size-1)
	_, err = buffer.add(frame)

	var full ErrReplayBufferFull
	if !errors.As(err, &full) {
		t.Fatalf("expected ErrReplayBufferFull, got %v", err)
	}
	if full.Limit != size-1 {
		t.Fatalf("limit got %d want %d", full.Limit, size-1)
	}
	if full.Size != size {
		t.Fatalf("size got %d want %d", full.Size, size)
	}
}

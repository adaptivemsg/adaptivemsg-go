package adaptivemsg

import (
	"errors"
	"testing"
)

func mustStage(t *testing.T, buffer *replayBuffer, streamID uint32, seq uint64, payload string) *frameRecord {
	t.Helper()
	record, held, err := buffer.stage(outboundFrame{streamID: streamID, payload: []byte(payload)}, seq)
	if err != nil || held {
		t.Fatalf("stage seq %d: held=%v err=%v", seq, held, err)
	}
	return record
}

func replayFrameSize(payload string) int64 {
	return int64(frameHeaderLenV3 + len(payload))
}

func TestReplayBufferStageTracksBytesAndSnapshot(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	stored := mustStage(t, buffer, 7, 1, "hello")

	if got, want := buffer.bytesUsed(), replayFrameSize("hello"); got != want {
		t.Fatalf("bytesUsed got %d want %d", got, want)
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
	mustStage(t, buffer, 1, 1, "one")
	mustStage(t, buffer, 2, 2, "two")
	mustStage(t, buffer, 1, 3, "three")

	dropped := buffer.ack(2)
	if want := replayFrameSize("one") + replayFrameSize("two"); dropped != want {
		t.Fatalf("ack dropped %d bytes, want %d", dropped, want)
	}
	if got := buffer.lastAckedSeq(); got != 2 {
		t.Fatalf("lastAckedSeq got %d want 2", got)
	}
	if got, want := buffer.bytesUsed(), replayFrameSize("three"); got != want {
		t.Fatalf("bytesUsed after ack got %d want %d", got, want)
	}

	remaining := buffer.snapshotFrom(0)
	if len(remaining) != 1 || remaining[0].seq != 3 {
		t.Fatalf("remaining frames got %v, want just seq 3", remaining)
	}
	if got := buffer.count(); got != 1 {
		t.Fatalf("count got %d want 1", got)
	}
}

func TestReplayBufferSnapshotFromSeq(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	mustStage(t, buffer, 1, 10, "a")
	mustStage(t, buffer, 1, 11, "b")
	mustStage(t, buffer, 2, 12, "c")

	frames := buffer.snapshotFrom(10)
	if len(frames) != 2 {
		t.Fatalf("snapshot length got %d want 2", len(frames))
	}
	if frames[0].seq != 11 || frames[1].seq != 12 {
		t.Fatalf("snapshot seqs got %d,%d want 11,12", frames[0].seq, frames[1].seq)
	}
	if frames := buffer.snapshotFrom(12); frames != nil {
		t.Fatalf("snapshot after the last frame got %v, want none", frames)
	}
}

func TestReplayBufferRejectsFrameLargerThanBuffer(t *testing.T) {
	const payload = "payload"
	buffer := newReplayBuffer(protocolVersionV3, replayFrameSize(payload)-1)
	_, held, err := buffer.stage(outboundFrame{streamID: 1, payload: []byte(payload)}, 1)
	if held {
		t.Fatal("a frame larger than the whole buffer was held")
	}
	var tooLarge ErrFrameTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if tooLarge.Size != len(payload) {
		t.Fatalf("size got %d want %d", tooLarge.Size, len(payload))
	}
}

func TestReplayBufferHoldsFrameUntilAck(t *testing.T) {
	const payload = "payload"
	buffer := newReplayBuffer(protocolVersionV3, replayFrameSize(payload))
	mustStage(t, buffer, 1, 1, payload)

	second := outboundFrame{streamID: 1, payload: []byte(payload)}
	if record, held, err := buffer.stage(second, 2); record != nil || !held || err != nil {
		t.Fatalf("stage got record=%v held=%v err=%v, want held", record, held, err)
	}
	buffer.ack(1)
	if _, held, err := buffer.stage(second, 2); held || err != nil {
		t.Fatalf("stage after the ACK got held=%v err=%v", held, err)
	}
}

func TestReplayBufferRejectsStaleSeq(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 1024)
	mustStage(t, buffer, 1, 5, "a")
	if _, _, err := buffer.stage(outboundFrame{payload: []byte("b")}, 5); err == nil {
		t.Fatal("a repeated sequence number was accepted")
	}
	buffer.ack(5)
	if _, _, err := buffer.stage(outboundFrame{payload: []byte("c")}, 4); err == nil {
		t.Fatal("an acknowledged sequence number was accepted")
	}
}

// Acknowledging frames one at a time must not move the frames after them each
// time, and must keep the rest in order.
func TestReplayBufferManyAcks(t *testing.T) {
	buffer := newReplayBuffer(protocolVersionV3, 0)
	const n = 1000
	for seq := uint64(1); seq <= n; seq++ {
		mustStage(t, buffer, 1, seq, "x")
	}
	for seq := uint64(1); seq <= n/2; seq++ {
		buffer.ack(seq)
	}
	frames := buffer.snapshotFrom(0)
	if len(frames) != n/2 || frames[0].seq != n/2+1 || frames[len(frames)-1].seq != n {
		t.Fatalf("after %d ACKs kept %d frames from %d", n/2, len(frames), frames[0].seq)
	}
	if got, want := buffer.bytesUsed(), int64(n/2)*replayFrameSize("x"); got != want {
		t.Fatalf("bytesUsed got %d want %d", got, want)
	}
}

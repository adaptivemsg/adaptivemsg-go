package adaptivemsg

import (
	"sync"
	"sync/atomic"
)

type replayEntry struct {
	frame *frameRecord
}

type replayBuffer struct {
	mu          sync.Mutex
	version     byte
	maxBytes    int64
	usedBytes   int64
	lastAcked   atomic.Uint64
	replayQueue []replayEntry
}

func newReplayBuffer(version byte, maxBytes int64) *replayBuffer {
	return &replayBuffer{
		version:  version,
		maxBytes: maxBytes,
	}
}

func (b *replayBuffer) add(frame outboundFrame) (*frameRecord, error) {
	if b == nil || frame.seq == 0 {
		return &frameRecord{
			streamID: frame.streamID,
			seq:      frame.seq,
			payload:  frame.payload,
		}, nil
	}

	size, err := replayEntrySize(b.version, frame)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	lastAcked := b.lastAcked.Load()
	if frame.seq <= lastAcked {
		return nil, ErrInvalidMessage{Reason: "replay frame seq already acknowledged"}
	}
	if n := len(b.replayQueue); n > 0 && frame.seq <= b.replayQueue[n-1].frame.seq {
		return nil, ErrInvalidMessage{Reason: "replay frame seq must increase monotonically"}
	}

	nextUsed := b.usedBytes + size
	if b.maxBytes > 0 && nextUsed > b.maxBytes {
		return nil, ErrReplayBufferFull{Limit: b.maxBytes, Size: nextUsed}
	}

	record := &frameRecord{
		streamID: frame.streamID,
		seq:      frame.seq,
		payload:  frame.payload,
		size:     size,
	}
	b.replayQueue = append(b.replayQueue, replayEntry{
		frame: record,
	})
	b.usedBytes = nextUsed
	return record, nil
}

func (b *replayBuffer) ack(lastSeq uint64) int64 {
	if b == nil || lastSeq == 0 {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if lastSeq <= b.lastAcked.Load() {
		return 0
	}
	b.lastAcked.Store(lastSeq)

	var dropped int64
	dropCount := 0
	for dropCount < len(b.replayQueue) && b.replayQueue[dropCount].frame.seq <= lastSeq {
		dropped += b.replayQueue[dropCount].frame.size
		dropCount++
	}
	if dropCount > 0 {
		copy(b.replayQueue, b.replayQueue[dropCount:])
		b.replayQueue = b.replayQueue[:len(b.replayQueue)-dropCount]
		b.usedBytes -= dropped
	}
	return dropped
}

func (b *replayBuffer) snapshotFrom(lastSeq uint64) []*frameRecord {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	start := 0
	for start < len(b.replayQueue) && b.replayQueue[start].frame.seq <= lastSeq {
		start++
	}
	if start == len(b.replayQueue) {
		return nil
	}

	frames := make([]*frameRecord, 0, len(b.replayQueue)-start)
	for _, entry := range b.replayQueue[start:] {
		frames = append(frames, entry.frame)
	}
	return frames
}

func (b *replayBuffer) bytesUsed() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usedBytes
}

func (b *replayBuffer) lastAckedSeq() uint64 {
	if b == nil {
		return 0
	}
	return b.lastAcked.Load()
}

func replayEntrySize(version byte, frame outboundFrame) (int64, error) {
	headerLen, err := frameHeaderLenForVersion(version)
	if err != nil {
		return 0, err
	}
	return int64(headerLen + len(frame.payload)), nil
}

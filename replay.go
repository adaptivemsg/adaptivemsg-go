package adaptivemsg

import (
	"sort"
	"sync"
	"sync/atomic"
)

type frameRecord struct {
	streamID uint32
	seq      uint64
	flags    FrameFlags
	payload  []byte
	size     int64
}

type replayBuffer struct {
	mu        sync.Mutex
	headerLen int
	maxBytes  int64
	usedBytes int64
	lastAcked atomic.Uint64
	// frames[head:] are the frames kept, in sequence order. An ACK advances
	// head instead of moving the frames after it.
	frames []*frameRecord
	head   int
}

func newReplayBuffer(version byte, maxBytes int64) *replayBuffer {
	headerLen, err := frameHeaderLenForVersion(version)
	if err != nil {
		headerLen = frameHeaderLenV3
	}
	return &replayBuffer{
		headerLen: headerLen,
		maxBytes:  maxBytes,
	}
}

// stage keeps frame under sequence number seq unless it has to wait for room.
// held reports that it waits: it does not fit next to the frames kept now,
// but will once they are acknowledged.
func (b *replayBuffer) stage(frame outboundFrame, seq uint64) (record *frameRecord, held bool, err error) {
	size := int64(b.headerLen + len(frame.payload))
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.maxBytes > 0 && b.usedBytes+size > b.maxBytes {
		if size <= b.maxBytes {
			return nil, true, nil
		}
		return nil, false, ErrFrameTooLarge{Size: len(frame.payload)}
	}
	if seq <= b.lastAcked.Load() {
		return nil, false, ErrInvalidMessage{Reason: "replay frame seq already acknowledged"}
	}
	if n := len(b.frames); n > b.head && seq <= b.frames[n-1].seq {
		return nil, false, ErrInvalidMessage{Reason: "replay frame seq must increase monotonically"}
	}

	record = &frameRecord{
		streamID: frame.streamID,
		seq:      seq,
		flags:    frame.flags,
		payload:  frame.payload,
		size:     size,
	}
	b.frames = append(b.frames, record)
	b.usedBytes += size
	return record, false, nil
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
	for b.head < len(b.frames) && b.frames[b.head].seq <= lastSeq {
		dropped += b.frames[b.head].size
		b.frames[b.head] = nil
		b.head++
	}
	b.usedBytes -= dropped
	switch {
	case b.head == len(b.frames):
		b.frames = b.frames[:0]
		b.head = 0
	case b.head > len(b.frames)/2:
		// Move the frames kept to the front once they are the smaller part,
		// so the slice does not grow without bound.
		n := copy(b.frames, b.frames[b.head:])
		clear(b.frames[n:])
		b.frames = b.frames[:n]
		b.head = 0
	}
	return dropped
}

// snapshotFrom returns the frames after lastSeq.
func (b *replayBuffer) snapshotFrom(lastSeq uint64) []*frameRecord {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	kept := b.frames[b.head:]
	start := sort.Search(len(kept), func(i int) bool { return kept[i].seq > lastSeq })
	if start == len(kept) {
		return nil
	}
	return append([]*frameRecord(nil), kept[start:]...)
}

func (b *replayBuffer) bytesUsed() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usedBytes
}

func (b *replayBuffer) count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.frames) - b.head
}

func (b *replayBuffer) lastAckedSeq() uint64 {
	if b == nil {
		return 0
	}
	return b.lastAcked.Load()
}

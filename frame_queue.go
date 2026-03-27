package adaptivemsg

import "sync"

type frameDeque struct {
	mu   sync.Mutex
	buf  []*frameRecord
	head int
	tail int
	size int
}

func newFrameDeque() *frameDeque {
	return &frameDeque{}
}

func (q *frameDeque) push(frame *frameRecord) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.buf) == 0 {
		q.buf = make([]*frameRecord, 32)
	}
	if q.size == len(q.buf) {
		q.grow()
	}

	q.buf[q.tail] = frame
	q.tail = (q.tail + 1) % len(q.buf)
	q.size++
}

func (q *frameDeque) pop() (*frameRecord, bool) {
	if q == nil {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.size == 0 {
		return nil, false
	}

	frame := q.buf[q.head]
	q.buf[q.head] = nil
	q.head = (q.head + 1) % len(q.buf)
	q.size--
	if q.size == 0 {
		q.head = 0
		q.tail = 0
	}
	return frame, true
}

func (q *frameDeque) reset(frames []*frameRecord) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(frames) == 0 {
		q.buf = nil
		q.head = 0
		q.tail = 0
		q.size = 0
		return
	}

	q.buf = frames
	q.head = 0
	q.size = len(frames)
	if q.size == len(q.buf) {
		q.tail = 0
	} else {
		q.tail = q.size
	}
}

func (q *frameDeque) grow() {
	newBuf := make([]*frameRecord, len(q.buf)*2)
	for i := 0; i < q.size; i++ {
		newBuf[i] = q.buf[(q.head+i)%len(q.buf)]
	}
	q.buf = newBuf
	q.head = 0
	q.tail = q.size
}

func (q *frameDeque) len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}

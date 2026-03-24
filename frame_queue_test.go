package adaptivemsg

import "testing"

func TestFrameDequePushPopOrderAndGrowth(t *testing.T) {
	q := newFrameDeque()

	for i := 1; i <= 40; i++ {
		q.push(&frameRecord{seq: uint64(i)})
	}
	for i := 1; i <= 25; i++ {
		frame, ok := q.pop()
		if !ok {
			t.Fatalf("pop %d: queue empty", i)
		}
		if got := frame.seq; got != uint64(i) {
			t.Fatalf("pop %d got seq %d want %d", i, got, i)
		}
	}
	for i := 41; i <= 80; i++ {
		q.push(&frameRecord{seq: uint64(i)})
	}
	for i := 26; i <= 80; i++ {
		frame, ok := q.pop()
		if !ok {
			t.Fatalf("pop %d: queue empty", i)
		}
		if got := frame.seq; got != uint64(i) {
			t.Fatalf("pop %d got seq %d want %d", i, got, i)
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("expected empty queue after draining")
	}
}

func TestFrameDequeResetReplacesContents(t *testing.T) {
	q := newFrameDeque()
	q.push(&frameRecord{seq: 1})
	q.push(&frameRecord{seq: 2})

	q.reset([]*frameRecord{
		{seq: 10},
		{seq: 11},
		{seq: 12},
	})

	for _, want := range []uint64{10, 11, 12} {
		frame, ok := q.pop()
		if !ok {
			t.Fatalf("pop want %d: queue empty", want)
		}
		if frame.seq != want {
			t.Fatalf("pop got seq %d want %d", frame.seq, want)
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("expected empty queue after reset drain")
	}

	q.reset(nil)
	if _, ok := q.pop(); ok {
		t.Fatal("expected empty queue after reset(nil)")
	}
}

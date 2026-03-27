package adaptivemsg

import (
	"testing"
	"time"
)

// benchRecoveryState builds a minimal recoveryState with default negotiated options.
func benchRecoveryState() *recoveryState {
	state := &recoveryState{
		role:        recoveryRoleClient,
		replay:      newReplayBuffer(protocolVersionV3, 1<<20),
		liveQueue:   newFrameDeque(),
		resumeQueue: newFrameDeque(),
	}
	state.setNegotiated(defaultServerRecoveryOptions().negotiated())
	return state
}

// BenchmarkNextAckWait_NoPending measures the common idle case:
// no ack pending, ackDueAt is zero — should return 0 immediately after mutex.
func BenchmarkNextAckWait_NoPending(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.nextAckWait()
	}
}

// BenchmarkNextAckWait_WithDelay measures the case where an ack is pending with
// a future deadline, requiring time.Until.
func BenchmarkNextAckWait_WithDelay(b *testing.B) {
	r := benchRecoveryState()
	r.mu.Lock()
	r.ackDueAt = time.Now().Add(10 * time.Millisecond)
	r.mu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.nextAckWait()
	}
}

// BenchmarkTakePendingControl_Empty measures the fast path: nothing to send,
// returns pendingControlNone immediately.
func BenchmarkTakePendingControl_Empty(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = r.takePendingControl()
	}
}

// BenchmarkTakePendingControl_AckReady measures the ack send path: ackDue=true,
// lastRecvSeq > lastAckSent — clears and returns the seq.
func BenchmarkTakePendingControl_AckReady(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.mu.Lock()
		r.lastRecvSeq++
		r.ackDue = true
		r.mu.Unlock()
		_, _, _ = r.takePendingControl()
	}
}

// BenchmarkWaitCalc measures the wait duration selection: one nextAckWait()
// mutex call plus one atomic heartbeatIntv load plus the min comparison.
// This is the core of the idle path in recoveryWriterLoop after the refactor.
func BenchmarkWaitCalc(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ackWait := r.nextAckWait()
		heartbeatWait := time.Duration(r.heartbeatIntv.Load())
		wait := ackWait
		if heartbeatWait > 0 && (ackWait <= 0 || heartbeatWait < ackWait) {
			wait = heartbeatWait
		}
		_ = wait
	}
}

// BenchmarkWaitCalc_AckBeatsHeartbeat measures the case where a pending ack
// delay is shorter than the heartbeat interval — heartbeat branch not taken.
func BenchmarkWaitCalc_AckBeatsHeartbeat(b *testing.B) {
	r := benchRecoveryState()
	shortDelay := 5 * time.Millisecond
	r.mu.Lock()
	r.ackDueAt = time.Now().Add(shortDelay)
	r.mu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ackWait := r.nextAckWait()
		heartbeatWait := time.Duration(r.heartbeatIntv.Load())
		wait := ackWait
		if heartbeatWait > 0 && (ackWait <= 0 || heartbeatWait < ackWait) {
			wait = heartbeatWait
		}
		_ = wait
	}
}

// BenchmarkWaitCalc_V2OldStyle is v2 baseline: uses intermediate bool variable.
// This is the BEFORE state to compare against the optimized v3.
func BenchmarkWaitCalc_V2OldStyle(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wait := r.nextAckWait()
		heartbeatWait := time.Duration(r.heartbeatIntv.Load())
		waitForHeartbeat := heartbeatWait > 0 && (wait <= 0 || heartbeatWait < wait)
		if waitForHeartbeat {
			wait = heartbeatWait
		}
		// v2: check bool at second location
		if waitForHeartbeat {
			_ = wait
		}
	}
}

// BenchmarkWaitCalc_V3Optimized is the refactored version: no intermediate bool.
// Compares logic path against v2 to quantify the optimization.
func BenchmarkWaitCalc_V3Optimized(b *testing.B) {
	r := benchRecoveryState()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ackWait := r.nextAckWait()
		heartbeatWait := time.Duration(r.heartbeatIntv.Load())
		wait := ackWait
		if heartbeatWait > 0 && (ackWait <= 0 || heartbeatWait < ackWait) {
			wait = heartbeatWait
		}
		// v3: direct comparison, no bool variable
		if wait == heartbeatWait {
			_ = wait
		}
	}
}

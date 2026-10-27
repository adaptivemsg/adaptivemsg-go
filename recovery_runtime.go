package adaptivemsg

import (
	"bufio"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type recoveryRole byte

const (
	recoveryRoleClient recoveryRole = 1
	recoveryRoleServer recoveryRole = 2
)

const (
	pendingControlNone byte = iota
	pendingControlAck
)

type recoveryState struct {
	role   recoveryRole
	server *Server

	addr    string
	timeout time.Duration

	reconnectMinBackoff time.Duration
	reconnectMaxBackoff time.Duration
	detachedTTL         time.Duration

	connectionID recoveryToken
	resumeSecret recoveryToken

	replay *replayBuffer

	reconnectActive atomic.Bool

	// resumeDepth is how many frames the writer has left to replay.
	resumeDepth atomic.Int64

	mu            sync.Mutex
	shared        negotiatedRecoveryOptions
	ackEvery      atomic.Uint32
	ackDelayNanos atomic.Int64
	heartbeatIntv atomic.Int64
	heartbeatTO   atomic.Int64
	lastRecvSeq   uint64
	lastAckSent   uint64
	ackPending    uint32
	// ackDue and ackDueAt change under mu; the writer reads them without it
	// to skip the lock while no ACK is due. ackDueAt is in Unix nanoseconds,
	// zero while no ACK is scheduled.
	ackDue      atomic.Bool
	ackDueAt    atomic.Int64
	expireTimer *time.Timer
}

func newClientRecoveryState(opts ClientRecoveryOptions, negotiated negotiatedRecoveryOptions, addr string, timeout time.Duration, connectionID, resumeSecret recoveryToken) *recoveryState {
	normalized := opts.normalized()
	state := &recoveryState{
		role:                recoveryRoleClient,
		addr:                addr,
		timeout:             timeout,
		reconnectMinBackoff: normalized.ReconnectMinBackoff,
		reconnectMaxBackoff: normalized.ReconnectMaxBackoff,
		connectionID:        connectionID,
		resumeSecret:        resumeSecret,
		replay:              newReplayBuffer(protocolVersionV3, normalized.MaxReplayBytes),
	}
	state.setNegotiated(negotiated)
	return state
}

func newServerRecoveryState(opts ServerRecoveryOptions, server *Server, connectionID, resumeSecret recoveryToken) *recoveryState {
	normalized := opts.normalized()
	state := &recoveryState{
		role:         recoveryRoleServer,
		server:       server,
		detachedTTL:  normalized.DetachedTTL,
		connectionID: connectionID,
		resumeSecret: resumeSecret,
		replay:       newReplayBuffer(protocolVersionV3, normalized.MaxReplayBytes),
	}
	state.setNegotiated(normalized.negotiated())
	return state
}

func (c *Connection) initTransportState() {
	if c == nil {
		return
	}
	c.transportOnce.Do(func() {
		c.transportCond = sync.NewCond(&c.transportMu)
		if c.sendNotify == nil {
			c.sendNotify = make(chan struct{}, 1)
		}
	})
}

func (c *Connection) signalSend() {
	if c == nil {
		return
	}
	c.initTransportState()
	select {
	case c.sendNotify <- struct{}{}:
	default:
	}
}

func (c *Connection) waitForTransport(lastGen uint64) (net.Conn, uint64, uint64, error) {
	c.initTransportState()
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	for {
		if c.closed.Load() {
			return nil, 0, 0, ErrClosed{}
		}
		if gen := c.transportGen.Load(); c.conn != nil && gen != lastGen {
			return c.conn, gen, c.transportResume, nil
		}
		c.transportCond.Wait()
	}
}

// attachedTransport is waitForTransport without the wait: ok is false while
// no transport newer than lastGen is attached.
func (c *Connection) attachedTransport(lastGen uint64) (net.Conn, uint64, uint64, bool) {
	c.initTransportState()
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if gen := c.transportGen.Load(); c.conn != nil && gen != lastGen {
		return c.conn, gen, c.transportResume, true
	}
	return nil, 0, 0, false
}

// transportActive reports whether the transport attached as generation gen is
// still the current one. Every attach, detach and close moves to a new
// generation.
func (c *Connection) transportActive(gen uint64) bool {
	return !c.closed.Load() && c.transportGen.Load() == gen
}

func (c *Connection) attachTransport(conn net.Conn, peerLastRecvSeq uint64) {
	c.initTransportState()
	c.transportMu.Lock()
	// markClosed sets closed before it closes the transport under this lock,
	// so a transport attached after the check is closed there.
	if c.closed.Load() {
		c.transportMu.Unlock()
		_ = conn.Close()
		return
	}
	oldConn := c.conn
	c.conn = conn
	c.transportGen.Add(1)
	c.transportResume = peerLastRecvSeq
	c.transportMu.Unlock()
	c.debug.transportAttaches.Add(1)

	if oldConn != nil && oldConn != conn {
		_ = oldConn.Close()
	}
	if c.recovery != nil {
		c.recovery.onAttached()
		c.recovery.armReadDeadline(conn)
	}
	c.transportCond.Broadcast()
	c.signalSend()
}

func (c *Connection) detachTransport(conn net.Conn) bool {
	c.initTransportState()
	c.transportMu.Lock()
	if c.conn != conn {
		c.transportMu.Unlock()
		return false
	}
	c.conn = nil
	c.transportGen.Add(1)
	c.transportResume = 0
	c.transportMu.Unlock()
	c.debug.transportDetaches.Add(1)

	if conn != nil {
		_ = conn.Close()
	}
	if c.recovery != nil {
		c.recovery.onDetached(c)
	}
	c.transportCond.Broadcast()
	c.signalSend()
	return true
}

func (c *Connection) closeTransport() {
	c.initTransportState()
	c.transportMu.Lock()
	conn := c.conn
	c.conn = nil
	c.transportGen.Add(1)
	c.transportResume = 0
	c.transportMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	c.transportCond.Broadcast()
	c.signalSend()
}

func (c *Connection) currentTransport() net.Conn {
	c.initTransportState()
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	return c.conn
}

// currentTransportForTest is a test hook for forcing transport-level failures.
func (c *Connection) currentTransportForTest() net.Conn {
	return c.currentTransport()
}

func (r *recoveryState) lastReceived() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRecvSeq
}

func (r *recoveryState) ackReceived(lastSeq uint64) {
	if r == nil || r.replay == nil {
		return
	}
	r.replay.ack(lastSeq)
}

func (r *recoveryState) negotiated() negotiatedRecoveryOptions {
	if r == nil {
		return negotiatedRecoveryOptions{}.normalized()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shared
}

func (r *recoveryState) setNegotiated(opts negotiatedRecoveryOptions) {
	if r == nil {
		return
	}
	normalized := opts.normalized()
	r.mu.Lock()
	r.shared = normalized
	r.mu.Unlock()
	r.ackEvery.Store(normalized.AckEvery)
	r.ackDelayNanos.Store(int64(normalized.AckDelay))
	r.heartbeatIntv.Store(int64(normalized.HeartbeatInterval))
	r.heartbeatTO.Store(int64(normalized.HeartbeatTimeout))
}

// prepareResume returns the frames after lastSeq, the last one the peer
// received, for the writer to replay. That also acknowledges the frames up to
// lastSeq: the ACK the peer sent for them may have been lost with the old
// transport.
func (r *recoveryState) prepareResume(lastSeq uint64) []*frameRecord {
	if r == nil {
		return nil
	}
	r.replay.ack(lastSeq)
	frames := r.replay.snapshotFrom(lastSeq)
	r.resumeDepth.Store(int64(len(frames)))
	return frames
}

func (r *recoveryState) noteReceived(c *Connection, seq uint64) {
	if r == nil {
		return
	}

	r.mu.Lock()
	if seq <= r.lastRecvSeq {
		if r.lastAckSent < r.lastRecvSeq {
			r.ackDue.Store(true)
		}
		r.mu.Unlock()
		c.signalSend()
		return
	}
	r.lastRecvSeq = seq
	r.ackPending++
	notify := false
	ackEvery := r.ackEvery.Load()
	if ackEvery <= 1 || r.ackPending >= ackEvery {
		r.ackDue.Store(true)
		r.ackDueAt.Store(0)
		notify = true
	} else {
		ackDelay := time.Duration(r.ackDelayNanos.Load())
		if ackDelay <= 0 {
			r.ackDue.Store(true)
			r.ackDueAt.Store(0)
			notify = true
		} else if r.ackDueAt.Load() == 0 {
			r.ackDueAt.Store(time.Now().Add(ackDelay).UnixNano())
			notify = true
		}
	}
	r.mu.Unlock()
	if notify {
		c.signalSend()
	}
}

func (r *recoveryState) nextAckWait() time.Duration {
	if r == nil || r.ackDue.Load() {
		return 0
	}
	dueAt := r.ackDueAt.Load()
	if dueAt == 0 {
		return 0
	}
	return max(time.Duration(dueAt-time.Now().UnixNano()), 0)
}

func (r *recoveryState) takePendingControl() (byte, uint64, bool) {
	if r == nil {
		return pendingControlNone, 0, false
	}
	if !r.ackDue.Load() {
		if dueAt := r.ackDueAt.Load(); dueAt == 0 || time.Now().UnixNano() < dueAt {
			return pendingControlNone, 0, false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastAckSent >= r.lastRecvSeq {
		r.ackDue.Store(false)
		r.ackDueAt.Store(0)
		return pendingControlNone, 0, false
	}
	if !r.ackDue.Load() {
		if dueAt := r.ackDueAt.Load(); dueAt == 0 || time.Now().UnixNano() < dueAt {
			return pendingControlNone, 0, false
		}
	}
	seq := r.lastRecvSeq
	r.lastAckSent = seq
	r.ackPending = 0
	r.ackDue.Store(false)
	r.ackDueAt.Store(0)
	return pendingControlAck, seq, true
}

func (r *recoveryState) onDetached(c *Connection) {
	if r == nil {
		return
	}
	switch r.role {
	case recoveryRoleClient:
		go c.reconnectLoop()
	case recoveryRoleServer:
		r.scheduleExpiry(c)
	}
}

func (r *recoveryState) onAttached() {
	if r == nil {
		return
	}
	r.mu.Lock()
	timer := r.expireTimer
	r.expireTimer = nil
	r.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (r *recoveryState) onClosed(c *Connection) {
	if r == nil {
		return
	}
	r.mu.Lock()
	timer := r.expireTimer
	r.expireTimer = nil
	r.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if r.role == recoveryRoleServer && r.server != nil {
		r.server.removeRecoveryConnection(r.connectionID, c)
	}
}

func (r *recoveryState) scheduleExpiry(c *Connection) {
	if r == nil {
		return
	}
	ttl := r.detachedTTL
	if ttl <= 0 {
		c.markClosed()
		return
	}

	r.mu.Lock()
	if r.expireTimer != nil {
		r.expireTimer.Stop()
	}
	r.expireTimer = time.AfterFunc(ttl, func() {
		if c.currentTransport() == nil && !c.closed.Load() {
			c.markClosed()
		}
	})
	r.mu.Unlock()
}

func (r *recoveryState) armReadDeadline(conn net.Conn) {
	if r == nil || conn == nil {
		return
	}
	timeout := time.Duration(r.heartbeatTO.Load())
	if timeout <= 0 {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
}

// testHookReconnected runs after a reconnect attaches a transport, before the
// reconnect loop ends.
var testHookReconnected atomic.Pointer[func(*Connection)]

func (c *Connection) reconnectLoop() {
	if c == nil || c.recovery == nil || c.recovery.role != recoveryRoleClient {
		return
	}
	for c.recovery.reconnectActive.CompareAndSwap(false, true) {
		c.reconnectUntilAttached()
		c.recovery.reconnectActive.Store(false)
		// A detach while the flag was set started no loop of its own.
		if c.closed.Load() || c.currentTransport() != nil {
			return
		}
	}
}

// reconnectUntilAttached retries the resume, backing off between attempts,
// until a transport is attached, the resume fails for good, or the connection
// closes.
func (c *Connection) reconnectUntilAttached() {
	backoff := c.recovery.reconnectMinBackoff
	if backoff <= 0 {
		backoff = defaultClientRecoveryOptions().ReconnectMinBackoff
	}
	maxBackoff := c.recovery.reconnectMaxBackoff
	if maxBackoff < backoff {
		maxBackoff = backoff
	}

	for {
		if c.closed.Load() {
			return
		}
		c.debug.reconnectAttempts.Add(1)
		_, err := c.resumeClientTransport()
		if err == nil {
			c.debug.reconnectSuccesses.Add(1)
			if hook := testHookReconnected.Load(); hook != nil {
				(*hook)(c)
			}
			return
		}
		c.debug.noteFailure(DebugFailureReconnectResume, "resume failed: "+err.Error())
		c.debug.reconnectFailures.Add(1)
		var rejected ErrResumeRejected
		var unsupported ErrUnsupportedFrameVersion
		if errors.As(err, &rejected) || errors.As(err, &unsupported) {
			c.debug.noteFailure(DebugFailureReconnectTerminal, "reconnect terminal failure: "+err.Error())
			c.markClosed()
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-c.closeCh:
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

var (
	// errTransportLost ends writing to the current transport; the writer
	// waits for the next one.
	errTransportLost = errors.New("recovery transport lost")
	// errWriterDone ends the writer: the connection is closed.
	errWriterDone = errors.New("recovery writer done")
)

// recoveryWriter is the state of the v3 writer goroutine; only that
// goroutine uses it.
type recoveryWriter struct {
	c *Connection
	// held is a frame that does not fit in the replay buffer yet. While it is
	// held no other frame is taken, so its sender, and the senders queued
	// behind it, wait until an ACK frees room.
	held      *outboundFrame
	waitTimer *time.Timer

	// The transport being written to.
	conn        net.Conn
	bw          *bufio.Writer
	lastSentSeq uint64
	// resume holds the frames to replay on this transport.
	resume []*frameRecord
}

func (c *Connection) recoveryWriterLoop() {
	w := &recoveryWriter{c: c, waitTimer: time.NewTimer(time.Hour)}
	w.waitTimer.Stop()
	defer w.waitTimer.Stop()
	var lastGen uint64
	for {
		var err error
		if conn, gen, resumeSeq, ok := c.attachedTransport(lastGen); ok {
			lastGen = gen
			err = w.writeTransport(conn, gen, resumeSeq)
		} else {
			err = w.stageWhileDetached()
		}
		if err == errWriterDone {
			return
		}
	}
}

// stageWhileDetached waits for a frame while no transport is attached and
// stages it, so that the resume replays it.
func (w *recoveryWriter) stageWhileDetached() error {
	select {
	case <-w.c.closeCh:
		return errWriterDone
	case <-w.c.sendNotify:
	case frame := <-w.outbound():
		if frame.closing != nil {
			close(frame.closing)
			return errWriterDone
		}
		w.stage(frame)
	}
	return nil
}

// writeTransport writes to conn until the transport is lost or the writer is
// done.
func (w *recoveryWriter) writeTransport(conn net.Conn, gen, resumeSeq uint64) error {
	c := w.c
	w.conn = conn
	w.bw = bufio.NewWriter(conn)
	// The peer cannot have received more than was sent.
	w.lastSentSeq = min(resumeSeq, c.nextSendSeq.Load())
	w.resume = c.recovery.prepareResume(w.lastSentSeq)
	for c.transportActive(gen) {
		wrote, err := w.writeNext()
		if err == nil && !wrote {
			err = w.wait()
		}
		if err == errTransportLost {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// writeNext writes the next frame that is ready: a pending ACK first, then
// the frames to replay, which go before live frames because live frames take
// later sequence numbers. It reports whether there may be more to write.
func (w *recoveryWriter) writeNext() (bool, error) {
	if _, ackSeq, ok := w.c.recovery.takePendingControl(); ok {
		return true, w.write(controlStreamID, 0, 0, buildAckControlPayload(ackSeq), "ack", DebugFailureRecoveryAckWrite)
	}
	if len(w.resume) > 0 {
		return true, w.writeResume()
	}
	frame, ok := w.takeFrame()
	if !ok {
		return false, nil
	}
	if frame.closing != nil {
		w.c.finishRecoveryWriter(w.bw, frame.closing)
		return false, errWriterDone
	}
	if err := w.writeLive(frame); err != nil {
		return false, err
	}
	// A held frame leaves nothing to write until an ACK frees room.
	return w.held == nil, nil
}

// wait flushes the frames written so far and waits for something to write:
// a new frame, a signal that a frame or an ACK is ready, the ACK delay, or
// the heartbeat interval.
func (w *recoveryWriter) wait() error {
	c := w.c
	if w.bw.Buffered() > 0 {
		if err := w.bw.Flush(); err != nil {
			c.debug.noteFailure(DebugFailureRecoveryLiveWrite, "recovery flush failed: "+err.Error())
			c.handleRecoveryTransportError(w.conn)
			return errTransportLost
		}
	}
	ackWait := c.recovery.nextAckWait()
	heartbeatWait := time.Duration(c.recovery.heartbeatIntv.Load())
	wait := ackWait
	if heartbeatWait > 0 && (ackWait <= 0 || heartbeatWait < ackWait) {
		wait = heartbeatWait
	}
	var timeout <-chan time.Time
	if wait > 0 {
		w.waitTimer.Reset(wait)
		defer w.waitTimer.Stop()
		timeout = w.waitTimer.C
	}
	select {
	case <-c.closeCh:
		return errWriterDone
	case <-c.sendNotify:
	case frame := <-w.outbound():
		if frame.closing != nil {
			c.finishRecoveryWriter(w.bw, frame.closing)
			return errWriterDone
		}
		return w.writeLive(frame)
	case <-timeout:
		if wait == heartbeatWait {
			return w.write(controlStreamID, 0, 0, buildPingControlPayload(), "ping", DebugFailureRecoveryPingWrite)
		}
	}
	return nil
}

// outbound returns the queue of new frames, or nil while a frame is held.
func (w *recoveryWriter) outbound() chan outboundFrame {
	if w.held != nil {
		return nil
	}
	return w.c.outbound
}

// takeFrame returns the held frame, or else the next queued frame if there
// is one.
func (w *recoveryWriter) takeFrame() (outboundFrame, bool) {
	if w.held != nil {
		frame := *w.held
		w.held = nil
		return frame, true
	}
	select {
	case frame := <-w.c.outbound:
		return frame, true
	default:
		return outboundFrame{}, false
	}
}

// stage keeps a new frame in the replay buffer under the next sequence number
// and tells its sender the outcome. It returns the kept record, or nil if
// the frame is held or failed.
func (w *recoveryWriter) stage(frame outboundFrame) *frameRecord {
	c := w.c
	// The number is taken only once the frame is kept: the peer closes the
	// connection when a number is skipped. Only this goroutine takes numbers.
	seq := c.nextSendSeq.Load() + 1
	record, held, err := c.recovery.replay.stage(frame, seq)
	if held {
		w.held = &frame
		return nil
	}
	if err == nil {
		c.nextSendSeq.Store(seq)
	}
	if frame.queued != nil {
		frame.queued <- err
	}
	if err != nil {
		c.debug.noteFailure(DebugFailureRecoveryLiveWrite, "recovery live queue failed: "+err.Error())
		return nil
	}
	return record
}

// writeLive stages a new frame and writes it, unless the peer has it.
func (w *recoveryWriter) writeLive(frame outboundFrame) error {
	record := w.stage(frame)
	if record == nil || record.seq <= w.lastSentSeq {
		return nil
	}
	if err := w.write(record.streamID, record.seq, record.flags, record.payload, "live", DebugFailureRecoveryLiveWrite); err != nil {
		return err
	}
	w.lastSentSeq = record.seq
	return nil
}

// writeResume writes the next frame to replay, unless the peer has it.
func (w *recoveryWriter) writeResume() error {
	frame := w.resume[0]
	w.resume = w.resume[1:]
	w.c.recovery.resumeDepth.Store(int64(len(w.resume)))
	if frame.seq <= w.lastSentSeq || frame.seq <= w.c.recovery.replay.lastAckedSeq() {
		return nil
	}
	if err := w.write(frame.streamID, frame.seq, frame.flags, frame.payload, "resume", DebugFailureRecoveryResumeWrite); err != nil {
		return err
	}
	w.lastSentSeq = frame.seq
	return nil
}

// write buffers a frame. On failure it detaches the transport.
func (w *recoveryWriter) write(streamID uint32, seq uint64, flags FrameFlags, payload []byte, kind string, failure DebugFailureCode) error {
	if err := w.c.writeFrameNoFlush(w.bw, streamID, seq, flags, payload); err != nil {
		w.c.debug.noteFailure(failure, "recovery "+kind+" write failed: "+err.Error())
		w.c.handleRecoveryTransportError(w.conn)
		return errTransportLost
	}
	return nil
}

func (c *Connection) recoveryReaderLoop() {
	var lastGen uint64
	for {
		conn, gen, _, err := c.waitForTransport(lastGen)
		if err != nil {
			return
		}
		lastGen = gen
		r := bufio.NewReaderSize(conn, readBufferSize)
		// Only this goroutine advances the last received sequence number.
		lastRecvSeq := c.recovery.lastReceived()

		for {
			if !c.transportActive(gen) {
				break
			}
			c.recovery.armReadDeadline(conn)
			streamID, seq, flags, payload, err := c.readFrameFrom(r)
			if err != nil {
				// Resuming would replay the same frame, so a malformed frame is terminal.
				if isMalformedFrameError(err) {
					c.debug.noteFailure(DebugFailureRecoveryData, "recovery frame rejected: "+err.Error())
					c.markClosed()
					return
				}
				if c.closing.Load() {
					// Close sent CLOSE and waits for the peer to close its side.
					c.markClosed()
					return
				}
				c.debug.noteFailure(DebugFailureRecoveryRead, "recovery read failed: "+err.Error())
				c.handleRecoveryTransportError(conn)
				break
			}
			if streamID == controlStreamID {
				peerClosed, err := c.handleControlFrame(payload)
				if err != nil {
					c.debug.noteFailure(DebugFailureRecoveryControl, "recovery control frame failed: "+err.Error())
					c.markClosed()
					return
				}
				if peerClosed {
					c.closeByPeer()
					return
				}
				continue
			}
			if lastRecvSeq, err = c.handleRecoveryDataFrame(lastRecvSeq, streamID, seq, flags, payload); err != nil {
				if c.closed.Load() {
					return
				}
				c.debug.noteFailure(DebugFailureRecoveryData, "recovery data frame failed: "+err.Error())
				c.markClosed()
				return
			}
		}
	}
}

// handleControlFrame applies a control frame and reports whether it was CLOSE.
func (c *Connection) handleControlFrame(payload []byte) (bool, error) {
	controlType, value, err := parseControlPayload(payload)
	if err != nil {
		return false, err
	}
	switch controlType {
	case controlTypeAck:
		c.recovery.ackReceived(value)
		// The writer may hold a frame waiting for the room this frees.
		c.signalSend()
		return false, nil
	case controlTypePing:
		// Liveness is already enforced by SetReadDeadline and periodic ping.
		// Keep ping handling as a no-op for protocol compatibility.
		return false, nil
	case controlTypeClose:
		return true, nil
	default:
		return false, ErrInvalidMessage{Reason: "unknown control frame type"}
	}
}

// finishRecoveryWriter writes CLOSE after the frames already written, so the
// peer releases the connection instead of keeping it for a reconnect.
func (c *Connection) finishRecoveryWriter(bw *bufio.Writer, done chan struct{}) {
	if err := c.writeFrameTo(bw, controlStreamID, 0, 0, buildCloseControlPayload()); err != nil {
		c.debug.noteFailure(DebugFailureRecoveryControl, "recovery close write failed: "+err.Error())
	}
	close(done)
}

// handleRecoveryDataFrame delivers a data frame that follows lastRecvSeq, the
// last one received, and returns the new last received sequence number.
func (c *Connection) handleRecoveryDataFrame(lastRecvSeq uint64, streamID uint32, seq uint64, flags FrameFlags, payload []byte) (uint64, error) {
	if seq == 0 {
		return lastRecvSeq, ErrInvalidMessage{Reason: "recovery data frame missing seq"}
	}
	if seq <= lastRecvSeq {
		c.recovery.noteReceived(c, seq)
		return lastRecvSeq, nil
	}
	if seq != lastRecvSeq+1 {
		return lastRecvSeq, ErrInvalidMessage{Reason: "recovery frame sequence gap"}
	}
	if err := c.acceptFrame(streamID, flags, payload); err != nil {
		return lastRecvSeq, err
	}
	c.recovery.noteReceived(c, seq)
	return seq, nil
}

func (c *Connection) handleRecoveryTransportError(conn net.Conn) {
	c.detachTransport(conn)
}

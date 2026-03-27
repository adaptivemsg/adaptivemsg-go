package adaptivemsg

import (
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

	liveQueue   *frameDeque
	resumeQueue *frameDeque

	mu            sync.Mutex
	shared        negotiatedRecoveryOptions
	ackEvery      atomic.Uint32
	ackDelayNanos atomic.Int64
	heartbeatIntv atomic.Int64
	heartbeatTO   atomic.Int64
	lastRecvSeq   uint64
	lastAckSent   uint64
	ackPending    uint32
	ackDue        bool
	ackDueAt      time.Time
	expireTimer   *time.Timer
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
		liveQueue:           newFrameDeque(),
		resumeQueue:         newFrameDeque(),
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
		liveQueue:    newFrameDeque(),
		resumeQueue:  newFrameDeque(),
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
		if c.conn != nil && c.transportGen != lastGen {
			return c.conn, c.transportGen, c.transportResume, nil
		}
		c.transportCond.Wait()
	}
}

func (c *Connection) transportActive(gen uint64, conn net.Conn) bool {
	c.initTransportState()
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	return !c.closed.Load() && c.conn == conn && c.transportGen == gen
}

func (c *Connection) attachTransport(conn net.Conn, peerLastRecvSeq uint64) {
	c.initTransportState()
	c.transportMu.Lock()
	oldConn := c.conn
	c.conn = conn
	c.transportGen++
	c.transportResume = peerLastRecvSeq
	c.transportMu.Unlock()

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
	c.transportGen++
	c.transportResume = 0
	c.transportMu.Unlock()

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
	c.transportGen++
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

func (r *recoveryState) enqueueLive(frame *frameRecord) {
	if r == nil {
		return
	}
	r.liveQueue.push(frame)
}

func (r *recoveryState) prepareResume(lastSeq uint64) {
	if r == nil {
		return
	}
	frames := r.replay.snapshotFrom(lastSeq)
	r.resumeQueue.reset(frames)
}

func (r *recoveryState) takeResumeFrame() (*frameRecord, bool) {
	if r == nil {
		return nil, false
	}
	return r.resumeQueue.pop()
}

func (r *recoveryState) takeLiveFrame() (*frameRecord, bool) {
	if r == nil {
		return nil, false
	}
	return r.liveQueue.pop()
}

func (r *recoveryState) noteReceived(c *Connection, seq uint64) {
	if r == nil {
		return
	}

	r.mu.Lock()
	if seq <= r.lastRecvSeq {
		if r.lastAckSent < r.lastRecvSeq {
			r.ackDue = true
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
		r.ackDue = true
		r.ackDueAt = time.Time{}
		notify = true
	} else {
		ackDelay := time.Duration(r.ackDelayNanos.Load())
		if ackDelay <= 0 {
			r.ackDue = true
			r.ackDueAt = time.Time{}
			notify = true
		} else if r.ackDueAt.IsZero() {
			r.ackDueAt = time.Now().Add(ackDelay)
			notify = true
		}
	}
	r.mu.Unlock()
	if notify {
		c.signalSend()
	}
}

func (r *recoveryState) nextAckWait() time.Duration {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ackDue || r.ackDueAt.IsZero() {
		return 0
	}
	wait := time.Until(r.ackDueAt)
	if wait < 0 {
		return 0
	}
	return wait
}

func (r *recoveryState) takePendingControl() (byte, uint64, bool) {
	if r == nil {
		return pendingControlNone, 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ackDue && !r.ackDueAt.IsZero() && !time.Now().Before(r.ackDueAt) && r.lastAckSent < r.lastRecvSeq {
		r.ackDue = true
	}
	if r.ackDue && r.lastAckSent < r.lastRecvSeq {
		seq := r.lastRecvSeq
		r.lastAckSent = seq
		r.ackPending = 0
		r.ackDue = false
		r.ackDueAt = time.Time{}
		return pendingControlAck, seq, true
	}
	return pendingControlNone, 0, false
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

func (c *Connection) reconnectLoop() {
	if c == nil || c.recovery == nil || c.recovery.role != recoveryRoleClient {
		return
	}
	if !c.recovery.reconnectActive.CompareAndSwap(false, true) {
		return
	}
	defer c.recovery.reconnectActive.Store(false)

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
		conn, err := c.resumeClientTransport()
		if err == nil {
			_ = conn
			return
		}
		var rejected ErrResumeRejected
		var unsupported ErrUnsupportedFrameVersion
		if errors.As(err, &rejected) || errors.As(err, &unsupported) {
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

func (c *Connection) recoveryWriterLoop() {
	var lastGen uint64
	var waitTimer *time.Timer
	stopAndDrain := func() {
		if !waitTimer.Stop() {
			select {
			case <-waitTimer.C:
			default:
			}
		}
	}
	defer func() {
		if waitTimer != nil {
			stopAndDrain()
		}
	}()
	for {
		conn, gen, resumeSeq, err := c.waitForTransport(lastGen)
		if err != nil {
			return
		}
		lastGen = gen
		lastSentSeq := resumeSeq
		c.recovery.prepareResume(resumeSeq)

		for {
			if !c.transportActive(gen, conn) {
				break
			}
			if _, ackSeq, ok := c.recovery.takePendingControl(); ok {
				payload := buildAckControlPayload(ackSeq)
				if err := c.writeFrameTo(conn, controlStreamID, 0, payload); err != nil {
					c.handleRecoveryTransportError(conn)
					break
				}
				continue
			}
			frame, ok := c.recovery.takeResumeFrame()
			if ok {
				if frame.seq <= lastSentSeq || frame.seq <= c.recovery.replay.lastAckedSeq() {
					continue
				}
				if err := c.writeFrameTo(conn, frame.streamID, frame.seq, frame.payload); err != nil {
					c.handleRecoveryTransportError(conn)
					break
				}
				lastSentSeq = frame.seq
				continue
			}
			frame, ok = c.recovery.takeLiveFrame()
			if ok {
				if frame.seq <= lastSentSeq {
					continue
				}
				if err := c.writeFrameTo(conn, frame.streamID, frame.seq, frame.payload); err != nil {
					c.handleRecoveryTransportError(conn)
					break
				}
				lastSentSeq = frame.seq
				continue
			}
			ackWait := c.recovery.nextAckWait()
			heartbeatWait := time.Duration(c.recovery.heartbeatIntv.Load())
			wait := ackWait
			if heartbeatWait > 0 && (ackWait <= 0 || heartbeatWait < ackWait) {
				wait = heartbeatWait
			}
			if wait <= 0 {
				select {
				case <-c.closeCh:
					return
				case <-c.sendNotify:
				}
				continue
			}
			if waitTimer == nil {
				waitTimer = time.NewTimer(wait)
			} else {
				stopAndDrain()
				waitTimer.Reset(wait)
			}
			timerFired := false
			select {
			case <-c.closeCh:
				stopAndDrain()
				return
			case <-c.sendNotify:
				stopAndDrain()
			case <-waitTimer.C:
				timerFired = true
			}
			if timerFired && wait == heartbeatWait {
				if err := c.writeFrameTo(conn, controlStreamID, 0, buildPingControlPayload()); err != nil {
					c.handleRecoveryTransportError(conn)
					break
				}
			}
		}
	}
}

func (c *Connection) recoveryReaderLoop() {
	var lastGen uint64
	for {
		conn, gen, _, err := c.waitForTransport(lastGen)
		if err != nil {
			return
		}
		lastGen = gen

		for {
			if !c.transportActive(gen, conn) {
				break
			}
			c.recovery.armReadDeadline(conn)
			streamID, seq, payload, err := c.readFrameFrom(conn)
			if err != nil {
				c.handleRecoveryTransportError(conn)
				break
			}
			c.recovery.armReadDeadline(conn)
			if streamID == controlStreamID {
				if err := c.handleControlFrame(payload); err != nil {
					c.markClosed()
					return
				}
				continue
			}
			if err := c.handleRecoveryDataFrame(streamID, seq, payload); err != nil {
				c.markClosed()
				return
			}
		}
	}
}

func (c *Connection) handleControlFrame(payload []byte) error {
	controlType, value, err := parseControlPayload(payload)
	if err != nil {
		return err
	}
	switch controlType {
	case controlTypeAck:
		c.recovery.ackReceived(value)
		return nil
	case controlTypePing:
		// Liveness is already enforced by SetReadDeadline and periodic ping.
		// Keep ping handling as a no-op for protocol compatibility.
		return nil
	default:
		return ErrInvalidMessage{Reason: "unknown control frame type"}
	}
}

func (c *Connection) handleRecoveryDataFrame(streamID uint32, seq uint64, payload []byte) error {
	if seq == 0 {
		return ErrInvalidMessage{Reason: "recovery data frame missing seq"}
	}
	lastRecvSeq := c.recovery.lastReceived()
	if seq <= lastRecvSeq {
		c.recovery.noteReceived(c, seq)
		return nil
	}
	if seq != lastRecvSeq+1 {
		return ErrInvalidMessage{Reason: "recovery frame sequence gap"}
	}

	streamCtx := c.getStreamCtx(streamID)
	if err := streamCtx.stream.core.incomingQ(payload); err != nil {
		return err
	}
	c.recovery.noteReceived(c, seq)
	return nil
}

func (c *Connection) handleRecoveryTransportError(conn net.Conn) {
	c.detachTransport(conn)
}

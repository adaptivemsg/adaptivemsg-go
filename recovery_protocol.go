package adaptivemsg

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"time"
)

const (
	controlStreamID = ^uint32(0)

	controlTypeAck  = 1
	controlTypePing = 2

	attachModeNew    = 1
	attachModeResume = 2

	attachStatusOK       = 1
	attachStatusRejected = 2

	recoveryTokenLen    = 16
	attachRequestLen    = 44
	attachResponseLen   = 60
	controlAckFrameLen  = 9
	controlHeartbeatLen = 1
)

type recoveryToken [recoveryTokenLen]byte

type attachRequest struct {
	mode         byte
	connectionID recoveryToken
	resumeSecret recoveryToken
	lastRecvSeq  uint64
}

type attachResponse struct {
	status       byte
	connectionID recoveryToken
	resumeSecret recoveryToken
	lastRecvSeq  uint64
	negotiated   negotiatedRecoveryOptions
}

func newRecoveryToken() (recoveryToken, error) {
	var token recoveryToken
	_, err := rand.Read(token[:])
	return token, err
}

func writeAttachRequest(conn net.Conn, req attachRequest) error {
	buf := make([]byte, attachRequestLen)
	buf[0] = req.mode
	copy(buf[4:20], req.connectionID[:])
	copy(buf[20:36], req.resumeSecret[:])
	binary.BigEndian.PutUint64(buf[36:44], req.lastRecvSeq)
	return writeFull(conn, buf)
}

func readAttachRequest(conn net.Conn) (attachRequest, error) {
	buf := make([]byte, attachRequestLen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return attachRequest{}, err
	}
	var req attachRequest
	req.mode = buf[0]
	copy(req.connectionID[:], buf[4:20])
	copy(req.resumeSecret[:], buf[20:36])
	req.lastRecvSeq = binary.BigEndian.Uint64(buf[36:44])
	return req, nil
}

func writeAttachResponse(conn net.Conn, resp attachResponse) error {
	negotiated, err := encodeNegotiatedRecoveryOptions(resp.negotiated)
	if err != nil {
		return err
	}
	buf := make([]byte, attachResponseLen)
	buf[0] = resp.status
	copy(buf[4:20], resp.connectionID[:])
	copy(buf[20:36], resp.resumeSecret[:])
	binary.BigEndian.PutUint64(buf[36:44], resp.lastRecvSeq)
	binary.BigEndian.PutUint32(buf[44:48], negotiated.ackEvery)
	binary.BigEndian.PutUint32(buf[48:52], negotiated.ackDelayMS)
	binary.BigEndian.PutUint32(buf[52:56], negotiated.heartbeatIntervalMS)
	binary.BigEndian.PutUint32(buf[56:60], negotiated.heartbeatTimeoutMS)
	return writeFull(conn, buf)
}

func readAttachResponse(conn net.Conn) (attachResponse, error) {
	buf := make([]byte, attachResponseLen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return attachResponse{}, err
	}
	var resp attachResponse
	resp.status = buf[0]
	copy(resp.connectionID[:], buf[4:20])
	copy(resp.resumeSecret[:], buf[20:36])
	resp.lastRecvSeq = binary.BigEndian.Uint64(buf[36:44])
	negotiated, err := decodeNegotiatedRecoveryOptions(
		binary.BigEndian.Uint32(buf[44:48]),
		binary.BigEndian.Uint32(buf[48:52]),
		binary.BigEndian.Uint32(buf[52:56]),
		binary.BigEndian.Uint32(buf[56:60]),
	)
	if err != nil {
		return attachResponse{}, err
	}
	resp.negotiated = negotiated
	return resp, nil
}

type encodedNegotiatedRecoveryOptions struct {
	ackEvery            uint32
	ackDelayMS          uint32
	heartbeatIntervalMS uint32
	heartbeatTimeoutMS  uint32
}

func encodeNegotiatedRecoveryOptions(opts negotiatedRecoveryOptions) (encodedNegotiatedRecoveryOptions, error) {
	opts = opts.normalized()
	ackDelayMS, err := durationToMillis(opts.AckDelay)
	if err != nil {
		return encodedNegotiatedRecoveryOptions{}, err
	}
	heartbeatIntervalMS, err := durationToMillis(opts.HeartbeatInterval)
	if err != nil {
		return encodedNegotiatedRecoveryOptions{}, err
	}
	heartbeatTimeoutMS, err := durationToMillis(opts.HeartbeatTimeout)
	if err != nil {
		return encodedNegotiatedRecoveryOptions{}, err
	}
	return encodedNegotiatedRecoveryOptions{
		ackEvery:            opts.AckEvery,
		ackDelayMS:          ackDelayMS,
		heartbeatIntervalMS: heartbeatIntervalMS,
		heartbeatTimeoutMS:  heartbeatTimeoutMS,
	}, nil
}

func decodeNegotiatedRecoveryOptions(ackEvery, ackDelayMS, heartbeatIntervalMS, heartbeatTimeoutMS uint32) (negotiatedRecoveryOptions, error) {
	opts := negotiatedRecoveryOptions{
		AckEvery:          ackEvery,
		AckDelay:          time.Duration(ackDelayMS) * time.Millisecond,
		HeartbeatInterval: time.Duration(heartbeatIntervalMS) * time.Millisecond,
		HeartbeatTimeout:  time.Duration(heartbeatTimeoutMS) * time.Millisecond,
	}
	if opts.AckEvery == 0 {
		return negotiatedRecoveryOptions{}, ErrInvalidMessage{Reason: "negotiated ack_every must be positive"}
	}
	if opts.AckDelay <= 0 {
		return negotiatedRecoveryOptions{}, ErrInvalidMessage{Reason: "negotiated ack_delay must be positive"}
	}
	if opts.HeartbeatInterval <= 0 {
		return negotiatedRecoveryOptions{}, ErrInvalidMessage{Reason: "negotiated heartbeat_interval must be positive"}
	}
	if opts.HeartbeatTimeout < 2*opts.HeartbeatInterval {
		return negotiatedRecoveryOptions{}, ErrInvalidMessage{Reason: "negotiated heartbeat_timeout too small"}
	}
	return opts, nil
}

func durationToMillis(d time.Duration) (uint32, error) {
	if d <= 0 {
		return 0, ErrInvalidMessage{Reason: "recovery duration must be positive"}
	}
	ms := d / time.Millisecond
	if ms <= 0 {
		ms = 1
	}
	if ms > time.Duration(^uint32(0)) {
		return 0, ErrInvalidMessage{Reason: "recovery duration too large"}
	}
	return uint32(ms), nil
}

func buildAckControlPayload(lastRecvSeq uint64) []byte {
	buf := make([]byte, controlAckFrameLen)
	buf[0] = controlTypeAck
	binary.BigEndian.PutUint64(buf[1:9], lastRecvSeq)
	return buf
}

func buildPingControlPayload() []byte {
	return []byte{controlTypePing}
}

func parseControlPayload(payload []byte) (byte, uint64, error) {
	if len(payload) == 0 {
		return 0, 0, ErrInvalidMessage{Reason: "empty control payload"}
	}
	switch payload[0] {
	case controlTypeAck:
		if len(payload) != controlAckFrameLen {
			return 0, 0, ErrInvalidMessage{Reason: "invalid ack control payload length"}
		}
		return payload[0], binary.BigEndian.Uint64(payload[1:9]), nil
	case controlTypePing:
		if len(payload) != controlHeartbeatLen {
			return 0, 0, ErrInvalidMessage{Reason: "invalid heartbeat control payload length"}
		}
		return payload[0], 0, nil
	default:
		return 0, 0, ErrInvalidMessage{Reason: "unknown control frame type"}
	}
}

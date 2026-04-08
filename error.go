package adaptivemsg

import "fmt"

// ErrCodec reports a codec encode or decode failure.
// It is returned when a message cannot be serialized or deserialized
// by the negotiated codec.
type ErrCodec struct {
	// Message contains the human-readable error detail.
	Message string
}

func (e ErrCodec) Error() string {
	return "codec error: " + e.Message
}

// ErrFrameTooLarge indicates that a payload exceeded the negotiated maximum
// frame size for the connection. The receiver will reject the frame.
type ErrFrameTooLarge struct {
	// Size is the offending payload size in bytes.
	Size int
}

func (e ErrFrameTooLarge) Error() string {
	return fmt.Sprintf("frame too large: %d", e.Size)
}

// ErrUnsupportedFrameVersion indicates that a peer sent a frame with an
// unknown protocol version byte. This typically means the remote end is
// running an incompatible version of the adaptivemsg protocol.
type ErrUnsupportedFrameVersion struct {
	// Version is the unrecognized protocol version byte.
	Version byte
}

func (e ErrUnsupportedFrameVersion) Error() string {
	return fmt.Sprintf("unsupported frame version: %d", e.Version)
}

// ErrUnsupportedCodec indicates that a peer selected a codec that is not
// registered in the local codec registry.
type ErrUnsupportedCodec struct {
	// Value is the unrecognized codec ID byte.
	Value byte
}

func (e ErrUnsupportedCodec) Error() string {
	return fmt.Sprintf("unsupported codec: %d", e.Value)
}

// ErrNoCommonCodec indicates that the handshake failed because the client
// and server have no codec in common. Register the same codec on both sides
// before connecting.
type ErrNoCommonCodec struct{}

func (e ErrNoCommonCodec) Error() string {
	return "no common codec"
}

// ErrTooManyCodecs indicates that the client offered more codecs than the
// protocol allows during the handshake.
type ErrTooManyCodecs struct {
	// Count is the number of codecs offered by the client.
	Count int
}

func (e ErrTooManyCodecs) Error() string {
	return fmt.Sprintf("too many codecs: %d", e.Count)
}

// ErrBadHandshakeMagic indicates that the connection does not speak the
// adaptivemsg protocol. The initial bytes did not match the expected
// handshake magic value.
type ErrBadHandshakeMagic struct{}

func (e ErrBadHandshakeMagic) Error() string {
	return "invalid handshake magic"
}

// ErrUnknownMessage indicates that a received message has a wire name that
// is not registered in the connection's message registry.
type ErrUnknownMessage struct {
	// Name is the unrecognized wire name.
	Name string
}

func (e ErrUnknownMessage) Error() string {
	return fmt.Sprintf("unknown message type: %s", e.Name)
}

// ErrCompactFieldCount indicates that a compact-mode struct has the wrong
// number of fields. This happens when the sender and receiver disagree on
// the struct definition.
type ErrCompactFieldCount struct {
	// Expected is the number of fields the local struct definition has.
	Expected int
	// Got is the number of fields found in the incoming payload.
	Got int
}

func (e ErrCompactFieldCount) Error() string {
	return fmt.Sprintf("compact field count mismatch: expected %d, got %d", e.Expected, e.Got)
}

// ErrConnectTimeout indicates that the TCP connect or handshake exceeded
// the timeout duration configured via Client.WithTimeout().
type ErrConnectTimeout struct{}

func (e ErrConnectTimeout) Error() string {
	return "connect timeout"
}

// ErrReplayBufferFull indicates that the recovery replay buffer has exceeded
// the configured MaxReplayBytes limit. The connection will be terminated
// because unacknowledged frames can no longer be retained for replay.
type ErrReplayBufferFull struct {
	// Limit is the configured maximum replay buffer size in bytes.
	Limit int64
	// Size is the current replay buffer size in bytes.
	Size int64
}

func (e ErrReplayBufferFull) Error() string {
	return fmt.Sprintf("replay buffer full: size=%d limit=%d", e.Size, e.Limit)
}

// ErrResumeRejected indicates that the server rejected a resume attempt.
// Common causes include a wrong connection secret, an expired detached
// session, or a codec mismatch.
type ErrResumeRejected struct {
	// Reason contains the human-readable rejection detail.
	Reason string
}

func (e ErrResumeRejected) Error() string {
	if e.Reason == "" {
		return "resume rejected"
	}
	return "resume rejected: " + e.Reason
}

// ErrRecvTimeout indicates that no message was received within the stream's
// recv timeout, as configured via SetRecvTimeout.
type ErrRecvTimeout struct{}

func (e ErrRecvTimeout) Error() string {
	return "recv timeout"
}

// ErrTypeMismatch indicates that the decoded message type does not match
// the expected type parameter. This occurs when a typed Recv or SendRecv
// receives a message with a different wire name than expected.
type ErrTypeMismatch struct {
	// Expected is the wire name of the expected message type.
	Expected string
	// Got is the wire name of the actually received message.
	Got string
}

func (e ErrTypeMismatch) Error() string {
	return fmt.Sprintf("message type mismatch: expected %s, got %s", e.Expected, e.Got)
}

// ErrClosed indicates that an operation was attempted on a closed connection
// or stream.
type ErrClosed struct{}

func (e ErrClosed) Error() string {
	return "connection closed"
}

// ErrRemote wraps an ErrorReply received from the peer. The Code and Message
// fields correspond to the remote ErrorReply's fields.
type ErrRemote struct {
	// Code is the error code from the remote ErrorReply.
	Code string
	// Message is the error message from the remote ErrorReply.
	Message string
}

func (e ErrRemote) Error() string {
	return fmt.Sprintf("remote error: %s: %s", e.Code, e.Message)
}

// ErrHandlerTaskBusy indicates that NewTask() was called on a stream that
// already has a running task. Only one handler task is allowed per stream
// at a time.
type ErrHandlerTaskBusy struct{}

func (e ErrHandlerTaskBusy) Error() string {
	return "only one handler task allowed per stream"
}

// ErrConcurrentRecv indicates that concurrent Recv or SendRecv calls were
// detected on the same stream. Only one receive operation is allowed at a
// time on a given stream.
type ErrConcurrentRecv struct{}

func (e ErrConcurrentRecv) Error() string {
	return "concurrent recv on stream"
}

// ErrInvalidMessage indicates invalid message usage, such as passing a nil
// value, a non-struct type, or a type that does not satisfy the Message
// interface.
type ErrInvalidMessage struct {
	// Reason contains the human-readable detail of what is invalid.
	Reason string
}

func (e ErrInvalidMessage) Error() string {
	return e.Reason
}

// ErrHandshakeRejected indicates that the server explicitly rejected the
// handshake. This is distinct from a version or codec mismatch.
type ErrHandshakeRejected struct{}

func (e ErrHandshakeRejected) Error() string {
	return "handshake rejected"
}

// ErrNoCommonVersion indicates that no protocol version overlap exists between
// the client and server. The fields show each side's supported version range.
type ErrNoCommonVersion struct {
	// ClientMin is the minimum protocol version supported by the client.
	ClientMin byte
	// ClientMax is the maximum protocol version supported by the client.
	ClientMax byte
	// ServerMin is the minimum protocol version supported by the server.
	ServerMin byte
	// ServerMax is the maximum protocol version supported by the server.
	ServerMax byte
}

func (e ErrNoCommonVersion) Error() string {
	return fmt.Sprintf("no common protocol version: client %d-%d, server %d-%d",
		e.ClientMin, e.ClientMax, e.ServerMin, e.ServerMax)
}

// ErrUnsupportedTransport indicates that a required transport feature is
// unavailable in the current configuration.
type ErrUnsupportedTransport struct {
	// Reason contains the human-readable detail of the unsupported feature.
	Reason string
}

func (e ErrUnsupportedTransport) Error() string {
	return e.Reason
}

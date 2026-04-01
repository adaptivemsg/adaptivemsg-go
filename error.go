package adaptivemsg

import "fmt"

// ErrCodec reports codec encode/decode failures.
type ErrCodec struct {
	Message string
}

func (e ErrCodec) Error() string {
	return "codec error: " + e.Message
}

// ErrFrameTooLarge indicates a payload exceeded the negotiated frame size.
type ErrFrameTooLarge struct {
	Size int
}

func (e ErrFrameTooLarge) Error() string {
	return fmt.Sprintf("frame too large: %d", e.Size)
}

// ErrUnsupportedFrameVersion indicates a peer version is unsupported.
type ErrUnsupportedFrameVersion struct {
	Version byte
}

func (e ErrUnsupportedFrameVersion) Error() string {
	return fmt.Sprintf("unsupported frame version: %d", e.Version)
}

// ErrUnsupportedCodec indicates a peer codec is unsupported.
type ErrUnsupportedCodec struct {
	Value byte
}

func (e ErrUnsupportedCodec) Error() string {
	return fmt.Sprintf("unsupported codec: %d", e.Value)
}

// ErrNoCommonCodec indicates no codec overlap exists.
type ErrNoCommonCodec struct{}

func (e ErrNoCommonCodec) Error() string {
	return "no common codec"
}

// ErrTooManyCodecs indicates the peer sent too many codecs.
type ErrTooManyCodecs struct {
	Count int
}

func (e ErrTooManyCodecs) Error() string {
	return fmt.Sprintf("too many codecs: %d", e.Count)
}

// ErrBadHandshakeMagic indicates the handshake magic is invalid.
type ErrBadHandshakeMagic struct{}

func (e ErrBadHandshakeMagic) Error() string {
	return "invalid handshake magic"
}

// ErrUnknownMessage indicates a wire name is not registered.
type ErrUnknownMessage struct {
	Name string
}

func (e ErrUnknownMessage) Error() string {
	return fmt.Sprintf("unknown message type: %s", e.Name)
}

// ErrCompactFieldCount indicates compact field counts do not match.
type ErrCompactFieldCount struct {
	Expected int
	Got      int
}

func (e ErrCompactFieldCount) Error() string {
	return fmt.Sprintf("compact field count mismatch: expected %d, got %d", e.Expected, e.Got)
}

// ErrConnectTimeout indicates a connect attempt timed out.
type ErrConnectTimeout struct{}

func (e ErrConnectTimeout) Error() string {
	return "connect timeout"
}

// ErrReplayBufferFull indicates recovery replay buffering exceeded the configured limit.
type ErrReplayBufferFull struct {
	Limit int64
	Size  int64
}

func (e ErrReplayBufferFull) Error() string {
	return fmt.Sprintf("replay buffer full: size=%d limit=%d", e.Size, e.Limit)
}

// ErrResumeRejected indicates the peer could not reattach a resumable connection.
type ErrResumeRejected struct {
	Reason string
}

func (e ErrResumeRejected) Error() string {
	if e.Reason == "" {
		return "resume rejected"
	}
	return "resume rejected: " + e.Reason
}

// ErrRecvTimeout indicates a receive operation timed out.
type ErrRecvTimeout struct{}

func (e ErrRecvTimeout) Error() string {
	return "recv timeout"
}

// ErrTypeMismatch indicates a wire/name mismatch during decode.
type ErrTypeMismatch struct {
	Expected string
	Got      string
}

func (e ErrTypeMismatch) Error() string {
	return fmt.Sprintf("message type mismatch: expected %s, got %s", e.Expected, e.Got)
}

// ErrClosed indicates the connection or stream is closed.
type ErrClosed struct{}

func (e ErrClosed) Error() string {
	return "connection closed"
}

// ErrRemote wraps an ErrorReply from the peer.
type ErrRemote struct {
	Code    string
	Message string
}

func (e ErrRemote) Error() string {
	return fmt.Sprintf("remote error: %s: %s", e.Code, e.Message)
}

// ErrHandlerTaskBusy indicates a handler task is already running.
type ErrHandlerTaskBusy struct{}

func (e ErrHandlerTaskBusy) Error() string {
	return "only one handler task allowed per stream"
}

// ErrConcurrentRecv indicates concurrent Recv on the same stream.
type ErrConcurrentRecv struct{}

func (e ErrConcurrentRecv) Error() string {
	return "concurrent recv on stream"
}

// ErrInvalidMessage indicates invalid message usage or shape.
type ErrInvalidMessage struct {
	Reason string
}

func (e ErrInvalidMessage) Error() string {
	return e.Reason
}

// ErrHandshakeRejected indicates the peer explicitly rejected the handshake.
type ErrHandshakeRejected struct{}

func (e ErrHandshakeRejected) Error() string {
	return "handshake rejected"
}

// ErrNoCommonVersion indicates no protocol version overlap between client and server.
type ErrNoCommonVersion struct {
	ClientMin byte
	ClientMax byte
	ServerMin byte
	ServerMax byte
}

func (e ErrNoCommonVersion) Error() string {
	return fmt.Sprintf("no common protocol version: client %d-%d, server %d-%d",
		e.ClientMin, e.ClientMax, e.ServerMin, e.ServerMax)
}

// ErrUnsupportedTransport indicates a transport feature is unavailable.
type ErrUnsupportedTransport struct {
	Reason string
}

func (e ErrUnsupportedTransport) Error() string {
	return e.Reason
}

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

// ErrHandshakeRejected indicates the peer rejected the handshake.
type ErrHandshakeRejected struct{}

func (e ErrHandshakeRejected) Error() string {
	return "handshake rejected"
}

// ErrNoCommonVersion indicates no shared protocol version exists.
type ErrNoCommonVersion struct {
	ClientMin byte
	ClientMax byte
	ServerMin byte
	ServerMax byte
}

func (e ErrNoCommonVersion) Error() string {
	return fmt.Sprintf(
		"no common protocol version: client %d-%d, server %d-%d",
		e.ClientMin,
		e.ClientMax,
		e.ServerMin,
		e.ServerMax,
	)
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

// ErrUnsupportedTransport indicates a transport feature is unavailable.
type ErrUnsupportedTransport struct {
	Reason string
}

func (e ErrUnsupportedTransport) Error() string {
	return e.Reason
}

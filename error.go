package adaptivemsg

import "fmt"

type ErrCodec struct {
	Message string
}

func (e ErrCodec) Error() string {
	return "codec error: " + e.Message
}

type ErrFrameTooLarge struct {
	Size int
}

func (e ErrFrameTooLarge) Error() string {
	return fmt.Sprintf("frame too large: %d", e.Size)
}

type ErrUnsupportedFrameVersion struct {
	Version byte
}

func (e ErrUnsupportedFrameVersion) Error() string {
	return fmt.Sprintf("unsupported frame version: %d", e.Version)
}

type ErrUnsupportedCodec struct {
	Value byte
}

func (e ErrUnsupportedCodec) Error() string {
	return fmt.Sprintf("unsupported codec: %d", e.Value)
}

type ErrHandshakeRejected struct{}

func (e ErrHandshakeRejected) Error() string {
	return "handshake rejected"
}

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

type ErrBadHandshakeMagic struct{}

func (e ErrBadHandshakeMagic) Error() string {
	return "invalid handshake magic"
}

type ErrUnknownMessage struct {
	Name string
}

func (e ErrUnknownMessage) Error() string {
	return fmt.Sprintf("unknown message type: %s", e.Name)
}

type ErrCompactFieldCount struct {
	Expected int
	Got      int
}

func (e ErrCompactFieldCount) Error() string {
	return fmt.Sprintf("compact field count mismatch: expected %d, got %d", e.Expected, e.Got)
}

type ErrConnectTimeout struct{}

func (e ErrConnectTimeout) Error() string {
	return "connect timeout"
}

type ErrRecvTimeout struct{}

func (e ErrRecvTimeout) Error() string {
	return "recv timeout"
}

type ErrTypeMismatch struct {
	Expected string
	Got      string
}

func (e ErrTypeMismatch) Error() string {
	return fmt.Sprintf("message type mismatch: expected %s, got %s", e.Expected, e.Got)
}

type ErrClosed struct{}

func (e ErrClosed) Error() string {
	return "connection closed"
}

type ErrRemote struct {
	Code    string
	Message string
}

func (e ErrRemote) Error() string {
	return fmt.Sprintf("remote error: %s: %s", e.Code, e.Message)
}

type ErrHandlerTaskBusy struct{}

func (e ErrHandlerTaskBusy) Error() string {
	return "only one handler task allowed per stream"
}

type ErrConcurrentRecv struct{}

func (e ErrConcurrentRecv) Error() string {
	return "concurrent recv on stream"
}

type ErrInvalidMessage struct {
	Reason string
}

func (e ErrInvalidMessage) Error() string {
	return e.Reason
}

type ErrUnsupported struct {
	Reason string
}

func (e ErrUnsupported) Error() string {
	return e.Reason
}

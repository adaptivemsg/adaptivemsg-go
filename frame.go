package adaptivemsg

import (
	"bufio"
	"encoding/binary"
	"io"
)

const frameHeaderLen = 10
const frameHeaderLenV3 = 18

// FrameFlags is the frame header flags byte. Each bit is an independent
// property and can be combined.
//
//	bit 0  FIN   last frame for a non-zero stream
//	bit 1  META  a length-prefixed metadata block follows the fixed header
//	bit 2–6     unassigned
//	bit 7  EXT   another flags byte follows the fixed header
type FrameFlags byte

const (
	// FrameFlagFIN marks the last frame for a non-zero stream. Only the side
	// that opened the stream sends it; the peer releases the stream after
	// processing the frame. Stream 0 ignores this flag.
	FrameFlagFIN FrameFlags = 1 << 0
	// FrameFlagMeta reserves the metadata block. It is not implemented yet.
	FrameFlagMeta FrameFlags = 1 << 1
	// FrameFlagExt reserves an additional flags byte. It is not implemented yet.
	FrameFlagExt FrameFlags = 1 << 7
)

// supportedFrameFlags are the bits this runtime implements. Every other bit,
// including the reserved META and EXT, must be zero until it is implemented
// and negotiated in the handshake.
const supportedFrameFlags = FrameFlagFIN

func writeFull(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if n > 0 {
			buf = buf[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func frameHeaderLenForVersion(version byte) (int, error) {
	switch version {
	case protocolVersionV2:
		return frameHeaderLen, nil
	case protocolVersionV3:
		return frameHeaderLenV3, nil
	default:
		return 0, ErrUnsupportedFrameVersion{Version: version}
	}
}

func (c *Connection) buildHeader(streamID uint32, seq uint64, flags FrameFlags, payloadLen int) ([frameHeaderLenV3]byte, int, error) {
	if payloadLen < 0 || uint64(payloadLen) > uint64(^uint32(0)) {
		return [frameHeaderLenV3]byte{}, 0, ErrFrameTooLarge{Size: payloadLen}
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return [frameHeaderLenV3]byte{}, 0, ErrFrameTooLarge{Size: payloadLen}
	}
	headerLen, err := frameHeaderLenForVersion(c.config.version)
	if err != nil {
		return [frameHeaderLenV3]byte{}, 0, err
	}
	var header [frameHeaderLenV3]byte
	header[0] = c.config.version
	header[1] = byte(flags)
	binary.BigEndian.PutUint32(header[2:6], streamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(payloadLen))
	if c.config.version == protocolVersionV3 {
		binary.BigEndian.PutUint64(header[10:18], seq)
	}
	return header, headerLen, nil
}

func (c *Connection) parseHeader(header []byte) (uint32, uint64, FrameFlags, int, error) {
	version := header[0]
	if version != c.config.version {
		return 0, 0, 0, 0, ErrUnsupportedFrameVersion{Version: version}
	}
	flags := FrameFlags(header[1])
	if flags&^supportedFrameFlags != 0 {
		return 0, 0, 0, 0, ErrInvalidMessage{Reason: "unsupported frame flags"}
	}
	streamID := binary.BigEndian.Uint32(header[2:6])
	payloadLen := binary.BigEndian.Uint32(header[6:10])
	var seq uint64
	if version == protocolVersionV3 {
		seq = binary.BigEndian.Uint64(header[10:18])
	}
	return streamID, seq, flags, int(payloadLen), nil
}

func (c *Connection) writeFrame(streamID uint32, seq uint64, flags FrameFlags, payload []byte) error {
	return c.writeFrameTo(c.writer, streamID, seq, flags, payload)
}

func (c *Connection) writeFrameTo(conn io.Writer, streamID uint32, seq uint64, flags FrameFlags, payload []byte) error {
	header, headerLen, err := c.buildHeader(streamID, seq, flags, len(payload))
	if err != nil {
		return err
	}
	if err := writeFull(conn, header[:headerLen]); err != nil {
		return err
	}
	if err := writeFull(conn, payload); err != nil {
		return err
	}
	c.debug.framesWritten.Add(1)
	c.debug.bytesWritten.Add(uint64(headerLen + len(payload)))
	if streamID == controlStreamID {
		c.debug.controlFramesWritten.Add(1)
	}
	// Flush if the writer supports it (buffered writer).
	if bw, ok := conn.(*bufio.Writer); ok {
		return bw.Flush()
	}
	return nil
}

// writeFrameNoFlush writes a frame without flushing, for batching multiple writes.
func (c *Connection) writeFrameNoFlush(conn io.Writer, streamID uint32, seq uint64, flags FrameFlags, payload []byte) error {
	header, headerLen, err := c.buildHeader(streamID, seq, flags, len(payload))
	if err != nil {
		return err
	}
	if err := writeFull(conn, header[:headerLen]); err != nil {
		return err
	}
	if err := writeFull(conn, payload); err != nil {
		return err
	}
	c.debug.framesWritten.Add(1)
	c.debug.bytesWritten.Add(uint64(headerLen + len(payload)))
	if streamID == controlStreamID {
		c.debug.controlFramesWritten.Add(1)
	}
	return nil
}

// isMalformedFrameError reports whether an error from readFrameFrom came from
// the frame contents rather than from the transport.
func isMalformedFrameError(err error) bool {
	switch err.(type) {
	case ErrInvalidMessage, ErrUnsupportedFrameVersion, ErrFrameTooLarge:
		return true
	}
	return false
}

func (c *Connection) readFrame() (uint32, uint64, FrameFlags, []byte, error) {
	return c.readFrameFrom(c.conn)
}

func (c *Connection) readFrameFrom(conn io.Reader) (uint32, uint64, FrameFlags, []byte, error) {
	headerLen, err := frameHeaderLenForVersion(c.config.version)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	var header [frameHeaderLenV3]byte
	if _, err := io.ReadFull(conn, header[:headerLen]); err != nil {
		return 0, 0, 0, nil, err
	}
	streamID, seq, flags, payloadLen, err := c.parseHeader(header[:headerLen])
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return 0, 0, 0, nil, ErrFrameTooLarge{Size: payloadLen}
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, 0, 0, nil, err
	}
	c.debug.framesRead.Add(1)
	c.debug.bytesRead.Add(uint64(headerLen + len(payload)))
	if streamID == controlStreamID {
		c.debug.controlFramesRead.Add(1)
	}
	return streamID, seq, flags, payload, nil
}

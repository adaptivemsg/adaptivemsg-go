package adaptivemsg

import (
	"encoding/binary"
	"io"
)

const frameHeaderLen = 10
const frameHeaderLenV3 = 18

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

func (c *Connection) buildHeader(streamID uint32, seq uint64, payloadLen int) ([]byte, error) {
	if payloadLen > int(^uint32(0)) {
		return nil, ErrFrameTooLarge{Size: payloadLen}
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return nil, ErrFrameTooLarge{Size: payloadLen}
	}
	headerLen, err := frameHeaderLenForVersion(c.config.version)
	if err != nil {
		return nil, err
	}
	header := make([]byte, headerLen)
	header[0] = c.config.version
	header[1] = 0
	binary.BigEndian.PutUint32(header[2:6], streamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(payloadLen))
	if c.config.version == protocolVersionV3 {
		binary.BigEndian.PutUint64(header[10:18], seq)
	}
	return header, nil
}

func (c *Connection) parseHeader(header []byte) (uint32, uint64, int, error) {
	version := header[0]
	if version != c.config.version {
		return 0, 0, 0, ErrUnsupportedFrameVersion{Version: version}
	}
	expectedLen, err := frameHeaderLenForVersion(version)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(header) != expectedLen {
		return 0, 0, 0, ErrInvalidMessage{Reason: "invalid frame header length"}
	}
	streamID := binary.BigEndian.Uint32(header[2:6])
	payloadLen := binary.BigEndian.Uint32(header[6:10])
	var seq uint64
	if version == protocolVersionV3 {
		seq = binary.BigEndian.Uint64(header[10:18])
	}
	return streamID, seq, int(payloadLen), nil
}

func (c *Connection) writeFrame(streamID uint32, seq uint64, payload []byte) error {
	return c.writeFrameTo(c.conn, streamID, seq, payload)
}

func (c *Connection) writeFrameTo(conn io.Writer, streamID uint32, seq uint64, payload []byte) error {
	header, err := c.buildHeader(streamID, seq, len(payload))
	if err != nil {
		return err
	}
	if err := writeFull(conn, header); err != nil {
		return err
	}
	if err := writeFull(conn, payload); err != nil {
		return err
	}
	return nil
}

func (c *Connection) readFrame() (uint32, uint64, []byte, error) {
	return c.readFrameFrom(c.conn)
}

func (c *Connection) readFrameFrom(conn io.Reader) (uint32, uint64, []byte, error) {
	headerLen, err := frameHeaderLenForVersion(c.config.version)
	if err != nil {
		return 0, 0, nil, err
	}
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(conn, header); err != nil {
		return 0, 0, nil, err
	}
	streamID, seq, payloadLen, err := c.parseHeader(header)
	if err != nil {
		return 0, 0, nil, err
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return 0, 0, nil, ErrFrameTooLarge{Size: payloadLen}
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, 0, nil, err
	}
	return streamID, seq, payload, nil
}

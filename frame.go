package adaptivemsg

import (
	"encoding/binary"
	"io"
)

const frameHeaderLen = 10

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

func (c *Connection) buildHeader(streamID uint32, payloadLen int) ([frameHeaderLen]byte, error) {
	if payloadLen > int(^uint32(0)) {
		return [frameHeaderLen]byte{}, ErrFrameTooLarge{Size: payloadLen}
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return [frameHeaderLen]byte{}, ErrFrameTooLarge{Size: payloadLen}
	}
	var header [frameHeaderLen]byte
	header[0] = c.config.version
	header[1] = 0
	binary.BigEndian.PutUint32(header[2:6], streamID)
	binary.BigEndian.PutUint32(header[6:10], uint32(payloadLen))
	return header, nil
}

func (c *Connection) parseHeader(header [frameHeaderLen]byte) (uint32, int, error) {
	version := header[0]
	if version != c.config.version {
		return 0, 0, ErrUnsupportedFrameVersion{Version: version}
	}
	streamID := binary.BigEndian.Uint32(header[2:6])
	payloadLen := binary.BigEndian.Uint32(header[6:10])
	return streamID, int(payloadLen), nil
}

func (c *Connection) writeFrame(streamID uint32, payload []byte) error {
	header, err := c.buildHeader(streamID, len(payload))
	if err != nil {
		return err
	}
	if err := writeFull(c.conn, header[:]); err != nil {
		return err
	}
	if err := writeFull(c.conn, payload); err != nil {
		return err
	}
	return nil
}

func (c *Connection) readFrame() (uint32, []byte, error) {
	var header [frameHeaderLen]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return 0, nil, err
	}
	streamID, payloadLen, err := c.parseHeader(header)
	if err != nil {
		return 0, nil, err
	}
	if uint32(payloadLen) > c.config.maxFrame {
		return 0, nil, ErrFrameTooLarge{Size: payloadLen}
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, nil, err
	}
	return streamID, payload, nil
}

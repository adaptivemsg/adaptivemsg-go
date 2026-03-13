package adaptivemsg

import (
	"encoding/binary"
	"io"
	"net"
)

const (
	protocolVersion    = 2
	handshakeHeaderLen = 12
	maxCodecCount      = 16
)

var handshakeMagic = [2]byte{'A', 'M'}

const defaultMaxFrame = ^uint32(0)

func handshakeClient(conn net.Conn, codecs []CodecID, maxFrame uint32) (connConfig, error) {
	if err := validateCodecList(codecs); err != nil {
		return connConfig{}, err
	}

	request := make([]byte, handshakeHeaderLen)
	copy(request[0:2], handshakeMagic[:])
	request[2] = protocolVersion
	request[3] = byte(len(codecs))
	request[4] = 0
	request[5] = 0
	request[6] = 0
	request[7] = 0
	binary.BigEndian.PutUint32(request[8:12], maxFrame)
	if _, err := conn.Write(request); err != nil {
		return connConfig{}, err
	}
	list := make([]byte, len(codecs))
	for i, codecID := range codecs {
		list[i] = byte(codecID)
	}
	if _, err := conn.Write(list); err != nil {
		return connConfig{}, err
	}

	response := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(conn, response); err != nil {
		return connConfig{}, err
	}
	if response[0] != handshakeMagic[0] || response[1] != handshakeMagic[1] {
		return connConfig{}, ErrBadHandshakeMagic{}
	}
	accept := response[2]
	version := response[3]
	selected := CodecID(response[4])
	serverMax := binary.BigEndian.Uint32(response[8:12])
	if version != protocolVersion {
		return connConfig{}, ErrUnsupportedFrameVersion{Version: version}
	}
	if accept == 0 {
		return connConfig{}, ErrNoCommonCodec{}
	}
	if !containsCodec(codecs, selected) {
		return connConfig{}, ErrNoCommonCodec{}
	}
	codec, ok := codecByID(selected)
	if !ok {
		return connConfig{}, ErrUnsupportedCodec{Value: byte(selected)}
	}
	return connConfig{
		version:  version,
		codecID:  selected,
		codec:    codec,
		maxFrame: serverMax,
	}, nil
}

func handshakeServer(conn net.Conn, codecs []CodecID, maxFrame uint32) (connConfig, error) {
	if err := validateCodecList(codecs); err != nil {
		return connConfig{}, err
	}

	request := make([]byte, handshakeHeaderLen)
	if _, err := io.ReadFull(conn, request); err != nil {
		return connConfig{}, err
	}
	if request[0] != handshakeMagic[0] || request[1] != handshakeMagic[1] {
		return connConfig{}, ErrBadHandshakeMagic{}
	}
	version := request[2]
	codecCount := int(request[3])
	clientMaxFrame := binary.BigEndian.Uint32(request[8:12])
	if version != protocolVersion {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrUnsupportedFrameVersion{Version: version}
	}
	if codecCount == 0 {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrNoCommonCodec{}
	}
	if codecCount > maxCodecCount {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrTooManyCodecs{Count: codecCount}
	}
	clientCodecs := make([]byte, codecCount)
	if _, err := io.ReadFull(conn, clientCodecs); err != nil {
		return connConfig{}, err
	}
	selected, ok := selectCodec(clientCodecs, codecs)
	if !ok {
		_ = writeHandshakeReply(conn, 0, protocolVersion, 0, 0)
		return connConfig{}, ErrNoCommonCodec{}
	}
	negotiatedMax := negotiateMaxFrame(clientMaxFrame, maxFrame)
	if err := writeHandshakeReply(conn, 1, protocolVersion, selected, negotiatedMax); err != nil {
		return connConfig{}, err
	}
	codec, ok := codecByID(selected)
	if !ok {
		return connConfig{}, ErrUnsupportedCodec{Value: byte(selected)}
	}
	return connConfig{
		version:  protocolVersion,
		codecID:  selected,
		codec:    codec,
		maxFrame: negotiatedMax,
	}, nil
}

func validateCodecList(codecs []CodecID) error {
	if len(codecs) == 0 {
		return ErrInvalidMessage{Reason: "codec list must be non-empty"}
	}
	if len(codecs) > maxCodecCount {
		return ErrTooManyCodecs{Count: len(codecs)}
	}
	for _, codecID := range codecs {
		if codecID == 0 {
			return ErrInvalidMessage{Reason: "codec ID must be non-zero"}
		}
		if _, ok := codecByID(codecID); !ok {
			return ErrUnsupportedCodec{Value: byte(codecID)}
		}
	}
	return nil
}

func negotiateMaxFrame(clientMaxFrame, serverMaxFrame uint32) uint32 {
	if clientMaxFrame == 0 {
		return 0
	}
	if clientMaxFrame < serverMaxFrame {
		return clientMaxFrame
	}
	return serverMaxFrame
}

func writeHandshakeReply(conn net.Conn, accept byte, version byte, codecID CodecID, maxFrame uint32) error {
	response := make([]byte, handshakeHeaderLen)
	copy(response[0:2], handshakeMagic[:])
	response[2] = accept
	response[3] = version
	response[4] = byte(codecID)
	response[5] = 0
	response[6] = 0
	response[7] = 0
	binary.BigEndian.PutUint32(response[8:12], maxFrame)
	_, err := conn.Write(response)
	return err
}

func containsCodec(codecs []CodecID, id CodecID) bool {
	for _, codec := range codecs {
		if codec == id {
			return true
		}
	}
	return false
}

func selectCodec(clientCodecs []byte, supported []CodecID) (CodecID, bool) {
	for _, raw := range clientCodecs {
		for _, sup := range supported {
			if raw == byte(sup) {
				return sup, true
			}
		}
	}
	return 0, false
}

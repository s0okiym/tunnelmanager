package relay

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	FramePing         byte = 0x01
	FramePong         byte = 0x02
	FrameNewChannel   byte = 0x03
	FrameChannelData  byte = 0x04
	FrameChannelClose byte = 0x05
	FrameAuthRequest  byte = 0x06
	FrameAuthResponse byte = 0x07
	FrameRegister     byte = 0x08
)

type Frame struct {
	Type    byte
	Payload []byte
}

func ReadFrame(r io.Reader) (Frame, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:4])
	typ := header[4]
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}
	return Frame{Type: typ, Payload: payload}, nil
}

func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > 1<<24 {
		return fmt.Errorf("frame payload too large: %d", len(payload))
	}
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	header[4] = typ
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

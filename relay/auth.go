package relay

import (
	"encoding/binary"
	"fmt"
	"net"
)

func AuthClient(conn net.Conn, token string) error {
	tokenBytes := []byte(token)
	payload := make([]byte, 2+len(tokenBytes))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(tokenBytes)))
	copy(payload[2:], tokenBytes)
	if err := WriteFrame(conn, FrameAuthRequest, payload); err != nil {
		return fmt.Errorf("auth send: %w", err)
	}
	frame, err := ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("auth response: %w", err)
	}
	if frame.Type != FrameAuthResponse {
		return fmt.Errorf("unexpected frame type %d during auth", frame.Type)
	}
	if len(frame.Payload) < 1 || frame.Payload[0] == 0 {
		return fmt.Errorf("auth rejected")
	}
	return nil
}

func AuthServer(conn net.Conn, token string) error {
	frame, err := ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("auth request: %w", err)
	}
	if frame.Type != FrameAuthRequest {
		return fmt.Errorf("expected auth request, got type %d", frame.Type)
	}
	if len(frame.Payload) < 2 {
		return fmt.Errorf("short auth payload")
	}
	tokLen := binary.BigEndian.Uint16(frame.Payload[:2])
	if int(tokLen) != len(frame.Payload)-2 {
		return fmt.Errorf("auth token length mismatch")
	}
	receivedToken := string(frame.Payload[2:])

	ok := receivedToken == token
	resp := []byte{boolToByte(ok)}
	if err := WriteFrame(conn, FrameAuthResponse, resp); err != nil {
		return fmt.Errorf("auth reply: %w", err)
	}
	if !ok {
		return fmt.Errorf("auth failed: token mismatch")
	}
	return nil
}

func RegisterClient(conn net.Conn, tunnels []RemoteTunnel) error {
	return WriteFrame(conn, FrameRegister, packTunnels(tunnels))
}

func RegisterServer(conn net.Conn) ([]RemoteTunnel, error) {
	frame, err := ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("register read: %w", err)
	}
	if frame.Type != FrameRegister {
		return nil, fmt.Errorf("expected register, got type %d", frame.Type)
	}
	return unpackTunnels(frame.Payload)
}

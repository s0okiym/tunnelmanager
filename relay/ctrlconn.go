package relay

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// CtrlConn manages a multiplexed control connection. It owns the read loop,
// dispatches frames to channels, and serializes writes.
type CtrlConn struct {
	conn         net.Conn
	wmu          sync.Mutex
	channels     map[uint32]*channel
	cmu          sync.Mutex
	nextID       uint32
	newCh        chan *channel
	readErr      error
	done         chan struct{}
	closeOnce    sync.Once
	lastActivity atomic.Int64 // unix nanos of the most recent inbound frame
}

func NewCtrlConn(conn net.Conn) *CtrlConn {
	cc := &CtrlConn{
		conn:     conn,
		channels: make(map[uint32]*channel),
		nextID:   1,
		newCh:    make(chan *channel, 64),
		done:     make(chan struct{}),
	}
	cc.lastActivity.Store(time.Now().UnixNano())
	go cc.readLoop()
	return cc
}

func (cc *CtrlConn) readLoop() {
	defer close(cc.done)
	for {
		frame, err := ReadFrame(cc.conn)
		if err != nil {
			if err != io.EOF {
				cc.readErr = fmt.Errorf("ctrl read: %w", err)
			}
			cc.closeAll()
			return
		}
		cc.lastActivity.Store(time.Now().UnixNano())
		cc.handleFrame(frame)
	}
}

// LastActivity reports when the last frame was received from the peer.
func (cc *CtrlConn) LastActivity() time.Time {
	return time.Unix(0, cc.lastActivity.Load())
}

func (cc *CtrlConn) handleFrame(f Frame) {
	switch f.Type {
	case FrameNewChannel:
		ch := cc.handleNewChannel(f.Payload)
		if ch != nil {
			cc.newCh <- ch
		}
	case FrameChannelData:
		cc.handleChannelData(f.Payload)
	case FrameChannelClose:
		cc.handleChannelClose(f.Payload)
	case FrameWindowUpdate:
		cc.handleWindowUpdate(f.Payload)
	case FramePing:
		cc.handlePing(f.Payload)
	case FramePong:
	case FrameAuthRequest:
	case FrameAuthResponse:
	case FrameRegister:
	default:
	}
}

func (cc *CtrlConn) handleNewChannel(payload []byte) *channel {
	if len(payload) < 6 {
		return nil
	}
	chID := binary.BigEndian.Uint32(payload[:4])
	targetLen := binary.BigEndian.Uint16(payload[4:6])
	if len(payload) < 6+int(targetLen) {
		return nil
	}
	target := string(payload[6 : 6+targetLen])
	ch := newChannel(chID, cc, target)

	cc.cmu.Lock()
	cc.channels[chID] = ch
	cc.cmu.Unlock()
	return ch
}

func (cc *CtrlConn) handleChannelData(payload []byte) {
	if len(payload) < 4 {
		return
	}
	chID := binary.BigEndian.Uint32(payload[:4])

	cc.cmu.Lock()
	ch, ok := cc.channels[chID]
	cc.cmu.Unlock()

	if ok && ch != nil {
		ch.pushData(payload[4:])
	}
}

func (cc *CtrlConn) handleChannelClose(payload []byte) {
	if len(payload) < 4 {
		return
	}
	chID := binary.BigEndian.Uint32(payload[:4])

	cc.cmu.Lock()
	ch, ok := cc.channels[chID]
	cc.cmu.Unlock()

	if ok {
		ch.setError(io.EOF)
	}
}

func (cc *CtrlConn) handleWindowUpdate(payload []byte) {
	if len(payload) < 8 {
		return
	}
	chID := binary.BigEndian.Uint32(payload[:4])
	delta := binary.BigEndian.Uint32(payload[4:8])

	cc.cmu.Lock()
	ch, ok := cc.channels[chID]
	cc.cmu.Unlock()

	if ok {
		ch.addSendWindow(int64(delta))
	}
}

// removeChannel drops a fully-closed channel from the multiplexer map.
func (cc *CtrlConn) removeChannel(id uint32) {
	cc.cmu.Lock()
	delete(cc.channels, id)
	cc.cmu.Unlock()
}

func (cc *CtrlConn) handlePing(payload []byte) {
	cc.wmu.Lock()
	WriteFrame(cc.conn, FramePong, payload)
	cc.wmu.Unlock()
}

// sendFrame serializes a write to the underlying connection.
func (cc *CtrlConn) sendFrame(typ byte, payload []byte) error {
	cc.wmu.Lock()
	err := WriteFrame(cc.conn, typ, payload)
	cc.wmu.Unlock()
	return err
}

// OpenChannel sends a NewChannel request and returns a channel.
func (cc *CtrlConn) OpenChannel(target string) (*channel, error) {
	cc.cmu.Lock()
	chID := cc.nextID
	cc.nextID++
	ch := newChannel(chID, cc, target)
	cc.channels[chID] = ch
	cc.cmu.Unlock()

	targetBytes := []byte(target)
	payload := make([]byte, 4+2+len(targetBytes))
	binary.BigEndian.PutUint32(payload[:4], chID)
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(targetBytes)))
	copy(payload[6:], targetBytes)

	if err := cc.sendFrame(FrameNewChannel, payload); err != nil {
		cc.cmu.Lock()
		delete(cc.channels, chID)
		cc.cmu.Unlock()
		return nil, err
	}
	return ch, nil
}

// AcceptChannel blocks until a NewChannel arrives or the connection dies.
func (cc *CtrlConn) AcceptChannel() (*channel, error) {
	select {
	case ch := <-cc.newCh:
		return ch, nil
	case <-cc.done:
		if cc.readErr != nil {
			return nil, cc.readErr
		}
		return nil, io.EOF
	}
}

type RemoteTunnel struct {
	RemotePort uint16
	BindAddr   string // address the server listens on; empty means 0.0.0.0
	TargetAddr string // e.g. "localhost:8080"
}

func packTunnels(tunnels []RemoteTunnel) []byte {
	var buf []byte
	count := make([]byte, 2)
	binary.BigEndian.PutUint16(count, uint16(len(tunnels)))
	buf = append(buf, count...)
	for _, t := range tunnels {
		port := make([]byte, 2)
		binary.BigEndian.PutUint16(port, t.RemotePort)
		buf = append(buf, port...)
		bindBytes := []byte(t.BindAddr)
		bindLen := make([]byte, 2)
		binary.BigEndian.PutUint16(bindLen, uint16(len(bindBytes)))
		buf = append(buf, bindLen...)
		buf = append(buf, bindBytes...)
		targetBytes := []byte(t.TargetAddr)
		targetLen := make([]byte, 2)
		binary.BigEndian.PutUint16(targetLen, uint16(len(targetBytes)))
		buf = append(buf, targetLen...)
		buf = append(buf, targetBytes...)
	}
	return buf
}

func unpackTunnels(data []byte) ([]RemoteTunnel, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("short register payload")
	}
	count := binary.BigEndian.Uint16(data[:2])
	data = data[2:]
	tunnels := make([]RemoteTunnel, 0, count)
	for range count {
		if len(data) < 4 {
			return nil, fmt.Errorf("short tunnel entry")
		}
		port := binary.BigEndian.Uint16(data[:2])
		bindLen := binary.BigEndian.Uint16(data[2:4])
		data = data[4:]
		if len(data) < int(bindLen) {
			return nil, fmt.Errorf("short bind address")
		}
		bind := string(data[:bindLen])
		data = data[bindLen:]
		if len(data) < 2 {
			return nil, fmt.Errorf("short target length")
		}
		targetLen := binary.BigEndian.Uint16(data[:2])
		data = data[2:]
		if len(data) < int(targetLen) {
			return nil, fmt.Errorf("short target address")
		}
		target := string(data[:targetLen])
		data = data[targetLen:]
		tunnels = append(tunnels, RemoteTunnel{RemotePort: port, BindAddr: bind, TargetAddr: target})
	}
	return tunnels, nil
}

func (cc *CtrlConn) closeAll() {
	cc.cmu.Lock()
	for id, ch := range cc.channels {
		delete(cc.channels, id)
		ch.setError(io.EOF)
	}
	cc.cmu.Unlock()
}

func (cc *CtrlConn) Close() error {
	cc.closeAll()
	return cc.conn.Close()
}

func (cc *CtrlConn) Done() <-chan struct{} {
	return cc.done
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// KeepAlive sends periodic pings to the remote end. If timeout > 0 and no frame
// has been received from the peer within that window, the connection is
// considered dead and closed so the caller can reconnect.
func KeepAlive(cc *CtrlConn, interval, timeout time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if timeout > 0 && time.Since(cc.LastActivity()) > timeout {
				log.Printf("remote: no response from peer for %v; closing dead connection", timeout)
				cc.Close()
				return
			}
			var ts [8]byte
			binary.BigEndian.PutUint64(ts[:], uint64(time.Now().UnixNano()))
			cc.sendFrame(FramePing, ts[:])
		case <-stop:
			return
		}
	}
}

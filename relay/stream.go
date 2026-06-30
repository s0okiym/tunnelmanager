package relay

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

type channelStatus int

const (
	chOpen     channelStatus = iota
	chWriteClosed
	chClosed
)

// channel implements net.Conn over a multiplexed control connection.
type channel struct {
	id     uint32
	ctrl   *CtrlConn
	target string // target address for remote forwarding (set by NewChannel)
	rb     bytes.Buffer
	rmu    sync.Mutex
	rcond  *sync.Cond
	status channelStatus
	cmu    sync.Mutex
	rerr   error
}

func newChannel(id uint32, ctrl *CtrlConn, target string) *channel {
	ch := &channel{id: id, ctrl: ctrl, target: target}
	ch.rcond = sync.NewCond(&ch.rmu)
	return ch
}

func (ch *channel) Read(b []byte) (int, error) {
	ch.rmu.Lock()
	defer ch.rmu.Unlock()
	for ch.rb.Len() == 0 {
		if ch.rerr != nil {
			return 0, ch.rerr
		}
		ch.cmu.Lock()
		s := ch.status
		ch.cmu.Unlock()
		if s >= chClosed {
			return 0, io.EOF
		}
		ch.rcond.Wait()
	}
	return ch.rb.Read(b)
}

func (ch *channel) Write(b []byte) (int, error) {
	ch.cmu.Lock()
	if ch.status >= chWriteClosed {
		ch.cmu.Unlock()
		return 0, io.ErrClosedPipe
	}
	ch.cmu.Unlock()

	payload := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(payload[:4], ch.id)
	copy(payload[4:], b)
	if err := ch.ctrl.sendFrame(FrameChannelData, payload); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (ch *channel) CloseWrite() error {
	ch.cmu.Lock()
	if ch.status >= chWriteClosed {
		ch.cmu.Unlock()
		return nil
	}
	ch.status = chWriteClosed
	ch.cmu.Unlock()

	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, ch.id)
	return ch.ctrl.sendFrame(FrameChannelClose, payload)
}

func (ch *channel) Close() error {
	ch.cmu.Lock()
	if ch.status == chClosed {
		ch.cmu.Unlock()
		return nil
	}
	ch.status = chClosed
	ch.cmu.Unlock()

	ch.rmu.Lock()
	ch.rcond.Broadcast()
	ch.rmu.Unlock()

	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, ch.id)
	return ch.ctrl.sendFrame(FrameChannelClose, payload)
}

func (ch *channel) LocalAddr() net.Addr     { return nil }
func (ch *channel) RemoteAddr() net.Addr    { return nil }
func (ch *channel) SetDeadline(time.Time) error     { return nil }
func (ch *channel) SetReadDeadline(time.Time) error  { return nil }
func (ch *channel) SetWriteDeadline(time.Time) error { return nil }

func (ch *channel) pushData(data []byte) {
	ch.rmu.Lock()
	ch.rb.Write(data)
	ch.rcond.Broadcast()
	ch.rmu.Unlock()
}

func (ch *channel) setError(err error) {
	ch.rmu.Lock()
	ch.rerr = err
	ch.rcond.Broadcast()
	ch.rmu.Unlock()
}

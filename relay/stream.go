package relay

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type channelStatus int

const (
	chOpen channelStatus = iota
	chWriteClosed
	chClosed
)

const (
	// defaultSendWindow is the initial per-channel send credit. The receiver
	// replenishes it with FrameWindowUpdate as it consumes data.
	defaultSendWindow = 64 * 1024
	// maxReceiveBuffer caps inbound data buffered per channel. It is a safety
	// net; normal operation relies on the send window for back-pressure.
	maxReceiveBuffer = 256 * 1024
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

	sendWindow int64
	wmu        sync.Mutex
	wcond      *sync.Cond
}

func newChannel(id uint32, ctrl *CtrlConn, target string) *channel {
	ch := &channel{id: id, ctrl: ctrl, target: target, sendWindow: defaultSendWindow}
	ch.rcond = sync.NewCond(&ch.rmu)
	ch.wcond = sync.NewCond(&ch.wmu)
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
	n, err := ch.rb.Read(b)
	if n > 0 {
		ch.sendWindowUpdate(n)
	}
	return n, err
}

func (ch *channel) Write(b []byte) (int, error) {
	ch.cmu.Lock()
	if ch.status >= chWriteClosed {
		ch.cmu.Unlock()
		return 0, io.ErrClosedPipe
	}
	ch.cmu.Unlock()

	total := 0
	for len(b) > 0 {
		ch.wmu.Lock()
		for ch.sendWindow <= 0 {
			ch.cmu.Lock()
			s := ch.status
			ch.cmu.Unlock()
			if s >= chWriteClosed {
				ch.wmu.Unlock()
				return total, io.ErrClosedPipe
			}
			ch.wcond.Wait()
		}

		chunk := b
		if int64(len(chunk)) > ch.sendWindow {
			chunk = b[:ch.sendWindow]
		}
		ch.sendWindow -= int64(len(chunk))
		ch.wmu.Unlock()

		payload := make([]byte, 4+len(chunk))
		binary.BigEndian.PutUint32(payload[:4], ch.id)
		copy(payload[4:], chunk)
		if err := ch.ctrl.sendFrame(FrameChannelData, payload); err != nil {
			return total, err
		}
		total += len(chunk)
		b = b[len(chunk):]
	}
	return total, nil
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

	ch.wmu.Lock()
	ch.wcond.Broadcast()
	ch.wmu.Unlock()

	// A fully-closed channel is done in both directions; drop it from the
	// multiplexer so the channel map does not grow per handled connection.
	ch.ctrl.removeChannel(ch.id)

	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, ch.id)
	return ch.ctrl.sendFrame(FrameChannelClose, payload)
}

func (ch *channel) LocalAddr() net.Addr              { return nil }
func (ch *channel) RemoteAddr() net.Addr             { return nil }
func (ch *channel) SetDeadline(time.Time) error      { return nil }
func (ch *channel) SetReadDeadline(time.Time) error  { return nil }
func (ch *channel) SetWriteDeadline(time.Time) error { return nil }

func (ch *channel) pushData(data []byte) {
	ch.rmu.Lock()
	defer ch.rmu.Unlock()
	if ch.rb.Len()+len(data) > maxReceiveBuffer {
		// Safety net: the peer sent more than the receive buffer allows despite
		// the send window. Tear down the channel rather than unbounded growth.
		ch.rerr = fmt.Errorf("receive buffer overflow")
		ch.rcond.Broadcast()
		go ch.Close()
		return
	}
	ch.rb.Write(data)
	ch.rcond.Broadcast()
}

func (ch *channel) setError(err error) {
	ch.rmu.Lock()
	ch.rerr = err
	ch.rcond.Broadcast()
	ch.rmu.Unlock()
}

func (ch *channel) addSendWindow(delta int64) {
	ch.wmu.Lock()
	ch.sendWindow += delta
	ch.wcond.Broadcast()
	ch.wmu.Unlock()
}

func (ch *channel) sendWindowUpdate(delta int) {
	payload := make([]byte, 4+4)
	binary.BigEndian.PutUint32(payload[:4], ch.id)
	binary.BigEndian.PutUint32(payload[4:], uint32(delta))
	ch.ctrl.sendFrame(FrameWindowUpdate, payload)
}

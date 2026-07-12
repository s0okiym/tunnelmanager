package relay

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const udpSessionTimeout = 60 * time.Second

// UDPProxy implements UDP local forwarding (like ssh -L but for UDP).
// Because UDP is connectionless, it keeps a per-client-address session:
// the first packet from a client opens a UDP socket to the target, and
// return packets from that target socket are routed back to the client.
// Idle sessions are closed after udpSessionTimeout.
type UDPProxy struct {
	listener   *net.UDPConn
	dialAddr   *net.UDPAddr
	done       chan struct{}
	closeDone  sync.Once
	closed     atomic.Bool
	sessions   map[string]*udpSession
	sessionsMu sync.Mutex
}

type udpSession struct {
	clientAddr net.Addr
	targetConn *net.UDPConn
	lastActive time.Time
}

func NewUDPProxy(listenAddr, targetAddr string) (*UDPProxy, error) {
	laddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, err
	}
	daddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return nil, err
	}
	ln, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, err
	}
	return &UDPProxy{
		listener: ln,
		dialAddr: daddr,
		done:     make(chan struct{}),
		sessions: make(map[string]*udpSession),
	}, nil
}

func (p *UDPProxy) Addr() net.Addr {
	return p.listener.LocalAddr()
}

func (p *UDPProxy) Serve() error {
	buf := make([]byte, bufferSize)
	for {
		n, clientAddr, err := p.listener.ReadFromUDP(buf)
		if err != nil {
			if p.closed.Load() {
				return nil
			}
			return err
		}
		if n > 0 {
			packet := make([]byte, n)
			copy(packet, buf[:n])
			p.handlePacket(packet, clientAddr)
		}
	}
}

func (p *UDPProxy) handlePacket(packet []byte, clientAddr *net.UDPAddr) {
	key := clientAddr.String()

	p.sessionsMu.Lock()
	sess, ok := p.sessions[key]
	if !ok {
		targetConn, err := net.DialUDP("udp", nil, p.dialAddr)
		if err != nil {
			p.sessionsMu.Unlock()
			return
		}
		sess = &udpSession{
			clientAddr: clientAddr,
			targetConn: targetConn,
			lastActive: time.Now(),
		}
		p.sessions[key] = sess
		go p.readFromTarget(sess)
	} else {
		sess.lastActive = time.Now()
	}
	p.sessionsMu.Unlock()

	sess.targetConn.Write(packet)
}

func (p *UDPProxy) readFromTarget(sess *udpSession) {
	buf := make([]byte, bufferSize)
	for {
		sess.targetConn.SetReadDeadline(time.Now().Add(udpSessionTimeout))
		n, err := sess.targetConn.Read(buf)
		if err != nil {
			// Timeout or error: clean up the session.
			p.removeSession(sess.clientAddr.String())
			return
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])
		p.listener.WriteToUDP(packet, sess.clientAddr.(*net.UDPAddr))

		p.sessionsMu.Lock()
		sess.lastActive = time.Now()
		p.sessionsMu.Unlock()
	}
}

func (p *UDPProxy) removeSession(key string) {
	p.sessionsMu.Lock()
	sess, ok := p.sessions[key]
	if ok {
		delete(p.sessions, key)
	}
	p.sessionsMu.Unlock()
	if ok {
		sess.targetConn.Close()
	}
}

func (p *UDPProxy) Close() error {
	p.closed.Store(true)
	err := p.listener.Close()
	p.closeDone.Do(func() { close(p.done) })

	p.sessionsMu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*udpSession)
	p.sessionsMu.Unlock()
	for _, sess := range sessions {
		sess.targetConn.Close()
	}
	return err
}

func (p *UDPProxy) Done() <-chan struct{} {
	return p.done
}

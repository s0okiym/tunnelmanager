package relay

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

type SocksProxy struct {
	listener  net.Listener
	user      string
	pass      string
	done      chan struct{}
	closeDone sync.Once
	closed    atomic.Bool
	mu        sync.Mutex
	stats     Stats
	connCount atomic.Int64
}

func NewSocksProxy(listenAddr string) (*SocksProxy, error) {
	return NewSocksProxyWithAuth(listenAddr, "", "")
}

func NewSocksProxyWithAuth(listenAddr, user, pass string) (*SocksProxy, error) {
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return &SocksProxy{
		listener: l,
		user:     user,
		pass:     pass,
		done:     make(chan struct{}),
	}, nil
}

func (p *SocksProxy) Addr() net.Addr {
	return p.listener.Addr()
}

func (p *SocksProxy) Serve() error {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			if p.closed.Load() {
				return nil
			}
			return err
		}
		go p.handle(conn)
	}
}

func (p *SocksProxy) handle(conn net.Conn) {
	defer conn.Close()

	target, err := socksHandshake(conn, p.user, p.pass)
	if err != nil {
		return
	}

	upstream, err := net.Dial("tcp", target.addr)
	if err != nil {
		socksReply(conn, 4, socksAtypIPv4, []byte{0, 0, 0, 0}, []byte{0, 0})
		return
	}
	defer upstream.Close()

	rawAddr := packedAddr(upstream.LocalAddr())
	if err := socksReply(conn, socksRepSuccess, rawAddr[0], rawAddr[1:len(rawAddr)-2], rawAddr[len(rawAddr)-2:]); err != nil {
		return
	}

	p.connCount.Add(1)
	defer p.connCount.Add(-1)
	s := Relay(upstream, conn)

	p.mu.Lock()
	p.stats.SentBytes += s.SentBytes
	p.stats.RecvBytes += s.RecvBytes
	if s.SentErr != nil {
		p.stats.SentErr = s.SentErr
	}
	if s.RecvErr != nil {
		p.stats.RecvErr = s.RecvErr
	}
	p.mu.Unlock()
}

func (p *SocksProxy) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *SocksProxy) Close() error {
	p.closed.Store(true)
	err := p.listener.Close()
	p.closeDone.Do(func() { close(p.done) })
	return err
}

func (p *SocksProxy) Done() <-chan struct{} {
	return p.done
}

func ListenSocks(addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("socks listen: %w", err)
	}
	return l, nil
}

func packedAddr(addr net.Addr) []byte {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		// Fallback for non-TCP upstream addresses: report 0.0.0.0:0.
		return append([]byte{socksAtypIPv4}, append([]byte{0, 0, 0, 0}, 0, 0)...)
	}
	ip := tcp.IP
	port := make([]byte, 2)
	port[0] = byte(tcp.Port >> 8)
	port[1] = byte(tcp.Port)

	if ip4 := ip.To4(); ip4 != nil {
		return append([]byte{socksAtypIPv4}, append(ip4, port...)...)
	}
	if ip16 := ip.To16(); ip16 != nil {
		return append([]byte{socksAtypIPv6}, append(ip16, port...)...)
	}
	return append([]byte{socksAtypIPv4}, append([]byte{0, 0, 0, 0}, port...)...)
}

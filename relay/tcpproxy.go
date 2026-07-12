package relay

import (
	"net"
	"sync"
	"sync/atomic"
)

type Proxy struct {
	listener  net.Listener
	dialAddr  string
	done      chan struct{}
	closeDone sync.Once
	closed    atomic.Bool
	mu        sync.Mutex
	stats     Stats
	connCount atomic.Int64
}

func NewProxy(listenAddr, dialAddr string) (*Proxy, error) {
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return &Proxy{
		listener: l,
		dialAddr: dialAddr,
		done:     make(chan struct{}),
	}, nil
}

func NewTLSProxy(listenAddr, dialAddr string, tlsCfg *TLSConfig) (*Proxy, error) {
	l, err := TLSListener(listenAddr, tlsCfg)
	if err != nil {
		return nil, err
	}
	return &Proxy{
		listener: l,
		dialAddr: dialAddr,
		done:     make(chan struct{}),
	}, nil
}

func (p *Proxy) Addr() net.Addr {
	return p.listener.Addr()
}

func (p *Proxy) Serve() error {
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

func (p *Proxy) handle(downstream net.Conn) {
	defer downstream.Close()

	upstream, err := net.Dial("tcp", p.dialAddr)
	if err != nil {
		return
	}
	defer upstream.Close()

	p.connCount.Add(1)
	defer p.connCount.Add(-1)
	s := Relay(upstream, downstream)

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

func (p *Proxy) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *Proxy) Close() error {
	p.closed.Store(true)
	err := p.listener.Close()
	p.closeDone.Do(func() { close(p.done) })
	return err
}

func (p *Proxy) Done() <-chan struct{} {
	return p.done
}

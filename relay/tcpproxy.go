package relay

import (
	"net"
	"sync/atomic"
)

type Proxy struct {
	listener  net.Listener
	dialAddr  string
	done      chan struct{}
	closed    atomic.Bool
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

	Relay(upstream, downstream)
}

func (p *Proxy) Close() error {
	p.closed.Store(true)
	return p.listener.Close()
}

func (p *Proxy) Done() <-chan struct{} {
	return p.done
}

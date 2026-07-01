package relay

import (
	"log"
	"net"
	"sync/atomic"
)

type ChainProxy struct {
	listener net.Listener
	hops     []string
	closed   atomic.Bool
}

func NewChainProxy(listenAddr string, hops []string) (*ChainProxy, error) {
	if len(hops) == 0 {
		return nil, nil
	}
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	log.Printf("chain: listening on %s, hops: %v", listenAddr, hops)
	return &ChainProxy{
		listener: l,
		hops:     hops,
	}, nil
}

func (cp *ChainProxy) Serve() error {
	for {
		conn, err := cp.listener.Accept()
		if err != nil {
			if cp.closed.Load() {
				return nil
			}
			return err
		}
		go cp.handle(conn)
	}
}

func (cp *ChainProxy) handle(conn net.Conn) {
	defer conn.Close()

	// Dial through the chain: conn -> hop1 -> hop2 -> ... -> final
	// Each hop is a running tunnel that forwards to the next.
	// We just need to dial the first hop and let it chain.
	next, err := net.Dial("tcp", cp.hops[0])
	if err != nil {
		return
	}
	defer next.Close()

	Relay(conn, next)
}

func (cp *ChainProxy) Close() error {
	cp.closed.Store(true)
	return cp.listener.Close()
}

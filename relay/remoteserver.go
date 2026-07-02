package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type RemoteServer struct {
	ctrlLn net.Listener
	token  string
	tlsCfg *TLSConfig
	mu     sync.Mutex
	conns  map[*CtrlConn]struct{}
	closed bool
	wg     sync.WaitGroup
	done   chan struct{}
}

func NewRemoteServer(listenAddr, token string, tlsCfg *TLSConfig) (*RemoteServer, error) {
	var ln net.Listener
	var err error
	if tlsCfg != nil && tlsCfg.Enabled {
		tlsLn, tlsErr := TLSListener(listenAddr, tlsCfg)
		if tlsErr != nil {
			return nil, fmt.Errorf("remote server tls listen: %w", tlsErr)
		}
		ln = tlsLn
	} else {
		ln, err = net.Listen("tcp", listenAddr)
		if err != nil {
			return nil, fmt.Errorf("remote server listen: %w", err)
		}
	}

	return &RemoteServer{
		ctrlLn: ln,
		token:  token,
		tlsCfg: tlsCfg,
		conns:  make(map[*CtrlConn]struct{}),
		done:   make(chan struct{}),
	}, nil
}

func (rs *RemoteServer) Addr() net.Addr {
	return rs.ctrlLn.Addr()
}

func (rs *RemoteServer) Serve() error {
	for {
		conn, err := rs.ctrlLn.Accept()
		if err != nil {
			select {
			case <-rs.done:
				return nil
			default:
				return fmt.Errorf("remote server accept: %w", err)
			}
		}
		rs.wg.Add(1)
		go rs.handleClient(conn)
	}
}

func (rs *RemoteServer) handleClient(conn net.Conn) {
	defer rs.wg.Done()
	defer conn.Close()

	log.Printf("remote: new control connection from %s", conn.RemoteAddr())

	if rs.token != "" {
		if err := AuthServer(conn, rs.token); err != nil {
			log.Printf("remote: auth failed from %s: %v", conn.RemoteAddr(), err)
			return
		}
	}

	tunnels, err := RegisterServer(conn)
	if err != nil {
		log.Printf("remote: register failed: %v", err)
		return
	}

	cc := NewCtrlConn(conn)
	defer cc.Close()

	// Register the control connection so Close() can tear it down.
	if !rs.trackConn(cc) {
		return // server already closing
	}
	defer rs.untrackConn(cc)

	for _, t := range tunnels {
		rs.wg.Add(1)
		go rs.serveRemoteTunnel(cc, t.RemotePort, t.TargetAddr)
	}

	<-cc.Done()
}

func (rs *RemoteServer) trackConn(cc *CtrlConn) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return false
	}
	rs.conns[cc] = struct{}{}
	return true
}

func (rs *RemoteServer) untrackConn(cc *CtrlConn) {
	rs.mu.Lock()
	delete(rs.conns, cc)
	rs.mu.Unlock()
}

func (rs *RemoteServer) serveRemoteTunnel(cc *CtrlConn, remotePort uint16, targetAddr string) {
	defer rs.wg.Done()

	listenAddr := fmt.Sprintf("0.0.0.0:%d", remotePort)
	ln, err := listenTunnel(listenAddr, cc)
	if err != nil {
		log.Printf("remote: cannot listen on %s: %v", listenAddr, err)
		return
	}
	defer ln.Close()

	// Release the listener as soon as the control connection dies, so the port
	// is freed for the next (re)connection and the Accept below unblocks.
	go func() {
		<-cc.Done()
		ln.Close()
	}()

	log.Printf("remote: listening on %s -> target %s", listenAddr, targetAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-cc.Done():
				// listener was closed on control-connection teardown
			default:
				log.Printf("remote: accept on %s: %v", listenAddr, err)
			}
			return
		}

		ch, err := cc.OpenChannel(targetAddr)
		if err != nil {
			log.Printf("remote: open channel for %s: %v", targetAddr, err)
			conn.Close()
			return
		}

		go func() {
			defer conn.Close()
			defer ch.Close()
			Relay(conn, ch)
		}()
	}
}

// listenTunnel binds addr, retrying briefly if the port is still held by a
// just-torn-down connection (the reconnect handoff race). It aborts early if
// the control connection dies while waiting.
func listenTunnel(addr string, cc *CtrlConn) (net.Listener, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-cc.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (rs *RemoteServer) Close() error {
	rs.mu.Lock()
	if rs.closed {
		rs.mu.Unlock()
		return nil
	}
	rs.closed = true
	conns := make([]*CtrlConn, 0, len(rs.conns))
	for cc := range rs.conns {
		conns = append(conns, cc)
	}
	rs.mu.Unlock()

	close(rs.done)
	err := rs.ctrlLn.Close()
	for _, cc := range conns {
		cc.Close()
	}
	return err
}

func (rs *RemoteServer) Wait() {
	rs.wg.Wait()
}

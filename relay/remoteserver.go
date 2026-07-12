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

	// listeners holds the per-bind:port shared public listener. Multiple control
	// connections may register the same remote port and bind address; they share
	// one listener and incoming connections are distributed round-robin across
	// the control connections.
	listeners map[listenerKey]*sharedListener
}

type listenerKey struct {
	port uint16
	bind string
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
		ctrlLn:    ln,
		token:     token,
		tlsCfg:    tlsCfg,
		conns:     make(map[*CtrlConn]struct{}),
		listeners: make(map[listenerKey]*sharedListener),
		done:      make(chan struct{}),
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
		go rs.serveRemoteTunnel(cc, t.RemotePort, t.BindAddr, t.TargetAddr)
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

func defaultBindAddr(addr string) string {
	if addr == "" {
		return "0.0.0.0"
	}
	return addr
}

func (rs *RemoteServer) serveRemoteTunnel(cc *CtrlConn, remotePort uint16, bindAddr, targetAddr string) {
	defer rs.wg.Done()

	sl, err := rs.attachSharedListener(remotePort, bindAddr, targetAddr, cc)
	if err != nil {
		log.Printf("remote: cannot listen on %s:%d: %v", defaultBindAddr(bindAddr), remotePort, err)
		return
	}

	log.Printf("remote: listening on %s:%d -> target %s", defaultBindAddr(bindAddr), remotePort, targetAddr)

	// Wait until this control connection dies, then detach it from the shared
	// listener. The shared listener stays open as long as at least one control
	// connection is registered for this bind:port.
	<-cc.Done()
	rs.detachSharedListener(sl, cc)
}

// sharedListener is a single public listener shared by multiple control
// connections that have registered the same remote port and bind address.
// Incoming connections are distributed round-robin across the registered
// control connections.
type sharedListener struct {
	rs       *RemoteServer
	port     uint16
	bindAddr string
	ln       net.Listener
	mu       sync.Mutex
	conns    map[*CtrlConn]string // control conn -> target address
	order    []*CtrlConn          // round-robin order
	next     int                  // index of next control conn to use
	closed   bool
}

func (rs *RemoteServer) attachSharedListener(port uint16, bindAddr, target string, cc *CtrlConn) (*sharedListener, error) {
	key := listenerKey{port: port, bind: bindAddr}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil, fmt.Errorf("server closed")
	}

	if sl, ok := rs.listeners[key]; ok {
		sl.mu.Lock()
		if !sl.closed {
			// Update the target if this control connection is already
			// registered, otherwise append it.
			if _, exists := sl.conns[cc]; !exists {
				sl.conns[cc] = target
				sl.order = append(sl.order, cc)
			} else {
				sl.conns[cc] = target
			}
			sl.mu.Unlock()
			return sl, nil
		}
		// Stale entry: the listener was closed but not yet removed.
		sl.mu.Unlock()
		delete(rs.listeners, key)
	}

	listenAddr := net.JoinHostPort(defaultBindAddr(bindAddr), fmt.Sprintf("%d", port))
	ln, err := listenTunnel(listenAddr, cc)
	if err != nil {
		return nil, err
	}

	sl := &sharedListener{
		rs:       rs,
		port:     port,
		bindAddr: bindAddr,
		ln:       ln,
		conns:    map[*CtrlConn]string{cc: target},
		order:    []*CtrlConn{cc},
	}
	rs.listeners[key] = sl
	rs.wg.Add(1)
	go sl.acceptLoop()
	return sl, nil
}

func (rs *RemoteServer) detachSharedListener(sl *sharedListener, cc *CtrlConn) {
	sl.mu.Lock()
	if _, ok := sl.conns[cc]; !ok {
		sl.mu.Unlock()
		return
	}
	delete(sl.conns, cc)

	// Remove cc from round-robin order and adjust next index.
	idx := -1
	for i, c := range sl.order {
		if c == cc {
			idx = i
			break
		}
	}
	if idx >= 0 {
		sl.order = append(sl.order[:idx], sl.order[idx+1:]...)
		if sl.next > idx {
			sl.next--
		}
		if sl.next >= len(sl.order) {
			sl.next = 0
		}
	}

	shouldClose := len(sl.order) == 0
	sl.mu.Unlock()

	if shouldClose {
		sl.Close()
		rs.mu.Lock()
		key := listenerKey{port: sl.port, bind: sl.bindAddr}
		if rs.listeners[key] == sl {
			delete(rs.listeners, key)
		}
		rs.mu.Unlock()
	}
}

func (sl *sharedListener) acceptLoop() {
	defer sl.rs.wg.Done()

	for {
		conn, err := sl.ln.Accept()
		if err != nil {
			sl.mu.Lock()
			closed := sl.closed
			sl.mu.Unlock()
			if closed {
				return
			}
			log.Printf("remote: accept on %s:%d: %v", defaultBindAddr(sl.bindAddr), sl.port, err)
			return
		}
		sl.dispatch(conn)
	}
}

func (sl *sharedListener) dispatch(conn net.Conn) {
	sl.mu.Lock()
	if len(sl.order) == 0 {
		sl.mu.Unlock()
		conn.Close()
		return
	}
	cc := sl.order[sl.next%len(sl.order)]
	target := sl.conns[cc]
	sl.next = (sl.next + 1) % len(sl.order)
	sl.mu.Unlock()

	ch, err := cc.OpenChannel(target)
	if err != nil {
		log.Printf("remote: open channel for %s: %v", target, err)
		conn.Close()
		return
	}

	go func() {
		defer conn.Close()
		defer ch.Close()
		Relay(conn, ch)
	}()
}

func (sl *sharedListener) Close() {
	sl.mu.Lock()
	if sl.closed {
		sl.mu.Unlock()
		return
	}
	sl.closed = true
	sl.mu.Unlock()
	sl.ln.Close()
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
	listeners := make([]*sharedListener, 0, len(rs.listeners))
	for _, sl := range rs.listeners {
		listeners = append(listeners, sl)
	}
	rs.mu.Unlock()

	close(rs.done)
	err := rs.ctrlLn.Close()
	for _, cc := range conns {
		cc.Close()
	}
	for _, sl := range listeners {
		sl.Close()
	}
	return err
}

func (rs *RemoteServer) Wait() {
	rs.wg.Wait()
}

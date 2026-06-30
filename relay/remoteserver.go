package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
)

type RemoteServer struct {
	ctrlLn  net.Listener
	token   string
	tlsCfg  *TLSConfig
	mu      sync.Mutex
	wg      sync.WaitGroup
	done    chan struct{}
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
		done:   make(chan struct{}),
	}, nil
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
	if err != nil {
		log.Printf("remote: register failed: %v", err)
		return
	}

	for _, t := range tunnels {
		rs.mu.Lock()
		rs.wg.Add(1)
		rs.mu.Unlock()

		go rs.serveRemoteTunnel(cc, t.RemotePort, t.TargetAddr)
	}

	<-cc.Done()
}

func (rs *RemoteServer) serveRemoteTunnel(cc *CtrlConn, remotePort uint16, targetAddr string) {
	defer rs.wg.Done()

	listenAddr := fmt.Sprintf("0.0.0.0:%d", remotePort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("remote: cannot listen on %s: %v", listenAddr, err)
		return
	}
	defer ln.Close()

	log.Printf("remote: listening on %s -> target %s", listenAddr, targetAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("remote: accept on %s: %v", listenAddr, err)
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

func (rs *RemoteServer) Close() error {
	close(rs.done)
	return rs.ctrlLn.Close()
}

func (rs *RemoteServer) Wait() {
	rs.wg.Wait()
}

package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type RemoteClient struct {
	serverAddr string
	token      string
	tlsCfg     *TLSConfig
	tunnels    []RemoteTunnel
	wg         sync.WaitGroup
	stopCh     chan struct{}
	cc         *CtrlConn
	mu         sync.Mutex
}

func NewRemoteClient(serverAddr, token string, tlsCfg *TLSConfig, tunnels []RemoteTunnel) *RemoteClient {
	return &RemoteClient{
		serverAddr: serverAddr,
		token:      token,
		tlsCfg:     tlsCfg,
		tunnels:    tunnels,
		stopCh:     make(chan struct{}),
	}
}

func (rc *RemoteClient) Run() {
	backoff := DefaultBackoff
	attempt := 0

	for {
		select {
		case <-rc.stopCh:
			return
		default:
		}

		err := rc.connect()
		if err != nil {
			select {
			case <-rc.stopCh:
				return
			default:
			}
			delay := BackoffDelay(backoff, attempt)
			attempt++
			log.Printf("remote: connection failed (%v), reconnecting in %v (attempt %d)", err, delay, attempt)
			select {
			case <-time.After(delay):
			case <-rc.stopCh:
				return
			}
		} else {
			attempt = 0
		}
	}
}

func (rc *RemoteClient) connect() error {
	var conn net.Conn
	var err error

	if rc.tlsCfg != nil && rc.tlsCfg.Enabled {
		conn, err = TLSDial(rc.serverAddr, rc.tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", rc.serverAddr, 10*time.Second)
	}
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}

	if rc.token != "" {
		if err := AuthClient(conn, rc.token); err != nil {
			conn.Close()
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err := RegisterClient(conn, rc.tunnels); err != nil {
		conn.Close()
		return fmt.Errorf("register: %w", err)
	}

	cc := NewCtrlConn(conn)

	rc.mu.Lock()
	rc.cc = cc
	rc.mu.Unlock()

	defer func() {
		rc.mu.Lock()
		rc.cc = nil
		rc.mu.Unlock()
	}()

	stopHeartbeat := make(chan struct{})
	go KeepAlive(cc, 15*time.Second, stopHeartbeat)
	defer close(stopHeartbeat)

	log.Printf("remote: connected to %s, %d tunnels registered", rc.serverAddr, len(rc.tunnels))

	rc.handleChannels(cc)
	return nil
}

func (rc *RemoteClient) handleChannels(cc *CtrlConn) {
	for {
		ch, err := cc.AcceptChannel()
		if err != nil {
			log.Printf("remote: accept channel: %v", err)
			return
		}

		rc.wg.Add(1)
		go rc.relayChannel(ch)
	}
}

func (rc *RemoteClient) relayChannel(ch *channel) {
	defer rc.wg.Done()

	local, err := net.DialTimeout("tcp", ch.target, 10*time.Second)
	if err != nil {
		log.Printf("remote: dial local target %s: %v", ch.target, err)
		ch.Close()
		return
	}

	Relay(local, ch)
	local.Close()
	ch.Close()
}

func (rc *RemoteClient) Close() error {
	close(rc.stopCh)
	rc.mu.Lock()
	if rc.cc != nil {
		rc.cc.Close()
	}
	rc.mu.Unlock()
	return nil
}

func (rc *RemoteClient) Wait() {
	rc.wg.Wait()
}

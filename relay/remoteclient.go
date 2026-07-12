package relay

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type RemoteClient struct {
	serverAddr     string
	token          string
	tlsCfg         *TLSConfig
	tunnels        []RemoteTunnel
	wg             sync.WaitGroup
	stopCh         chan struct{}
	cc             *CtrlConn
	mu             sync.Mutex
	lastErr        atomic.Value
	reconnectCount atomic.Int64
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

// LastError returns the most recent non-nil error encountered by the client.
func (rc *RemoteClient) LastError() error {
	v := rc.lastErr.Load()
	if v == nil {
		return nil
	}
	return v.(error)
}

func (rc *RemoteClient) ReconnectCount() int64 {
	return rc.reconnectCount.Load()
}

func (rc *RemoteClient) IsConnected() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.cc != nil
}

func (rc *RemoteClient) setLastErr(err error) {
	if err != nil {
		rc.lastErr.Store(err)
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
			rc.reconnectCount.Add(1)
			log.Printf("remote: connection failed (%v), reconnecting in %v (attempt %d)", err, delay, attempt)
			rc.setLastErr(err)
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
	go KeepAlive(cc, 15*time.Second, 45*time.Second, stopHeartbeat)
	defer close(stopHeartbeat)

	log.Printf("remote: connected to %s, %d tunnels registered", rc.serverAddr, len(rc.tunnels))

	rc.handleChannels(cc)
	return nil
}

func (rc *RemoteClient) handleChannels(cc *CtrlConn) {
	for {
		ch, err := cc.AcceptChannel()
		if err != nil {
			rc.setLastErr(fmt.Errorf("accept channel: %w", err))
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
		rc.setLastErr(fmt.Errorf("dial local target %s: %w", ch.target, err))
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

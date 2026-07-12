package manager

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"tunnel/relay"
)

type Manager struct {
	mu         sync.Mutex
	cfg        *Config
	cfgPath    string
	tunnels    map[string]*managedTunnel
	stopCh     chan struct{}
	knownHosts *relay.KnownHosts
}

type managedTunnel struct {
	cfg     TunnelConfig
	cancel  func()
	done    chan struct{} // closed when the tunnel goroutine has fully returned
	lastErr error
	errMu   sync.Mutex
	state   *TunnelState
}

// TunnelState tracks the live status of a managed tunnel. It is safe for
// concurrent use by the tunnel goroutine and the List() reader.
type TunnelState struct {
	mu             sync.RWMutex
	Status         string
	Since          time.Time
	ReconnectCount int64
	LastErr        string
}

// Set updates the status and optional error message. It records a new Since
// timestamp whenever the status transitions.
func (ts *TunnelState) Set(status, err string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.Status != status {
		ts.Status = status
		ts.Since = time.Now()
	}
	ts.LastErr = err
}

// SetReconnectCount updates the total reconnect count seen by a remote client.
func (ts *TunnelState) SetReconnectCount(n int64) {
	ts.mu.Lock()
	ts.ReconnectCount = n
	ts.mu.Unlock()
}

// Get returns a snapshot of the state.
func (ts *TunnelState) Get() (status string, since time.Time, reconnect int64, lastErr string) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.Status, ts.Since, ts.ReconnectCount, ts.LastErr
}

func NewManager(cfg *Config, cfgPath string) *Manager {
	kh, _ := relay.LoadKnownHosts(filepath.Join(DefaultConfigDir, "known_hosts"))
	return &Manager{
		cfg:        cfg,
		cfgPath:    cfgPath,
		tunnels:    make(map[string]*managedTunnel),
		stopCh:     make(chan struct{}),
		knownHosts: kh,
	}
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	for _, tc := range m.cfg.Tunnels {
		if tc.Autostart {
			if err := m.startOne(tc); err != nil {
				log.Printf("manager: failed to start %s: %v", tc.Name, err)
			}
		}
	}
	return nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *Manager) stopLocked() error {
	// Cancel everything first, then wait for teardown, so listeners are
	// released concurrently rather than one-at-a-time.
	type pending struct {
		name string
		done chan struct{}
	}
	var waiting []pending
	for name, mt := range m.tunnels {
		mt.cancel()
		waiting = append(waiting, pending{name: name, done: mt.done})
		delete(m.tunnels, name)
	}
	for _, p := range waiting {
		m.waitTunnelStopped(p.name, p.done)
	}
	return nil
}

// waitTunnelStopped blocks until the tunnel goroutine has returned (its
// listener is released) or a safety timeout elapses.
func (m *Manager) waitTunnelStopped(name string, done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("manager: timeout waiting for %s to stop", name)
	}
	log.Printf("manager: stopped %s", name)
}

// stopTunnelLocked cancels a single tunnel and waits for it to release its
// resources. Caller must hold m.mu.
func (m *Manager) stopTunnelLocked(name string, mt *managedTunnel) {
	mt.cancel()
	delete(m.tunnels, name)
	m.waitTunnelStopped(name, mt.done)
}

func (m *Manager) StopGroup(group string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, mt := range m.tunnels {
		if mt.cfg.Group == group {
			m.stopTunnelLocked(name, mt)
		}
	}
	return nil
}

func (m *Manager) StartGroup(group string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range m.cfg.Tunnels {
		if tc.Group == group {
			if _, running := m.tunnels[tc.Name]; !running {
				if err := m.startOne(tc); err != nil {
					log.Printf("manager: failed to start %s (group %s): %v", tc.Name, group, err)
				}
			}
		}
	}
	return nil
}

func (m *Manager) List() []TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make([]TunnelStatus, 0, len(m.cfg.Tunnels))
	for _, tc := range m.cfg.Tunnels {
		statuses = append(statuses, m.tunnelStatusLocked(tc))
	}
	return statuses
}

func (m *Manager) tunnelStatusLocked(tc TunnelConfig) TunnelStatus {
	mt, running := m.tunnels[tc.Name]
	st := TunnelStatus{
		Name:   tc.Name,
		Mode:   tc.Mode,
		Local:  tc.Local,
		Remote: tc.Remote,
		Group:  tc.Group,
	}
	if running && mt.state != nil {
		status, since, reconnect, lastErr := mt.state.Get()
		st.Status = status
		st.Since = since
		st.ReconnectCount = reconnect
		st.Error = lastErr
		if st.Status == "" {
			st.Status = "running"
		}
	} else {
		st.Status = "stopped"
	}
	return st
}

// Status returns the current status of a single configured tunnel.
func (m *Manager) Status(name string) (TunnelStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.findConfig(name)
	if idx < 0 {
		return TunnelStatus{}, fmt.Errorf("tunnel %q not found", name)
	}
	return m.tunnelStatusLocked(m.cfg.Tunnels[idx]), nil
}

// Logs returns the most recent log lines for the named tunnel. An empty name
// returns the global recent log tail. The limit is capped by LogBuffer capacity.
func (m *Manager) Logs(name string, limit int) ([]string, error) {
	m.mu.Lock()
	if name != "" && m.findConfig(name) < 0 {
		m.mu.Unlock()
		return nil, fmt.Errorf("tunnel %q not found", name)
	}
	m.mu.Unlock()
	return GlobalLogBuffer().Lines(name, limit), nil
}

func (m *Manager) Add(tc TunnelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.findConfig(tc.Name) >= 0 {
		return fmt.Errorf("tunnel %q already exists", tc.Name)
	}

	m.cfg.Tunnels = append(m.cfg.Tunnels, tc)
	if err := SaveConfig(m.cfg, m.cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if tc.Autostart {
		return m.startOne(tc)
	}
	return nil
}

func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if mt, ok := m.tunnels[name]; ok {
		m.stopTunnelLocked(name, mt)
	}

	idx := m.findConfig(name)
	if idx < 0 {
		return fmt.Errorf("tunnel %q not found", name)
	}
	m.cfg.Tunnels = append(m.cfg.Tunnels[:idx], m.cfg.Tunnels[idx+1:]...)
	return SaveConfig(m.cfg, m.cfgPath)
}

// Reload re-reads the config from disk and restarts all tunnels in place. It
// holds the manager lock across the whole stop/swap/start so it never races
// concurrent control requests, and reuses the same Manager instance so a bound
// control-server handler keeps working after a reload.
func (m *Manager) Reload() error {
	newCfg, err := LoadConfig(m.cfgPath)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.cfg = newCfg
	return m.startLocked()
}

func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.findConfig(name)
	if idx < 0 {
		return fmt.Errorf("tunnel %q not found", name)
	}
	m.cfg.Tunnels[idx].Autostart = true
	if err := SaveConfig(m.cfg, m.cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if _, running := m.tunnels[name]; !running {
		return m.startOne(m.cfg.Tunnels[idx])
	}
	return nil
}

func (m *Manager) Disable(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.findConfig(name)
	if idx < 0 {
		return fmt.Errorf("tunnel %q not found", name)
	}
	m.cfg.Tunnels[idx].Autostart = false
	if err := SaveConfig(m.cfg, m.cfgPath); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if mt, running := m.tunnels[name]; running {
		m.stopTunnelLocked(name, mt)
	}
	return nil
}

func (m *Manager) Restart(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.findConfig(name)
	if idx < 0 {
		return fmt.Errorf("tunnel %q not found", name)
	}
	if mt, running := m.tunnels[name]; running {
		m.stopTunnelLocked(name, mt)
	}
	return m.startOne(m.cfg.Tunnels[idx])
}

func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SaveConfig(m.cfg, m.cfgPath)
}

func (m *Manager) findConfig(name string) int {
	for i, tc := range m.cfg.Tunnels {
		if tc.Name == name {
			return i
		}
	}
	return -1
}

func (m *Manager) startOne(tc TunnelConfig) error {
	if _, exists := m.tunnels[tc.Name]; exists {
		return nil // already running
	}

	var runFn func(<-chan struct{}, *TunnelState) error
	switch tc.Mode {
	case "local":
		if len(tc.Hops) > 0 {
			runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runChain(tc, ctx, state) }
		} else {
			runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runLocal(tc, ctx, state) }
		}
	case "dynamic":
		runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runDynamic(tc, ctx, state) }
	case "remote":
		if tc.Server != "" {
			if tc.Connections > 1 {
				runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runRemoteClientMulti(tc, ctx, state) }
			} else {
				runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runRemoteClient(tc, ctx, state) }
			}
		} else {
			runFn = func(ctx <-chan struct{}, state *TunnelState) error { return m.runRemoteServer(tc, ctx, state) }
		}
	default:
		return fmt.Errorf("unknown mode %q", tc.Mode)
	}

	ctx := make(chan struct{})
	done := make(chan struct{})
	cancel := func() { close(ctx) }
	state := &TunnelState{}
	mt := &managedTunnel{cfg: tc, cancel: cancel, done: done, state: state}

	// Start health check if configured
	if tc.HealthCheck != "" {
		d, err := time.ParseDuration(tc.HealthCheck)
		if err == nil {
			stopHC := make(chan struct{})
			go relay.HealthCheck(tc.Local, d, 3, stopHC)
			go func() {
				<-ctx
				close(stopHC)
			}()
		}
	}

	go func() {
		defer close(done)
		log.Printf("manager: starting tunnel %s (%s)", tc.Name, tc.Mode)
		state.Set("starting", "")
		if err := runFn(ctx, state); err != nil {
			mt.errMu.Lock()
			mt.lastErr = err
			mt.errMu.Unlock()
			state.Set("error", err.Error())
			log.Printf("manager: %s: %v", tc.Name, err)
		} else {
			state.Set("stopped", "")
		}
	}()

	m.tunnels[tc.Name] = mt
	return nil
}

func (m *Manager) runLocal(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	if tc.Protocol == "udp" {
		if tc.TLS {
			return fmt.Errorf("tls is not supported with UDP")
		}
		proxy, err := relay.NewUDPProxy(tc.Local, tc.Remote)
		if err != nil {
			return fmt.Errorf("udp proxy failed: %w", err)
		}

		state.Set("listening", "")

		errCh := make(chan error, 1)
		go func() {
			errCh <- proxy.Serve()
		}()

		select {
		case <-stop:
			proxy.Close()
			return nil
		case err := <-errCh:
			return err
		}
	}

	var proxy *relay.Proxy
	var err error

	if tc.TLS {
		tlsCfg, cErr := relay.SetupTLS(tc.TLSCert, tc.TLSKey, tc.TLSVerify)
		if cErr != nil {
			return fmt.Errorf("tls: %w", cErr)
		}
		if tlsCfg.Fingerprint != "" {
			log.Printf("manager: %s TLS identity fingerprint: %s", tc.Name, tlsCfg.Fingerprint)
		}
		proxy, err = relay.NewTLSProxy(tc.Local, tc.Remote, tlsCfg)
	} else {
		proxy, err = relay.NewProxy(tc.Local, tc.Remote)
	}
	if err != nil {
		return fmt.Errorf("proxy failed: %w", err)
	}

	state.Set("listening", "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.Serve()
	}()

	select {
	case <-stop:
		proxy.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func (m *Manager) runChain(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	proxy, err := relay.NewChainProxy(tc.Local, tc.Hops)
	if err != nil {
		return fmt.Errorf("chain proxy failed: %w", err)
	}
	if proxy == nil {
		return fmt.Errorf("chain proxy nil")
	}

	state.Set("listening", "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.Serve()
	}()

	select {
	case <-stop:
		proxy.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func (m *Manager) runDynamic(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	var proxy *relay.SocksProxy
	var err error
	if tc.SocksUser != "" || tc.SocksPass != "" {
		proxy, err = relay.NewSocksProxyWithAuth(tc.Local, tc.SocksUser, tc.SocksPass)
	} else {
		proxy, err = relay.NewSocksProxy(tc.Local)
	}
	if err != nil {
		return fmt.Errorf("socks proxy failed: %w", err)
	}

	state.Set("listening", "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.Serve()
	}()

	select {
	case <-stop:
		proxy.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func (m *Manager) runRemoteClient(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	var tlsCfg *relay.TLSConfig
	if tc.TLS {
		var err error
		tlsCfg, err = relay.SetupTLS(tc.TLSCert, tc.TLSKey, tc.TLSVerify)
		if err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		tlsCfg.TrustOnFirstUse = tc.TLSTrustOnFirstUse
		tlsCfg.ServerFingerprint = tc.TLSServerFingerprint
		tlsCfg.KnownHosts = m.knownHosts
	}

	bind, port, target, err := parseRemoteSpec(tc.Remote)
	if err != nil {
		return fmt.Errorf("invalid remote spec: %w", err)
	}
	tunnels := []relay.RemoteTunnel{
		{RemotePort: port, BindAddr: bind, TargetAddr: target},
	}

	client := relay.NewRemoteClient(tc.Server, tc.Token, tlsCfg, tunnels)

	go func() {
		client.Run()
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	mt := m.tunnels[tc.Name]
	for {
		select {
		case <-stop:
			client.Close()
			return nil
		case <-ticker.C:
			if client.IsConnected() {
				state.Set("established", "")
			} else {
				state.Set("reconnecting", "")
			}
			state.SetReconnectCount(client.ReconnectCount())
			if err := client.LastError(); err != nil {
				mt.errMu.Lock()
				mt.lastErr = err
				mt.errMu.Unlock()
			}
		}
	}
}

func (m *Manager) runRemoteClientMulti(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	var tlsCfg *relay.TLSConfig
	if tc.TLS {
		var err error
		tlsCfg, err = relay.SetupTLS(tc.TLSCert, tc.TLSKey, tc.TLSVerify)
		if err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		tlsCfg.TrustOnFirstUse = tc.TLSTrustOnFirstUse
		tlsCfg.ServerFingerprint = tc.TLSServerFingerprint
		tlsCfg.KnownHosts = m.knownHosts
	}

	bind, port, target, err := parseRemoteSpec(tc.Remote)
	if err != nil {
		return fmt.Errorf("invalid remote spec: %w", err)
	}
	tunnel := relay.RemoteTunnel{RemotePort: port, BindAddr: bind, TargetAddr: target}

	n := tc.Connections
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}

	clients := make([]*relay.RemoteClient, n)
	for i := range n {
		clients[i] = relay.NewRemoteClient(tc.Server, tc.Token, tlsCfg, []relay.RemoteTunnel{tunnel})
		go clients[i].Run()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			for _, c := range clients {
				c.Close()
			}
			return nil
		case <-ticker.C:
			connected := 0
			var totalReconnects int64
			for _, c := range clients {
				if c.IsConnected() {
					connected++
				}
				totalReconnects += c.ReconnectCount()
			}
			if connected == n {
				state.Set("established", "")
			} else if connected > 0 {
				state.Set("degraded", "")
			} else {
				state.Set("reconnecting", "")
			}
			state.SetReconnectCount(totalReconnects)
		}
	}
}

func (m *Manager) runRemoteServer(tc TunnelConfig, stop <-chan struct{}, state *TunnelState) error {
	var tlsCfg *relay.TLSConfig
	if tc.TLS {
		var err error
		tlsCfg, err = relay.SetupTLS(tc.TLSCert, tc.TLSKey, tc.TLSVerify)
		if err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		if tlsCfg.Fingerprint != "" {
			log.Printf("manager: %s TLS identity fingerprint: %s", tc.Name, tlsCfg.Fingerprint)
		}
	}

	srv, err := relay.NewRemoteServer(tc.Local, tc.Token, tlsCfg)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}

	state.Set("listening", "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve()
	}()

	select {
	case <-stop:
		srv.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func parseRemoteSpec(spec string) (bind string, port uint16, target string, err error) {
	if spec == "" {
		return "", 0, "", fmt.Errorf("empty remote spec")
	}
	if strings.ContainsAny(spec, "[]") {
		return "", 0, "", fmt.Errorf("IPv6 literals are not supported: %q", spec)
	}

	hostPortSep := strings.LastIndexByte(spec, ':')
	if hostPortSep < 0 {
		return "", 0, "", fmt.Errorf("invalid remote spec %q (expected [bind:]port:host:hostport)", spec)
	}
	hostPortStr := spec[hostPortSep+1:]
	mid := spec[:hostPortSep]

	portHostSep := strings.LastIndexByte(mid, ':')
	if portHostSep < 0 {
		return "", 0, "", fmt.Errorf("invalid remote spec %q (expected [bind:]port:host:hostport)", spec)
	}
	host := mid[portHostSep+1:]
	front := mid[:portHostSep]

	if host == "" {
		return "", 0, "", fmt.Errorf("invalid remote spec %q: target host is empty", spec)
	}

	// front is either "remotePort" or "bind:remotePort"
	bindSep := strings.LastIndexByte(front, ':')
	var portStr string
	if bindSep >= 0 {
		bind = front[:bindSep]
		portStr = front[bindSep+1:]
	} else {
		portStr = front
	}

	portU, perr := strconv.ParseUint(portStr, 10, 16)
	if perr != nil {
		return "", 0, "", fmt.Errorf("invalid remote port %q: %w", portStr, perr)
	}
	hostPort, perr := strconv.ParseUint(hostPortStr, 10, 16)
	if perr != nil {
		return "", 0, "", fmt.Errorf("invalid target port %q: %w", hostPortStr, perr)
	}

	if bind == "" {
		bind = "0.0.0.0"
	}

	return bind, uint16(portU), net.JoinHostPort(host, strconv.Itoa(int(hostPort))), nil
}

func (m *Manager) HandleControl(req Request) Response {
	var empty = json.RawMessage("null")

	switch req.Method {
	case "list":
		statuses := m.List()
		data, err := json.Marshal(statuses)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: data, ID: req.ID}

	case "stop":
		err := m.Stop()
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "stop-group":
		var params struct {
			Group string `json:"group"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		m.StopGroup(params.Group)
		return Response{Result: empty, ID: req.ID}

	case "start-group":
		var params struct {
			Group string `json:"group"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		m.StartGroup(params.Group)
		return Response{Result: empty, ID: req.ID}

	case "add":
		var tc TunnelConfig
		if err := json.Unmarshal(req.Params, &tc); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		if err := m.Add(tc); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "remove":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		if err := m.Remove(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "enable":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		if err := m.Enable(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "disable":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		if err := m.Disable(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "restart":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		if err := m.Restart(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "save":
		if err := m.Save(); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	case "status":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		st, err := m.Status(params.Name)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		data, err := json.Marshal(st)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: data, ID: req.ID}

	case "logs":
		var params struct {
			Name  string `json:"name"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: fmt.Sprintf("invalid params: %v", err), ID: req.ID}
		}
		lines, err := m.Logs(params.Name, params.Lines)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		data, err := json.Marshal(lines)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: data, ID: req.ID}

	case "reload":
		if err := m.Reload(); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: empty, ID: req.ID}

	default:
		return Response{Error: fmt.Sprintf("unknown method %q", req.Method), ID: req.ID}
	}
}

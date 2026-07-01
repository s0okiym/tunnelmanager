package manager

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"tunnel/relay"
)

type Manager struct {
	mu       sync.Mutex
	cfg      *Config
	cfgPath  string
	tunnels  map[string]*managedTunnel
	stopCh   chan struct{}
}

type managedTunnel struct {
	cfg    TunnelConfig
	cancel func()
}

func NewManager(cfg *Config, cfgPath string) *Manager {
	return &Manager{
		cfg:     cfg,
		cfgPath: cfgPath,
		tunnels: make(map[string]*managedTunnel),
		stopCh:  make(chan struct{}),
	}
}

func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range m.cfg.Tunnels {
		if tc.Autostart {
			if err := m.startLocked(tc); err != nil {
				log.Printf("manager: failed to start %s: %v", tc.Name, err)
			}
		}
	}
	return nil
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, mt := range m.tunnels {
		mt.cancel()
		delete(m.tunnels, name)
		log.Printf("manager: stopped %s", name)
	}
	return nil
}

func (m *Manager) List() []TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make([]TunnelStatus, 0, len(m.cfg.Tunnels))
	for _, tc := range m.cfg.Tunnels {
		_, running := m.tunnels[tc.Name]
		st := TunnelStatus{
			Name:   tc.Name,
			Mode:   tc.Mode,
			Local:  tc.Local,
			Remote: tc.Remote,
		}
		if running {
			st.Status = "running"
		} else {
			st.Status = "stopped"
		}
		statuses = append(statuses, st)
	}
	return statuses
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
		return m.startLocked(tc)
	}
	return nil
}

func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if mt, ok := m.tunnels[name]; ok {
		mt.cancel()
		delete(m.tunnels, name)
	}

	idx := m.findConfig(name)
	if idx < 0 {
		return fmt.Errorf("tunnel %q not found", name)
	}
	m.cfg.Tunnels = append(m.cfg.Tunnels[:idx], m.cfg.Tunnels[idx+1:]...)
	return SaveConfig(m.cfg, m.cfgPath)
}

func (m *Manager) startLocked(tc TunnelConfig) error {
	ctx := make(chan struct{})

	switch tc.Mode {
	case "local":
		go m.runLocal(tc, ctx)
	case "dynamic":
		go m.runDynamic(tc, ctx)
	case "remote":
		if tc.Server != "" {
			go m.runRemoteClient(tc, ctx)
		} else {
			go m.runRemoteServer(tc, ctx)
		}
	default:
		return fmt.Errorf("unknown mode %q", tc.Mode)
	}

	m.tunnels[tc.Name] = &managedTunnel{cfg: tc, cancel: func() { close(ctx) }}
	return nil
}

func (m *Manager) runLocal(tc TunnelConfig, stop <-chan struct{}) {
	proxy, err := relay.NewProxy(tc.Local, tc.Remote)
	if err != nil {
		log.Printf("manager: %s: proxy failed: %v", tc.Name, err)
		return
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.Serve()
	}()

	select {
	case <-stop:
		proxy.Close()
	case <-errCh:
	}
}

func (m *Manager) runDynamic(tc TunnelConfig, stop <-chan struct{}) {
	proxy, err := relay.NewSocksProxy(tc.Local)
	if err != nil {
		log.Printf("manager: %s: socks proxy failed: %v", tc.Name, err)
		return
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.Serve()
	}()

	select {
	case <-stop:
		proxy.Close()
	case <-errCh:
	}
}

func (m *Manager) runRemoteClient(tc TunnelConfig, stop <-chan struct{}) {
	var tlsCfg *relay.TLSConfig
	if tc.TLS {
		cert, err := relay.GenerateCert()
		if err != nil {
			log.Printf("manager: %s: tls cert: %v", tc.Name, err)
			return
		}
		tlsCfg = &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}
	}

	// parse Remote spec: "9090:localhost:8080"
	port, target := parseRemoteSpec(tc.Remote)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: port, TargetAddr: target},
	}

	client := relay.NewRemoteClient(tc.Server, tc.Token, tlsCfg, tunnels)

	go func() {
		client.Run()
	}()

	<-stop
	client.Close()
}

func (m *Manager) runRemoteServer(tc TunnelConfig, stop <-chan struct{}) {
	var tlsCfg *relay.TLSConfig
	if tc.TLS {
		cert, err := relay.GenerateCert()
		if err != nil {
			log.Printf("manager: %s: tls cert: %v", tc.Name, err)
			return
		}
		tlsCfg = &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}
	}

	srv, err := relay.NewRemoteServer(tc.Local, tc.Token, tlsCfg)
	if err != nil {
		log.Printf("manager: %s: server: %v", tc.Name, err)
		return
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve()
	}()

	select {
	case <-stop:
		srv.Close()
	case <-errCh:
	}
}

func (m *Manager) findConfig(name string) int {
	for i, tc := range m.cfg.Tunnels {
		if tc.Name == name {
			return i
		}
	}
	return -1
}

// parseRemoteSpec extracts port and target from "port:host:hostport" format.
func parseRemoteSpec(spec string) (uint16, string) {
	// Format: port:host:hostport
	hostPort := ""
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			if hostPort == "" {
				hostPort = spec[i+1:]
				spec = spec[:i]
			} else {
				target := spec[i+1:] + ":" + hostPort
				spec = spec[:i]
				// parse port
				var port uint16
				for _, c := range spec {
					port = port*10 + uint16(c-'0')
				}
				return port, target
			}
		}
	}
	return 0, spec + ":" + hostPort
}

// HandleControl processes a control request.
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

	default:
		return Response{Error: fmt.Sprintf("unknown method %q", req.Method), ID: req.ID}
	}
}

package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tunnel/relay"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Tunnels) != 0 {
		t.Fatalf("expected no tunnels by default, got %d", len(cfg.Tunnels))
	}
}

func TestSaveLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Tunnels = append(cfg.Tunnels, TunnelConfig{
		Name:      "test",
		Mode:      "local",
		Local:     "127.0.0.1:8080",
		Remote:    "127.0.0.1:80",
		Autostart: true,
	})

	if err := SaveConfig(&cfg, path); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(loaded.Tunnels))
	}
	if loaded.Tunnels[0].Name != "test" {
		t.Fatalf("expected name test, got %s", loaded.Tunnels[0].Name)
	}
	if loaded.Tunnels[0].Mode != "local" {
		t.Fatalf("expected mode local, got %s", loaded.Tunnels[0].Mode)
	}
}

func TestLoadConfigNotExists(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected default config")
	}
}

func TestManagerAddRemoveList(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	cfg := DefaultConfig()
	mgr := NewManager(&cfg, filepath.Join(dir, "config.yaml"))

	// Add
	tc := TunnelConfig{
		Name:      "test-tunnel",
		Mode:      "local",
		Local:     "127.0.0.1:9999",
		Remote:    "127.0.0.1:80",
		Autostart: true,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	// Auto-start for port 9999 should fail (no one listening), but that's fine
	// List
	statuses := mgr.List()
	if len(statuses) != 1 {
		t.Fatalf("expected 1, got %d", len(statuses))
	}
	if statuses[0].Name != "test-tunnel" {
		t.Fatalf("expected test-tunnel, got %s", statuses[0].Name)
	}

	// Remove
	if err := mgr.Remove("test-tunnel"); err != nil {
		t.Fatal(err)
	}

	statuses = mgr.List()
	if len(statuses) != 0 {
		t.Fatalf("expected 0, got %d", len(statuses))
	}
}

func TestManagerAddDuplicate(t *testing.T) {
	cfg := DefaultConfig()
	mgr := NewManager(&cfg, "")

	tc := TunnelConfig{Name: "dup", Mode: "local"}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Add(tc); err == nil {
		t.Fatal("expected error for duplicate")
	}
}

func TestManagerRemoveNonexistent(t *testing.T) {
	cfg := DefaultConfig()
	mgr := NewManager(&cfg, "")
	if err := mgr.Remove("nonexistent"); err == nil {
		t.Fatal("expected error")
	}
}

func TestManagerListShowsError(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	// Start a tunnel on an invalid bind address so it fails asynchronously.
	tc := TunnelConfig{
		Name:      "bad-addr",
		Mode:      "local",
		Local:     "256.256.256.256:1234",
		Remote:    "127.0.0.1:80",
		Autostart: true,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	// Wait for the goroutine to fail and record the error.
	for i := 0; i < 50; i++ {
		statuses := mgr.List()
		if len(statuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(statuses))
		}
		if statuses[0].Status == "error" {
			if statuses[0].Error == "" {
				t.Fatal("expected non-empty error message")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tunnel did not transition to error status")
}
func TestHandleControl(t *testing.T) {
	cfg := DefaultConfig()
	mgr := NewManager(&cfg, "")

	// Add via control
	params := `{"name":"ctrl-test","mode":"local","local":":0","remote":"127.0.0.1:1"}`
	resp := mgr.HandleControl(managerRequest("add", params))
	if resp.Error != "" {
		t.Fatalf("add error: %s", resp.Error)
	}

	// List via control
	resp = mgr.HandleControl(managerRequest("list", ""))
	if resp.Error != "" {
		t.Fatalf("list error: %s", resp.Error)
	}
	if len(resp.Result) == 0 {
		t.Fatal("expected non-empty result")
	}
}

func managerRequest(method string, params string) Request {
	return Request{
		Method: method,
		Params: []byte(params),
		ID:     1,
	}
}

func TestPidfile(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	if IsRunning() {
		t.Fatal("should not be running")
	}

	if err := WritePidfile(); err != nil {
		t.Fatal(err)
	}

	pid, err := ReadPidfile()
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("expected %d, got %d", os.Getpid(), pid)
	}

	if err := RemovePidfile(); err != nil {
		t.Fatal(err)
	}
}

func findFreePortManager(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	return port
}

func startEchoLoopManager(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(c)
		}
	}()
	return ln
}

func echoThroughManager(addr string, payload []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	errCh := make(chan error, 1)
	go func() {
		_, werr := conn.Write(payload)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		} else {
			conn.Close()
		}
		errCh <- werr
	}()
	got, rerr := io.ReadAll(conn)
	werr := <-errCh
	if rerr != nil {
		return nil, rerr
	}
	if werr != nil {
		return nil, werr
	}
	return got, nil
}

func waitEchoManager(addr string, payload []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := echoThroughManager(addr, payload)
		if err == nil {
			return got, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, lastErr
}

// TestRunRemoteClientMulti proves that a remote tunnel with connections > 1
// starts multiple control connections that share a single public listener on
// the server, instead of failing with "address already in use".
func TestRunRemoteClientMulti(t *testing.T) {
	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePortManager(t)
	targetPort := echoLn.Addr().(*net.TCPAddr).Port

	m := NewManager(&Config{}, "")
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.runRemoteClientMulti(TunnelConfig{
			Name:        "multi",
			Mode:        "remote",
			Server:      srv.Addr().String(),
			Remote:      fmt.Sprintf("%d:127.0.0.1:%d", remotePort, targetPort),
			Connections: 2,
		}, stop, &TunnelState{})
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	for i := 0; i < 6; i++ {
		got, err := waitEchoManager(addr, payload, 5*time.Second)
		if err != nil {
			t.Fatalf("connection %d failed: %v", i+1, err)
		}
		if len(got) != len(payload) {
			t.Fatalf("connection %d: unexpected echo length %d", i+1, len(got))
		}
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runRemoteClientMulti did not stop")
	}
}

func TestParseRemoteSpecManager(t *testing.T) {
	bind, port, target, err := parseRemoteSpec("9090:localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if bind != "0.0.0.0" {
		t.Fatalf("expected default bind 0.0.0.0, got %s", bind)
	}
	if port != 9090 {
		t.Fatalf("port=%d", port)
	}
	if target != "localhost:8080" {
		t.Fatalf("target=%s", target)
	}
}

func TestParseRemoteSpecManagerWithBind(t *testing.T) {
	bind, port, target, err := parseRemoteSpec("127.0.0.1:9090:localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if bind != "127.0.0.1" {
		t.Fatalf("bind=%s", bind)
	}
	if port != 9090 || target != "localhost:8080" {
		t.Fatalf("port=%d target=%s", port, target)
	}
}

func TestManagerListShowsListening(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	tc := TunnelConfig{
		Name:      "local-web",
		Mode:      "local",
		Local:     "127.0.0.1:0",
		Remote:    echoLn.Addr().String(),
		Autostart: true,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		statuses := mgr.List()
		if len(statuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(statuses))
		}
		if statuses[0].Status == "listening" {
			if statuses[0].Since.IsZero() {
				t.Fatal("expected non-zero since timestamp")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tunnel did not transition to listening status")
}

func TestManagerListShowsEstablished(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePortManager(t)
	targetPort := echoLn.Addr().(*net.TCPAddr).Port

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	tc := TunnelConfig{
		Name:      "remote-client",
		Mode:      "remote",
		Server:    srv.Addr().String(),
		Remote:    fmt.Sprintf("%d:127.0.0.1:%d", remotePort, targetPort),
		Autostart: true,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		statuses := mgr.List()
		if len(statuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(statuses))
		}
		if statuses[0].Status == "established" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote client did not transition to established status")
}

func TestManagerListShowsReconnectCount(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()

	remotePort := findFreePortManager(t)

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	tc := TunnelConfig{
		Name:      "remote-rc",
		Mode:      "remote",
		Server:    srv.Addr().String(),
		Remote:    fmt.Sprintf("%d:127.0.0.1:1", remotePort),
		Autostart: true,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	// Wait for the control connection to come up.
	for i := 0; i < 50; i++ {
		statuses := mgr.List()
		if len(statuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(statuses))
		}
		if statuses[0].Status == "established" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Drop the server so the client reconnects.
	srv.Close()

	// Wait for at least one reconnect attempt.
	for i := 0; i < 80; i++ {
		statuses := mgr.List()
		if len(statuses) != 1 {
			t.Fatalf("expected 1 status, got %d", len(statuses))
		}
		if statuses[0].ReconnectCount > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("reconnect count did not increase")
}

func TestManagerEnableDisable(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	port := findFreePortManager(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	tc := TunnelConfig{
		Name:      "web",
		Mode:      "local",
		Local:     addr,
		Remote:    echoLn.Addr().String(),
		Autostart: false,
	}
	if err := mgr.Add(tc); err != nil {
		t.Fatal(err)
	}

	statuses := mgr.List()
	if len(statuses) != 1 || statuses[0].Status != "stopped" {
		t.Fatalf("expected stopped, got %+v", statuses)
	}

	if err := mgr.Enable("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := waitEchoManager(addr, []byte("hello"), 2*time.Second); err != nil {
		t.Fatalf("enable did not start tunnel: %v", err)
	}
	if !mgr.cfg.Tunnels[0].Autostart {
		t.Fatal("expected autostart=true after enable")
	}

	if err := mgr.Disable("web"); err != nil {
		t.Fatal(err)
	}
	statuses = mgr.List()
	if len(statuses) != 1 || statuses[0].Status != "stopped" {
		t.Fatalf("expected stopped after disable, got %+v", statuses)
	}
	if mgr.cfg.Tunnels[0].Autostart {
		t.Fatal("expected autostart=false after disable")
	}
	if _, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
		t.Fatal("listener still open after disable")
	}
}

func TestManagerRestart(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	port := findFreePortManager(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := mgr.Add(TunnelConfig{
		Name:      "web",
		Mode:      "local",
		Local:     addr,
		Remote:    echoLn.Addr().String(),
		Autostart: true,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := waitEchoManager(addr, []byte("before"), 2*time.Second); err != nil {
		t.Fatalf("tunnel did not start: %v", err)
	}

	if err := mgr.Restart("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := waitEchoManager(addr, []byte("after"), 2*time.Second); err != nil {
		t.Fatalf("tunnel did not restart: %v", err)
	}

	statuses := mgr.List()
	if len(statuses) != 1 || (statuses[0].Status != "listening" && statuses[0].Status != "running") {
		t.Fatalf("expected running status after restart, got %+v", statuses)
	}
}

func TestManagerSave(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := DefaultConfig()
	mgr := NewManager(&cfg, cfgPath)

	if err := mgr.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 0 {
		t.Fatalf("expected empty tunnels, got %d", len(loaded.Tunnels))
	}

	if err := mgr.Add(TunnelConfig{
		Name:      "web",
		Mode:      "local",
		Local:     "127.0.0.1:8080",
		Remote:    "127.0.0.1:80",
		Autostart: true,
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err = LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Name != "web" {
		t.Fatalf("expected saved tunnel web, got %+v", loaded.Tunnels)
	}
}

func TestHandleControlEnableDisableRestartSave(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	echoLn := startEchoLoopManager(t)
	defer echoLn.Close()

	cfg := DefaultConfig()
	cfgPath := filepath.Join(dir, "config.yaml")
	mgr := NewManager(&cfg, cfgPath)

	port := findFreePortManager(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	params := fmt.Sprintf(`{"name":"web","mode":"local","local":%q,"remote":%q,"autostart":false}`, addr, echoLn.Addr().String())
	resp := mgr.HandleControl(managerRequest("add", params))
	if resp.Error != "" {
		t.Fatalf("add failed: %s", resp.Error)
	}

	resp = mgr.HandleControl(managerRequest("enable", `{"name":"web"}`))
	if resp.Error != "" {
		t.Fatalf("enable failed: %s", resp.Error)
	}
	if _, err := waitEchoManager(addr, []byte("enabled"), 2*time.Second); err != nil {
		t.Fatalf("enable did not start tunnel: %v", err)
	}

	resp = mgr.HandleControl(managerRequest("restart", `{"name":"web"}`))
	if resp.Error != "" {
		t.Fatalf("restart failed: %s", resp.Error)
	}
	if _, err := waitEchoManager(addr, []byte("restarted"), 2*time.Second); err != nil {
		t.Fatalf("restart did not bring tunnel back: %v", err)
	}

	resp = mgr.HandleControl(managerRequest("save", ""))
	if resp.Error != "" {
		t.Fatalf("save failed: %s", resp.Error)
	}
	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Name != "web" {
		t.Fatalf("save did not persist tunnel, got %+v", loaded.Tunnels)
	}

	resp = mgr.HandleControl(managerRequest("disable", `{"name":"web"}`))
	if resp.Error != "" {
		t.Fatalf("disable failed: %s", resp.Error)
	}
	statuses := mgr.List()
	if len(statuses) != 1 || statuses[0].Status != "stopped" {
		t.Fatalf("expected stopped after disable, got %+v", statuses)
	}
}

func TestManagerStatus(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tunnels = append(cfg.Tunnels, TunnelConfig{
		Name:   "web",
		Mode:   "local",
		Local:  "127.0.0.1:0",
		Remote: "127.0.0.1:1",
	})
	mgr := NewManager(&cfg, "")

	st, err := mgr.Status("web")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Name != "web" || st.Status != "stopped" {
		t.Fatalf("unexpected status: %+v", st)
	}

	if _, err := mgr.Status("missing"); err == nil {
		t.Fatal("expected error for unknown tunnel")
	}
}

func TestHandleControlStatusLogs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tunnels = append(cfg.Tunnels, TunnelConfig{
		Name:   "web",
		Mode:   "local",
		Local:  "127.0.0.1:0",
		Remote: "127.0.0.1:1",
	})
	mgr := NewManager(&cfg, "")

	GlobalLogBuffer().Add("manager: web: started")
	GlobalLogBuffer().Add("manager: other: started")

	resp := mgr.HandleControl(managerRequest("status", `{"name":"web"}`))
	if resp.Error != "" {
		t.Fatalf("status error: %s", resp.Error)
	}
	var st TunnelStatus
	if err := json.Unmarshal(resp.Result, &st); err != nil {
		t.Fatal(err)
	}
	if st.Name != "web" || st.Status != "stopped" {
		t.Fatalf("unexpected status: %+v", st)
	}

	resp = mgr.HandleControl(managerRequest("logs", `{"name":"web","lines":10}`))
	if resp.Error != "" {
		t.Fatalf("logs error: %s", resp.Error)
	}
	var lines []string
	if err := json.Unmarshal(resp.Result, &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "web") {
		t.Fatalf("expected one web log line, got %v", lines)
	}
}

func TestLogBufferFilteringAndLimit(t *testing.T) {
	lb := NewLogBuffer(10)
	for i := 1; i <= 5; i++ {
		lb.Add(fmt.Sprintf("tunnel web line %d", i))
		lb.Add(fmt.Sprintf("tunnel db line %d", i))
	}

	web := lb.Lines("web", 2)
	if len(web) != 2 {
		t.Fatalf("expected 2 web lines, got %d", len(web))
	}
	if !strings.Contains(web[0], "line 4") || !strings.Contains(web[1], "line 5") {
		t.Fatalf("expected oldest-first recent lines, got %v", web)
	}

	all := lb.Lines("", 10)
	if len(all) != 10 {
		t.Fatalf("expected 10 total lines, got %d", len(all))
	}
}

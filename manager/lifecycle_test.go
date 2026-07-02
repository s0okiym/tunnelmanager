package manager

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func startEchoServer(t *testing.T) net.Listener {
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
			go func(c net.Conn) { io.Copy(c, c); c.Close() }(c)
		}
	}()
	return ln
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func echoOnce(addr string, payload []byte) ([]byte, error) {
	c, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	errc := make(chan error, 1)
	go func() {
		_, e := c.Write(payload)
		if tc, ok := c.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		errc <- e
	}()
	got, rerr := io.ReadAll(c)
	<-errc
	if rerr != nil {
		return nil, rerr
	}
	return got, nil
}

func waitEchoLocal(addr string, payload []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		got, err := echoOnce(addr, payload)
		if err == nil && string(got) == string(payload) {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("echo mismatch: got %d bytes", len(got))
		}
		time.Sleep(30 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

// TestManagerReloadRebindsPort verifies that reloading the daemon config tears
// down each tunnel fully (releasing its listener) before restarting it, so the
// local port is reliably reusable across reloads (no "address already in use").
func TestManagerReloadRebindsPort(t *testing.T) {
	echoLn := startEchoServer(t)
	defer echoLn.Close()

	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	localPort := freeTCPPort(t)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Tunnels = []TunnelConfig{{
		Name:      "l",
		Mode:      "local",
		Local:     localAddr,
		Remote:    echoLn.Addr().String(),
		Autostart: true,
	}}
	if err := SaveConfig(&cfg, cfgPath); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(&cfg, cfgPath)
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	payload := []byte("hello-reload")
	if err := waitEchoLocal(localAddr, payload, 3*time.Second); err != nil {
		t.Fatalf("initial: %v", err)
	}

	for i := 0; i < 6; i++ {
		resp := mgr.HandleControl(managerRequest("reload", ""))
		if resp.Error != "" {
			t.Fatalf("reload %d error: %s", i, resp.Error)
		}
		if err := waitEchoLocal(localAddr, payload, 3*time.Second); err != nil {
			t.Fatalf("after reload %d: local port not served: %v", i, err)
		}
	}
}

// TestManagerStopReleasesPortImmediately verifies that Stop() does not return
// until each tunnel has fully torn down and released its listener. Without the
// teardown wait, Stop() returns while proxy.Close() is still pending and the
// port is briefly still bound.
func TestManagerStopReleasesPortImmediately(t *testing.T) {
	echoLn := startEchoServer(t)
	defer echoLn.Close()

	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	localPort := freeTCPPort(t)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)

	cfg := DefaultConfig()
	cfg.Tunnels = []TunnelConfig{{
		Name:      "l",
		Mode:      "local",
		Local:     localAddr,
		Remote:    echoLn.Addr().String(),
		Autostart: true,
	}}
	mgr := NewManager(&cfg, filepath.Join(dir, "config.yaml"))
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitEchoLocal(localAddr, []byte("x"), 3*time.Second); err != nil {
		t.Fatalf("initial: %v", err)
	}

	mgr.Stop()

	// The port must be immediately rebindable now that Stop() has returned.
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		t.Fatalf("port not released synchronously after Stop(): %v", err)
	}
	ln.Close()
}

// TestManagerReloadConcurrent runs reload concurrently with list; under -race
// this catches the unlocked shared-state mutation in the reload path.
func TestManagerReloadConcurrent(t *testing.T) {
	dir := t.TempDir()
	origDataDir := DefaultDataDir
	DefaultDataDir = dir
	defer func() { DefaultDataDir = origDataDir }()

	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := DefaultConfig()
	cfg.Tunnels = []TunnelConfig{
		{Name: "a", Mode: "local", Local: "127.0.0.1:0", Remote: "127.0.0.1:1"},
		{Name: "b", Mode: "local", Local: "127.0.0.1:0", Remote: "127.0.0.1:1"},
	}
	if err := SaveConfig(&cfg, cfgPath); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(&cfg, cfgPath)
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				mgr.HandleControl(managerRequest("reload", ""))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				mgr.HandleControl(managerRequest("list", ""))
			}
		}()
	}
	wg.Wait()
}

package manager

import (
	"os"
	"path/filepath"
	"testing"
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

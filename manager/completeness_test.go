package manager

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadConfigFromEnv verifies LoadConfig("") honors the TUNNEL_CONFIG env
// var, matching the path advertised in the generated systemd unit.
func TestLoadConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "myconfig.yaml")
	cfg := DefaultConfig()
	cfg.Tunnels = []TunnelConfig{{Name: "envtunnel", Mode: "local", Local: "127.0.0.1:1", Remote: "127.0.0.1:2"}}
	if err := SaveConfig(&cfg, path); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TUNNEL_CONFIG", path)
	loaded, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tunnels) != 1 || loaded.Tunnels[0].Name != "envtunnel" {
		t.Fatalf("LoadConfig did not honor TUNNEL_CONFIG: %+v", loaded.Tunnels)
	}
}

// TestWriteSystemdUnitUserCreatesDir verifies the user unit directory is created
// and that the function reports success only when the file was actually written.
func TestWriteSystemdUnitUserCreatesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := WriteSystemdUnitUser(""); err != nil {
		t.Fatalf("WriteSystemdUnitUser: %v", err)
	}
	p := filepath.Join(home, ".config", "systemd", "user", "tunnel.service")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("unit file not written: %v", err)
	}
}

// TestControlSocketOverride verifies a configured control socket path is honored.
func TestControlSocketOverride(t *testing.T) {
	orig := controlSocketOverride
	defer func() { controlSocketOverride = orig }()

	SetControlSocketPath("/tmp/custom-tunnel.sock")
	if got := ControlSocketPath(); got != "/tmp/custom-tunnel.sock" {
		t.Fatalf("override not honored: %s", got)
	}
	SetControlSocketPath("")
	if got := ControlSocketPath(); got == "/tmp/custom-tunnel.sock" {
		t.Fatalf("override not cleared: %s", got)
	}
}

// TestSetupLogFile verifies the daemon can redirect its log output to a file.
func TestSetupLogFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tunnel.log")

	f, err := SetupLogFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		log.SetOutput(os.Stderr)
		f.Close()
	}()

	log.Print("hello-logfile")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello-logfile") {
		t.Fatalf("log not written to file: %q", data)
	}
}

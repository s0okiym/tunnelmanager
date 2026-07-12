package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EDaemonRemoteConnections verifies that a daemon-configured remote
// tunnel with connections > 1 works end-to-end: the server creates a single
// shared listener for the remote port and traffic flows through the multiple
// control connections without "address already in use" errors.
func TestE2EDaemonRemoteConnections(t *testing.T) {
	bin := buildTunnelBinary(t)

	echoLn := startEchoLoop(t)
	defer echoLn.Close()

	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home)

	cfgDir := filepath.Join(home, ".config", "tunnel")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.yaml")

	remotePort := findFreePort(t)
	remoteAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	targetAddr := echoLn.Addr().String()

	cfg := fmt.Sprintf("tunnels:\n"+
		"  - name: remote-srv\n"+
		"    mode: remote\n"+
		"    local: 127.0.0.1:9000\n"+
		"    autostart: true\n"+
		"  - name: remote-multi\n"+
		"    mode: remote\n"+
		"    server: 127.0.0.1:9000\n"+
		"    remote: %d:%s\n"+
		"    connections: 2\n"+
		"    autostart: true\n", remotePort, targetAddr)

	if err := os.WriteFile(cfgPath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(home, "daemon.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	daemon := exec.Command(bin, "start", "--foreground")
	daemon.Env = env
	daemon.Stdout = logFile
	daemon.Stderr = logFile
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}

	exited := make(chan struct{})
	go func() { daemon.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
		default:
			daemon.Process.Kill()
		}
		if t.Failed() {
			if data, err := os.ReadFile(logPath); err == nil {
				t.Logf("daemon log:\n%s", data)
			}
		}
	}()

	sock := filepath.Join(home, ".local", "share", "tunnel", "control.sock")
	waitFile(t, sock, 10*time.Second)

	out, err := runCLI(t, bin, env, "ls")
	if err != nil {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	if !strings.Contains(out, "remote-srv") || !strings.Contains(out, "remote-multi") {
		t.Fatalf("ls did not list tunnels:\n%s", out)
	}

	for i := 0; i < 6; i++ {
		if _, err := waitEcho(remoteAddr, randomBytes(256), 5*time.Second); err != nil {
			t.Fatalf("connection %d failed: %v", i+1, err)
		}
	}

	if out, err := runCLI(t, bin, env, "stop"); err != nil {
		t.Fatalf("stop command failed: %v\n%s", err, out)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after `tunnel stop`")
	}
}

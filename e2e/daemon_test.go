package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func buildTunnelBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tunneld-bin")
	cmd := exec.Command("go", "build", "-o", bin, "tunnel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build tunnel binary: %v\n%s", err, out)
	}
	return bin
}

func runCLI(t *testing.T, bin string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
}

// TestE2EDaemonLifecycle drives the real compiled binary through the full
// daemon lifecycle over a Unix control socket: start -> ls -> forward traffic
// -> SIGHUP reload -> stop. It covers the CLI/daemon path and specifically
// verifies:
//   - SIGHUP reload is reflected in `ls` (control server talks to the reloaded
//     manager, not a stale one);
//   - `tunnel stop` actually terminates the daemon process.
func TestE2EDaemonLifecycle(t *testing.T) {
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

	localPort := findFreePort(t)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	cfg1 := fmt.Sprintf("tunnels:\n"+
		"  - name: web\n"+
		"    mode: local\n"+
		"    local: %s\n"+
		"    remote: %s\n"+
		"    autostart: true\n", localAddr, echoLn.Addr().String())
	if err := os.WriteFile(cfgPath, []byte(cfg1), 0644); err != nil {
		t.Fatal(err)
	}

	// Capture daemon output for debugging on failure.
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

	// `ls` shows the autostarted tunnel.
	out, err := runCLI(t, bin, env, "ls")
	if err != nil {
		t.Fatalf("ls: %v\n%s", err, out)
	}
	if !strings.Contains(out, "web") {
		t.Fatalf("ls did not list 'web':\n%s", out)
	}

	// Traffic flows through the local forward.
	if _, err := waitEcho(localAddr, randomBytes(256), 3*time.Second); err != nil {
		t.Fatalf("local forward not working: %v", err)
	}

	// SIGHUP reload: add a second tunnel and reload.
	local2 := findFreePort(t)
	local2Addr := fmt.Sprintf("127.0.0.1:%d", local2)
	cfg2 := cfg1 + fmt.Sprintf(
		"  - name: web2\n"+
			"    mode: local\n"+
			"    local: %s\n"+
			"    remote: %s\n"+
			"    autostart: true\n", local2Addr, echoLn.Addr().String())
	if err := os.WriteFile(cfgPath, []byte(cfg2), 0644); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}

	// After reload, `ls` (which hits the control server) must reflect the new
	// config — this fails if SIGHUP orphaned the control server on a stale
	// manager.
	reloaded := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ = runCLI(t, bin, env, "ls")
		if strings.Contains(out, "web2") {
			reloaded = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !reloaded {
		t.Fatalf("SIGHUP reload not reflected via control socket:\n%s", out)
	}
	if _, err := waitEcho(local2Addr, randomBytes(256), 3*time.Second); err != nil {
		t.Fatalf("reloaded tunnel not serving: %v", err)
	}

	// `tunnel stop` must terminate the daemon process.
	if out, err := runCLI(t, bin, env, "stop"); err != nil {
		t.Fatalf("stop command failed: %v\n%s", err, out)
	}
	select {
	case <-exited:
		// daemon exited on its own — good
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit after `tunnel stop`")
	}
}

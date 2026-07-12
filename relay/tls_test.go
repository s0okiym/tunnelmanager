package relay

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startTestTLSServer starts a TLS listener using a persistent identity in dir
// and runs a one-shot echo handler in a goroutine. It returns the listener and
// the TLSConfig used so the caller knows the server fingerprint.
func startTestTLSServer(t *testing.T, dir string) (net.Listener, *TLSConfig) {
	t.Helper()
	DefaultIdentityDir = dir
	t.Cleanup(func() { DefaultIdentityDir = "" })

	srvCfg, err := SetupTLS("", "", false)
	if err != nil {
		t.Fatalf("setup server tls: %v", err)
	}
	ln, err := TLSListener("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("tls listener: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln, srvCfg
}

func TestTLSDialPinnedFingerprint(t *testing.T) {
	dir := t.TempDir()
	ln, srvCfg := startTestTLSServer(t, dir)
	defer ln.Close()

	addr := ln.Addr().String()
	conn, err := TLSDial(addr, &TLSConfig{
		Enabled:           true,
		ServerFingerprint: srvCfg.Fingerprint,
	})
	if err != nil {
		t.Fatalf("dial with correct fingerprint: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("unexpected echo %q", buf)
	}
}

func TestTLSDialPinnedFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	ln, _ := startTestTLSServer(t, dir)
	defer ln.Close()

	addr := ln.Addr().String()
	_, err := TLSDial(addr, &TLSConfig{
		Enabled:           true,
		ServerFingerprint: "SHA256:BBBBBBBBBBBB",
	})
	if err == nil {
		t.Fatal("expected fingerprint mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch in error, got %v", err)
	}
}

func TestTLSDialTrustOnFirstUse(t *testing.T) {
	dir := t.TempDir()
	ln, srvCfg := startTestTLSServer(t, dir)
	defer ln.Close()

	khPath := filepath.Join(t.TempDir(), "known_hosts")
	kh, err := LoadKnownHosts(khPath)
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr().String()
	cfg := &TLSConfig{
		Enabled:         true,
		TrustOnFirstUse: true,
		KnownHosts:      kh,
	}
	conn, err := TLSDial(addr, cfg)
	if err != nil {
		t.Fatalf("first TOFU dial: %v", err)
	}
	conn.Close()

	// Verify the host was recorded.
	if err := kh.Verify(addr, srvCfg.Fingerprint); err != nil {
		t.Fatalf("host not recorded: %v", err)
	}

	// Tamper with the recorded entry and ensure a subsequent dial is rejected.
	if err := os.WriteFile(khPath, []byte(fmt.Sprintf("%s SHA256:BBBBBBBBBBBB\n", addr)), 0600); err != nil {
		t.Fatal(err)
	}
	kh2, err := LoadKnownHosts(khPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KnownHosts = kh2
	_, err = TLSDial(addr, cfg)
	if err == nil {
		t.Fatal("expected TOFU mismatch after tampering")
	}
}

// TestTLSDialWithoutVerificationWorks confirms that the existing insecure TLS
// dial path (no --tls-verify, no fingerprint) still works.
func TestTLSDialWithoutVerificationWorks(t *testing.T) {
	dir := t.TempDir()
	ln, _ := startTestTLSServer(t, dir)
	defer ln.Close()

	conn, err := TLSDial(ln.Addr().String(), NewTLSConfig(true))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
}

func TestTLSDialVerifyWithSystemRootsFailsForSelfSigned(t *testing.T) {
	dir := t.TempDir()
	ln, _ := startTestTLSServer(t, dir)
	defer ln.Close()

	_, err := TLSDial(ln.Addr().String(), &TLSConfig{Enabled: true, Insecure: false})
	if err == nil {
		t.Fatal("expected TLS verification to fail for self-signed cert")
	}
}

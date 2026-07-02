package e2e

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"tunnel/relay"
)

// TestE2ERemoteTargetUnreachable verifies that when a remote (-R) tunnel's local
// target is down, a public connection is closed cleanly (rather than hanging)
// and the server survives. Exercises RemoteClient.relayChannel's dial-failure
// path.
func TestE2ERemoteTargetUnreachable(t *testing.T) {
	deadTarget := fmt.Sprintf("127.0.0.1:%d", findFreePort(t)) // nothing listening

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	tunnels := []relay.RemoteTunnel{{RemotePort: remotePort, TargetAddr: deadTarget}}
	client := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go client.Run()
	defer client.Close()

	// Wait until the remote listener is up, then take that connection.
	var conn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.Dial("tcp", addr)
		if derr == nil {
			conn = c
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("remote port never came up")
	}
	defer conn.Close()

	// The server must close the connection promptly since the target is dead.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatalf("expected EOF/no data for unreachable target, got %d bytes", n)
	}

	// The server must still be alive: a second connection should also be
	// accepted (and likewise closed).
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("server not accepting after target-unreachable: %v", err)
	}
	c2.Close()
}

// TestE2ERemoteTLSAuth exercises the real production combination: remote
// forwarding over TLS with a shared token. Previously only pairwise
// combinations were tested.
func TestE2ERemoteTLSAuth(t *testing.T) {
	echoLn := startEchoLoop(t)
	defer echoLn.Close()

	serverTLS, err := relay.SetupTLS("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := relay.NewRemoteServer("127.0.0.1:0", "sekret", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	tunnels := []relay.RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()}}

	clientTLS, err := relay.SetupTLS("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "sekret", clientTLS, tunnels)
	go client.Run()
	defer client.Close()

	payload := randomBytes(64 * 1024)
	got, err := waitEcho(addr, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("tls+auth+remote echo failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tls+auth+remote: payload mismatch (%d vs %d)", len(got), len(payload))
	}
}

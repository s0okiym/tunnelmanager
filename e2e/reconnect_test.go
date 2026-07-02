package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"tunnel/relay"
)

// startEchoLoop starts an echo server that accepts many connections until it is
// closed. Unlike startEcho (single-shot) it is safe to reuse across reconnects.
func startEchoLoop(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
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

// echoThrough dials addr, sends payload with half-close, and returns the echo.
func echoThrough(addr string, payload []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	errc := make(chan error, 1)
	go func() {
		_, werr := conn.Write(payload)
		halfCloseTCP(conn)
		errc <- werr
	}()
	got, rerr := io.ReadAll(conn)
	werr := <-errc
	if rerr != nil {
		return nil, rerr
	}
	if werr != nil {
		return nil, werr
	}
	return got, nil
}

// waitEcho polls until an echo round-trip on addr succeeds or the deadline passes.
func waitEcho(addr string, payload []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := echoThrough(addr, payload)
		if err == nil {
			return got, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, lastErr
}

// waitPortFree polls until 0.0.0.0:port can be bound (i.e. the server has
// released its listener) or the deadline passes.
func waitPortFree(port uint16, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			ln.Close()
			return nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return lastErr
}

// TestE2ERemoteServerFreesPortOnDisconnect is the deterministic regression test
// for the per-tunnel listener leak: once the control connection dies, the
// server must release the public listener promptly. Without the fix the port
// stays bound indefinitely.
func TestE2ERemoteServerFreesPortOnDisconnect(t *testing.T) {
	echoLn := startEchoLoop(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	tunnels := []relay.RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()}}

	c1 := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go c1.Run()

	if _, err := waitEcho(addr, randomBytes(256), 5*time.Second); err != nil {
		t.Fatalf("client echo never worked: %v", err)
	}

	c1.Close()
	if err := waitPortFree(remotePort, 3*time.Second); err != nil {
		t.Fatalf("server did not release port %d after client disconnect: %v", remotePort, err)
	}
}

// TestE2ERemoteReconnect proves that after a client disconnects, a new client
// can re-register the same remote port and traffic resumes — i.e. the server
// releases the per-tunnel public listener when the control connection dies.
func TestE2ERemoteReconnect(t *testing.T) {
	echoLn := startEchoLoop(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	tunnels := []relay.RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()}}

	c1 := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go c1.Run()

	payload := randomBytes(4096)
	got, err := waitEcho(addr, payload, 5*time.Second)
	if err != nil {
		t.Fatalf("client1: echo never worked: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("client1: echo mismatch")
	}

	// Drop client 1; the server must free the remote port. Give the server a
	// moment to tear down the control connection (real clients reconnect after
	// a backoff delay, not instantaneously).
	c1.Close()
	time.Sleep(300 * time.Millisecond)

	// Client 2 registers the same remote port — simulates auto-reconnect.
	c2 := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go c2.Run()
	defer c2.Close()

	got, err = waitEcho(addr, payload, 8*time.Second)
	if err != nil {
		t.Fatalf("client2: remote port did not recover after reconnect: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("client2: echo mismatch after reconnect")
	}
}

// TestE2ERemoteServerShutdownNoHang proves the server shuts down promptly even
// with a live client — Wait() must not block on leaked per-tunnel goroutines.
func TestE2ERemoteServerShutdownNoHang(t *testing.T) {
	echoLn := startEchoLoop(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()

	remotePort := findFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	tunnels := []relay.RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()}}
	client := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go client.Run()
	defer client.Close()

	if _, err := waitEcho(addr, randomBytes(256), 5*time.Second); err != nil {
		t.Fatalf("echo never worked: %v", err)
	}

	srv.Close()
	waitDone := make(chan struct{})
	go func() {
		srv.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("srv.Wait() hung after Close() — leaked per-tunnel goroutines")
	}
}

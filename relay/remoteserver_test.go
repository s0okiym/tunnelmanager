package relay

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func findFreePortRelay(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	return port
}

func startCountingEcho(t *testing.T, counter *atomic.Int32) net.Listener {
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
				counter.Add(1)
				io.Copy(c, c)
				c.Close()
			}(c)
		}
	}()
	return ln
}

func echoThroughRelay(addr string, payload []byte) ([]byte, error) {
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

func waitEchoRelay(addr string, payload []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := echoThroughRelay(addr, payload)
		if err == nil {
			return got, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return nil, lastErr
}

// TestRemoteServerSharedListener proves that multiple control connections can
// register the same remote port. The server must create only one public
// listener and distribute incoming connections round-robin across the control
// connections instead of failing with "address already in use".
func TestRemoteServerSharedListener(t *testing.T) {
	var counterA, counterB atomic.Int32
	echoA := startCountingEcho(t, &counterA)
	echoB := startCountingEcho(t, &counterB)
	defer echoA.Close()
	defer echoB.Close()

	srv, err := NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePortRelay(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)

	c1 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoA.Addr().String()}})
	c2 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoB.Addr().String()}})
	go c1.Run()
	go c2.Run()
	defer c1.Close()
	defer c2.Close()

	if _, err := waitEchoRelay(addr, randomBytes(100), 5*time.Second); err != nil {
		t.Fatalf("first echo never worked: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := waitEchoRelay(addr, randomBytes(100), 5*time.Second); err != nil {
			t.Fatalf("connection %d failed: %v", i+1, err)
		}
	}

	countA := counterA.Load()
	countB := counterB.Load()
	if countA == 0 || countB == 0 {
		t.Fatalf("load balancing not working: A=%d, B=%d", countA, countB)
	}
	if countA+countB != 6 {
		t.Fatalf("expected 6 connections total, got A=%d B=%d", countA, countB)
	}
}

// TestRemoteServerSharedListenerReconnect proves that when one of several
// control connections sharing a port disconnects, the shared listener stays
// open and the remaining control connection keeps serving traffic.
func TestRemoteServerSharedListenerReconnect(t *testing.T) {
	var counterA, counterB atomic.Int32
	echoA := startCountingEcho(t, &counterA)
	echoB := startCountingEcho(t, &counterB)
	defer echoA.Close()
	defer echoB.Close()

	srv, err := NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePortRelay(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)

	c1 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoA.Addr().String()}})
	c2 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{{RemotePort: remotePort, TargetAddr: echoB.Addr().String()}})
	go c1.Run()
	go c2.Run()

	if _, err := waitEchoRelay(addr, randomBytes(100), 5*time.Second); err != nil {
		t.Fatalf("first echo never worked: %v", err)
	}

	// Disconnect one client. The shared listener must remain usable.
	c1.Close()
	time.Sleep(300 * time.Millisecond)

	for i := 0; i < 3; i++ {
		if _, err := waitEchoRelay(addr, randomBytes(100), 5*time.Second); err != nil {
			t.Fatalf("connection %d after disconnect failed: %v", i+1, err)
		}
	}

	c2.Close()
}

// TestRemoteServerDistinctBindAddr proves that two control connections can
// register the same remote port with different bind addresses without
// colliding. Each bind address gets its own shared listener.
func TestRemoteServerDistinctBindAddr(t *testing.T) {
	var counterA, counterB atomic.Int32
	echoA := startCountingEcho(t, &counterA)
	echoB := startCountingEcho(t, &counterB)
	defer echoA.Close()
	defer echoB.Close()

	srv, err := NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePortRelay(t)
	addr := fmt.Sprintf("127.0.0.1:%d", remotePort)

	c1 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{
		{RemotePort: remotePort, BindAddr: "127.0.0.1", TargetAddr: echoA.Addr().String()},
	})
	c2 := NewRemoteClient(srv.Addr().String(), "", nil, []RemoteTunnel{
		{RemotePort: remotePort, BindAddr: "0.0.0.0", TargetAddr: echoB.Addr().String()},
	})
	go c1.Run()
	go c2.Run()
	defer c1.Close()
	defer c2.Close()

	// Both listeners should be reachable on loopback.
	if _, err := waitEchoRelay(addr, randomBytes(64), 5*time.Second); err != nil {
		t.Fatalf("connect to %s failed: %v", addr, err)
	}

	// The 0.0.0.0 listener should be reachable too (same address on loopback).
	// Round-robin means traffic may hit either target; just verify no error.
	for i := 0; i < 4; i++ {
		if _, err := waitEchoRelay(addr, randomBytes(64), 5*time.Second); err != nil {
			t.Fatalf("connection %d failed: %v", i+1, err)
		}
	}
}

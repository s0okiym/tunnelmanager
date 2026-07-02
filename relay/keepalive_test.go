package relay

import (
	"io"
	"net"
	"testing"
	"time"
)

// TestKeepAliveDetectsDeadPeer verifies that when the peer stops responding (a
// half-open connection), KeepAlive notices the lack of activity and closes the
// control connection so the caller's reconnect loop can take over.
func TestKeepAliveDetectsDeadPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
		// Silent peer: drain input so client writes succeed, but never reply.
		io.Copy(io.Discard, c)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cc := NewCtrlConn(conn)

	sc := <-accepted
	defer sc.Close()

	stop := make(chan struct{})
	defer close(stop)
	go KeepAlive(cc, 50*time.Millisecond, 250*time.Millisecond, stop)

	select {
	case <-cc.Done():
		// good — KeepAlive detected the dead peer and closed the connection
	case <-time.After(3 * time.Second):
		t.Fatal("KeepAlive did not detect dead peer within timeout")
	}
}

// TestKeepAliveHealthyPeer verifies KeepAlive keeps a responsive connection
// alive: a peer that echoes pongs must not be torn down.
func TestKeepAliveHealthyPeer(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b) // its read loop answers pings with pongs
	defer ccb.Close()

	stop := make(chan struct{})
	defer close(stop)
	go KeepAlive(cca, 20*time.Millisecond, 300*time.Millisecond, stop)

	select {
	case <-cca.Done():
		t.Fatal("KeepAlive closed a healthy connection")
	case <-time.After(700 * time.Millisecond):
		// still alive after several ping/pong cycles — good
	}
}

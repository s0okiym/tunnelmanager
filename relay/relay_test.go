package relay

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func randomBytes(n int) []byte {
	if n == 0 {
		return nil
	}
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// echoFunc is a helper that accepts one connection on ln and echoes what it reads.
// It runs in the current goroutine (synchronous Accept, no goroutine leak).
func echoFunc(ln net.Listener, done chan<- struct{}) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	io.Copy(conn, conn)
	conn.Close()
	if done != nil {
		done <- struct{}{}
	}
}

func halfCloseTCP(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.CloseWrite()
	} else {
		c.Close()
	}
}

// ─── Proxy tests ─────────────────────────────────────────────────────────

func TestProxyEcho(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(10000)

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	client, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client.Write(payload)
	halfCloseTCP(client)

	got, err := io.ReadAll(client)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("echo: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestProxyLargeEcho(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(10 * 1024 * 1024)

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	client, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		client.Write(payload)
		halfCloseTCP(client)
	}()

	got, err := io.ReadAll(client)
	wg.Wait()
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("10MB echo: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestProxyConcurrentEcho(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	const count = 10
	done := make(chan struct{}, count)

	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := randomBytes(1000 + id*100)
			go echoFunc(echoLn, done)

			client, err := net.Dial("tcp", proxy.listener.Addr().String())
			if err != nil {
				t.Errorf("conn %d: dial: %v", id, err)
				return
			}
			client.Write(payload)
			halfCloseTCP(client)

			got, err := io.ReadAll(client)
			client.Close()
			if err != nil {
				t.Errorf("conn %d: read: %v", id, err)
				return
			}
			<-done

			if !bytes.Equal(got, payload) {
				t.Errorf("conn %d: got %d bytes, want %d", id, len(got), len(payload))
			}
		}(i)
	}
	wg.Wait()
}

func TestProxyMultipleRounds(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	for round := range 3 {
		payload := randomBytes(5000 + round*1000)

		done := make(chan struct{}, 1)
		go echoFunc(echoLn, done)

		client, err := net.Dial("tcp", proxy.listener.Addr().String())
		if err != nil {
			t.Fatalf("round %d: dial: %v", round, err)
		}
		client.Write(payload)
		halfCloseTCP(client)

		got, err := io.ReadAll(client)
		client.Close()
		if err != nil {
			t.Fatalf("round %d: read: %v", round, err)
		}
		<-done

		if !bytes.Equal(got, payload) {
			t.Fatalf("round %d: data mismatch", round)
		}
	}
}

func TestProxyAddr(t *testing.T) {
	proxy, err := NewProxy("127.0.0.1:0", "127.0.0.1:9999")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	addr := proxy.Addr()
	if addr == nil || addr.Network() != "tcp" {
		t.Errorf("bad addr: %v", addr)
	}
}

func TestProxyManySmallWrites(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	client, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	const chunks = 100
	var expected bytes.Buffer
	for range chunks {
		chunk := randomBytes(512)
		client.Write(chunk)
		expected.Write(chunk)
	}
	halfCloseTCP(client)

	got, err := io.ReadAll(client)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, expected.Bytes()) {
		t.Fatalf("many writes: got %d bytes, want %d", len(got), expected.Len())
	}
}

func TestProxyDoneCloses(t *testing.T) {
	proxy, err := NewProxy("127.0.0.1:0", "127.0.0.1:9999")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()

	select {
	case <-proxy.Done():
		t.Fatal("Done fired before Close")
	case <-time.After(50 * time.Millisecond):
	}

	if err := proxy.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case <-proxy.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not fire after Close")
	}
}

// ─── Direct Relay test ───────────────────────────────────────────────────

func TestRelayDirect(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	a, err := net.Dial("tcp", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	bLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bLn.Close()

	go func() {
		b, err := bLn.Accept()
		if err != nil {
			return
		}
		defer b.Close()
		Relay(a, b)
	}()

	client, err := net.Dial("tcp", bLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	payload := randomBytes(5000)
	client.Write(payload)
	halfCloseTCP(client)

	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("direct relay: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestRelayDirectConcurrent(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			done := make(chan struct{}, 1)
			go echoFunc(echoLn, done)

			a, err := net.Dial("tcp", echoLn.Addr().String())
			if err != nil {
				t.Errorf("conn %d: dial a: %v", id, err)
				return
			}
			defer a.Close()

			bLn, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Errorf("conn %d: listen: %v", id, err)
				return
			}
			defer bLn.Close()

			go func() {
				b, err := bLn.Accept()
				if err != nil {
					return
				}
				defer b.Close()
				Relay(a, b)
			}()

			client, err := net.Dial("tcp", bLn.Addr().String())
			if err != nil {
				t.Errorf("conn %d: dial client: %v", id, err)
				return
			}
			defer client.Close()

			payload := randomBytes(2000 + id*500)
			client.Write(payload)
			halfCloseTCP(client)

			got, err := io.ReadAll(client)
			if err != nil {
				t.Errorf("conn %d: read: %v", id, err)
				return
			}
			<-done

			if !bytes.Equal(got, payload) {
				t.Errorf("conn %d: data mismatch: got %d want %d", id, len(got), len(payload))
			}
		}(i)
	}
	wg.Wait()
}

// ─── SOCKS5 tests ────────────────────────────────────────────────────────

// socks5Connect performs a full SOCKS5 handshake and returns the connection.
// target is "host:port".
func socks5Connect(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("socks dial: %v", err)
	}

	// greeting
	greeting := []byte{5, 1, 0} // ver=5, nmethods=1, no-auth
	if _, err := conn.Write(greeting); err != nil {
		conn.Close()
		t.Fatalf("socks greeting: %v", err)
	}

	var resp [2]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		conn.Close()
		t.Fatalf("socks greeting resp: %v", err)
	}
	if resp[0] != 5 || resp[1] != 0 {
		conn.Close()
		t.Fatalf("socks: unexpected greeting resp %v", resp)
	}

	// connect request
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		conn.Close()
		t.Fatalf("socks target: %v", err)
	}
	portInt := atoiPort(portStr)
	port := []byte{byte(portInt >> 8), byte(portInt)}

	var req []byte
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = []byte{5, 1, 0, 1}
			req = append(req, ip4...)
		} else {
			req = []byte{5, 1, 0, 4}
			req = append(req, ip.To16()...)
		}
	} else {
		req = []byte{5, 1, 0, 3, byte(len(host))}
		req = append(req, []byte(host)...)
	}
	req = append(req, port...)

	if _, err := conn.Write(req); err != nil {
		conn.Close()
		t.Fatalf("socks request: %v", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		conn.Close()
		t.Fatalf("socks reply header: %v", err)
	}
	if reply[0] != 5 || reply[1] != 0 {
		conn.Close()
		t.Fatalf("socks: connect failed, rep=%d", reply[1])
	}

	// read the rest of the reply (atyp + addr + port)
	atyp := reply[3]
	switch atyp {
	case 1: // IPv4
		io.ReadFull(conn, make([]byte, 4+2))
	case 3: // domain
		var l [1]byte
		io.ReadFull(conn, l[:])
		io.ReadFull(conn, make([]byte, int(l[0])+2))
	case 4: // IPv6
		io.ReadFull(conn, make([]byte, 16+2))
	}

	return conn
}

func atoiPort(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestSocksEcho(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(10000)

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	client := socks5Connect(t, proxy.Addr().String(), echoLn.Addr().String())
	client.Write(payload)
	halfCloseTCP(client)

	got, err := io.ReadAll(client)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("socks echo: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestSocksLargeEcho(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(5 * 1024 * 1024)

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	client := socks5Connect(t, proxy.Addr().String(), echoLn.Addr().String())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		client.Write(payload)
		halfCloseTCP(client)
	}()

	got, err := io.ReadAll(client)
	wg.Wait()
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("socks 5MB echo: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestSocksDomainTarget(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(5000)

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	// connect via domain name
	client := socks5Connect(t, proxy.Addr().String(), "127.0.0.1:"+portOf(t, echoLn))
	client.Write(payload)
	halfCloseTCP(client)

	got, err := io.ReadAll(client)
	client.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("socks domain target: got %d bytes, want %d", len(got), len(payload))
	}
}

func portOf(t *testing.T, ln net.Listener) string {
	t.Helper()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestSocksInvalidVersion(t *testing.T) {
	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// send invalid version
	conn.Write([]byte{4, 1, 0}) // SOCKS4
	var resp [2]byte
	_, err = io.ReadFull(conn, resp[:])
	if err == nil {
		t.Fatal("expected error for invalid SOCKS version")
	}
}

func TestSocksConnectRefused(t *testing.T) {
	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// greeting
	conn.Write([]byte{5, 1, 0})
	var resp [2]byte
	io.ReadFull(conn, resp[:])

	// connect to an unreachable port
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closedLn.Addr().String()
	closedLn.Close()

	host, port, _ := net.SplitHostPort(closedAddr)
	ip := net.ParseIP(host).To4()
	var p uint16
	fmt.Sscanf(port, "%d", &p)
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(p>>8), byte(p))
	conn.Write(req)

	var reply [10]byte
	n, err := io.ReadFull(conn, reply[:])
	if err != nil {
		t.Fatal(err)
	}
	if n < 4 || reply[0] != 5 || reply[1] == 0 {
		t.Fatalf("expected failure reply, got rep=%d", reply[1])
	}
}

func TestSocksAuthSuccess(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	proxy, err := NewSocksProxyWithAuth("127.0.0.1:0", "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	done := make(chan struct{}, 1)
	go echoFunc(echoLn, done)

	conn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// greeting: offer no-auth and password-auth
	conn.Write([]byte{5, 2, 0, 2})
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatal(err)
	}
	if method[0] != 5 || method[1] != 2 {
		t.Fatalf("expected password auth selected, got %v", method)
	}

	// password subnegotiation
	conn.Write([]byte{1, 5, 'a', 'l', 'i', 'c', 'e', 6, 's', 'e', 'c', 'r', 'e', 't'})
	var authReply [2]byte
	if _, err := io.ReadFull(conn, authReply[:]); err != nil {
		t.Fatal(err)
	}
	if authReply[0] != 1 || authReply[1] != 0 {
		t.Fatalf("auth failed: %v", authReply)
	}

	// connect request
	host, port, _ := net.SplitHostPort(echoLn.Addr().String())
	ip := net.ParseIP(host).To4()
	p := uint16(0)
	fmt.Sscanf(port, "%d", &p)
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(p>>8), byte(p))
	conn.Write(req)

	var reply [10]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		t.Fatalf("connect failed: %d", reply[1])
	}

	conn.Write([]byte("hello\n"))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "hello\n" {
		t.Fatalf("got %q", buf[:n])
	}
}

func TestSocksAuthFailure(t *testing.T) {
	proxy, err := NewSocksProxyWithAuth("127.0.0.1:0", "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte{5, 1, 2})
	var method [2]byte
	io.ReadFull(conn, method[:])
	if method[1] != 2 {
		t.Fatal("password auth not selected")
	}

	conn.Write([]byte{1, 5, 'a', 'l', 'i', 'c', 'e', 5, 'w', 'r', 'o', 'n', 'g'})
	var authReply [2]byte
	if _, err := io.ReadFull(conn, authReply[:]); err != nil {
		t.Fatal(err)
	}
	if authReply[1] == 0 {
		t.Fatal("expected auth failure")
	}
}

func TestSocksNoAuthRejectedWhenRequired(t *testing.T) {
	proxy, err := NewSocksProxyWithAuth("127.0.0.1:0", "alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// offer only no-auth
	conn.Write([]byte{5, 1, 0})
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		t.Fatal(err)
	}
	if method[1] != 0xff {
		t.Fatalf("expected no acceptable method, got %d", method[1])
	}
}

func TestPackedAddrNonTCP(t *testing.T) {
	// packedAddr should not panic on a non-TCP address.
	addr := &net.UnixAddr{Name: "/tmp/x", Net: "unix"}
	out := packedAddr(addr)
	if len(out) != 7 || out[0] != socksAtypIPv4 {
		t.Fatalf("unexpected fallback packed addr: %v", out)
	}
}

func TestSocksDoneCloses(t *testing.T) {
	proxy, err := NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()

	select {
	case <-proxy.Done():
		t.Fatal("Done fired before Close")
	case <-time.After(50 * time.Millisecond):
	}

	if err := proxy.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case <-proxy.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not fire after Close")
	}
}

// ─── Phase 3: Remote / Frame / CtrlConn tests ───────────────────────────

func TestFrameRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteFrame(a, FramePing, []byte("hello"))
	}()

	frame, err := ReadFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if frame.Type != FramePing || string(frame.Payload) != "hello" {
		t.Fatalf("got type=%d payload=%q", frame.Type, string(frame.Payload))
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go WriteFrame(a, FramePong, nil)
	frame, err := ReadFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != FramePong || len(frame.Payload) != 0 {
		t.Fatal("expected empty pong")
	}
}

func TestFrameOversizedRejected(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// Craft a header claiming a payload larger than maxFramePayload.
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[:4], maxFramePayload+1)
	header[4] = FramePing
	go a.Write(header)

	_, err := ReadFrame(b)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("too large")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCtrlConnChannelBasic(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	ch, err := cca.OpenChannel("localhost:8080")
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := ccb.AcceptChannel()
	if err != nil {
		t.Fatal(err)
	}

	if accepted.target != "localhost:8080" {
		t.Fatalf("target mismatch: %s", accepted.target)
	}

	// write from server side
	go accepted.Write([]byte("ping"))

	buf := make([]byte, 4)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("got %q", buf[:n])
	}

	// write from client side
	ch.Write([]byte("pong"))
	n, err = accepted.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("got %q", buf[:n])
	}
}

func TestCtrlConnChannelClose(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	ch, err := cca.OpenChannel("t:1")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ccb.AcceptChannel()
	if err != nil {
		t.Fatal(err)
	}

	ch.CloseWrite()
	buf := make([]byte, 4)
	n, err := accepted.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected EOF after close, got err=%v n=%d", err, n)
	}
}

func TestCtrlConnAuth(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	errCh := make(chan error, 2)
	go func() {
		errCh <- AuthClient(a, "mytoken")
	}()
	go func() {
		errCh <- AuthServer(b, "mytoken")
	}()

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCtrlConnAuthReject(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	errCh := make(chan error, 2)
	go func() {
		errCh <- AuthClient(a, "wrong")
	}()
	go func() {
		errCh <- AuthServer(b, "expected")
	}()

	clientErr := <-errCh
	serverErr := <-errCh
	if clientErr == nil || serverErr == nil {
		t.Fatal("expected auth failure")
	}
}

func TestCtrlConnRegister(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	tunnels := []RemoteTunnel{
		{RemotePort: 9090, TargetAddr: "localhost:8080"},
		{RemotePort: 9091, TargetAddr: "127.0.0.1:3000"},
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- RegisterClient(a, tunnels)
	}()
	go func() {
		got, err := RegisterServer(b)
		if err != nil {
			errCh <- err
			return
		}
		if len(got) != 2 {
			errCh <- fmt.Errorf("expected 2 tunnels, got %d", len(got))
			return
		}
		if got[0].RemotePort != 9090 || got[0].TargetAddr != "localhost:8080" {
			errCh <- fmt.Errorf("tunnel 0 mismatch: %+v", got[0])
			return
		}
		if got[1].RemotePort != 9091 || got[1].TargetAddr != "127.0.0.1:3000" {
			errCh <- fmt.Errorf("tunnel 1 mismatch: %+v", got[1])
			return
		}
		errCh <- nil
	}()

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoteE2E(t *testing.T) {
	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localLn.Close()

	done := make(chan struct{}, 1)
	go echoFunc(localLn, done)

	srv, err := NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	// find a free port for the remote listener
	remoteLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remotePort := uint16(remoteLn.Addr().(*net.TCPAddr).Port)
	remoteLn.Close()

	tunnels := []RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: localLn.Addr().String()},
	}
	client := NewRemoteClient(srv.ctrlLn.Addr().String(), "", nil, tunnels)

	go func() {
		client.Run()
	}()
	defer client.Close()

	time.Sleep(300 * time.Millisecond)

	// connect to the remote port
	payload := randomBytes(5000)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		t.Fatal(err)
	}

	conn.Write(payload)
	halfCloseTCP(conn)

	got, err := io.ReadAll(conn)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("remote e2e: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestBackoffDelay(t *testing.T) {
	cfg := BackoffConfig{
		BaseDelay:   time.Second,
		MaxDelay:    10 * time.Second,
		JitterRatio: 0,
	}

	// without jitter, should be deterministic
	d0 := BackoffDelay(cfg, 0)
	if d0 != time.Second {
		t.Fatalf("attempt 0: want 1s, got %v", d0)
	}

	d1 := BackoffDelay(cfg, 1)
	if d1 != 2*time.Second {
		t.Fatalf("attempt 1: want 2s, got %v", d1)
	}

	d4 := BackoffDelay(cfg, 4)
	if d4 != 10*time.Second {
		t.Fatalf("attempt 4: want 10s (capped), got %v", d4)
	}

	d10 := BackoffDelay(cfg, 10)
	if d10 != 10*time.Second {
		t.Fatalf("attempt 10: want 10s (capped), got %v", d10)
	}
}

func TestTLSCertGenerate(t *testing.T) {
	cert, err := GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no cert generated")
	}
}

func TestTLSConnection(t *testing.T) {
	cert, err := GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &TLSConfig{Enabled: true, Cert: cert, Insecure: true}

	ln, err := TLSListener("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payload := randomBytes(10000)

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		io.Copy(conn, conn)
		conn.Close()
	}()

	conn, err := TLSDial(ln.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write(payload)
	closeWrite(conn)

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tls echo: got %d bytes, want %d", len(got), len(payload))
	}
}

// ─── Benchmarks ──────────────────────────────────────────────────────────

func BenchmarkProxyThroughput(b *testing.B) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer echoLn.Close()

	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	proxy, err := NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	sizes := []int{1024, 64 * 1024, 1 * 1024 * 1024}
	for _, sz := range sizes {
		b.Run(nameSize(sz), func(b *testing.B) {
			payload := randomBytes(sz)
			b.SetBytes(int64(sz))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				client, err := net.Dial("tcp", proxy.listener.Addr().String())
				if err != nil {
					b.Fatal(err)
				}
				go func() {
					client.Write(payload)
					halfCloseTCP(client)
				}()
				io.ReadAll(client)
				client.Close()
			}
		})
	}
}

func nameSize(sz int) string {
	switch {
	case sz < 1024:
		return "1KB"
	case sz < 1024*1024:
		return "64KB"
	default:
		return "1MB"
	}
}

// TestChannelFlowControlSlowReader verifies that a Write larger than the
// initial send window blocks until the receiver reads enough data to replenish
// the window. This prevents unbounded memory growth on a slow consumer.
func TestChannelFlowControlSlowReader(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	serverChReady := make(chan *channel, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		ch, err := ccb.AcceptChannel()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		serverChReady <- ch

		// Slow consumer: wait before reading the whole payload.
		time.Sleep(300 * time.Millisecond)
		buf := make([]byte, defaultSendWindow+1)
		n, err := io.ReadFull(ch, buf)
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		if n != defaultSendWindow+1 {
			t.Errorf("expected %d bytes, got %d", defaultSendWindow+1, n)
		}
		ch.Close()
	}()

	clientCh, err := cca.OpenChannel("target")
	if err != nil {
		t.Fatal(err)
	}
	defer clientCh.Close()

	serverCh := <-serverChReady
	_ = serverCh

	payload := make([]byte, defaultSendWindow+1)
	writeDone := make(chan error, 1)
	go func() {
		_, err := clientCh.Write(payload)
		writeDone <- err
	}()

	// The write should not complete immediately because it exceeds the window.
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("write should have blocked until the reader consumed data")
		}
		t.Fatalf("unexpected write error: %v", err)
	case <-time.After(100 * time.Millisecond):
		// expected: blocked
	}

	// After the slow reader consumes the data, the write unblocks.
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not unblock after reader consumed data")
	}

	<-serverDone
}

// TestChannelFlowControlHeadOfLineBlocking verifies that one channel whose
// receiver is stalled does not prevent another channel from transmitting data
// on the same control connection.
func TestChannelFlowControlHeadOfLineBlocking(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	serverChs := make(chan [2]*channel, 1)
	go func() {
		ch0, err := ccb.AcceptChannel()
		if err != nil {
			t.Errorf("accept ch0: %v", err)
			return
		}
		ch1, err := ccb.AcceptChannel()
		if err != nil {
			t.Errorf("accept ch1: %v", err)
			return
		}
		serverChs <- [2]*channel{ch0, ch1}
	}()

	clientCh0, err := cca.OpenChannel("target0")
	if err != nil {
		t.Fatal(err)
	}
	defer clientCh0.Close()
	clientCh1, err := cca.OpenChannel("target1")
	if err != nil {
		t.Fatal(err)
	}
	defer clientCh1.Close()

	chs := <-serverChs
	serverCh0, serverCh1 := chs[0], chs[1]

	// Fill ch0's send window so subsequent writes block.
	fill := make([]byte, defaultSendWindow)
	if _, err := clientCh0.Write(fill); err != nil {
		t.Fatalf("fill ch0: %v", err)
	}

	// ch1 should still be able to send and receive data promptly.
	msg := []byte("hello through ch1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(serverCh1, buf); err != nil {
			t.Errorf("read ch1: %v", err)
			return
		}
		if string(buf) != string(msg) {
			t.Errorf("ch1 data mismatch")
		}
	}()

	if _, err := clientCh1.Write(msg); err != nil {
		t.Fatalf("write ch1: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ch1 blocked by stalled ch0")
	}

	serverCh0.Close()
	serverCh1.Close()
}

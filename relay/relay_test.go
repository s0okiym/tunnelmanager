package relay

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
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

	// connect to unreachable port
	req := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 1} // 127.0.0.1:1
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

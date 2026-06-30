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

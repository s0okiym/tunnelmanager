package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"tunnel/relay"
)

// ─── Helpers ─────────────────────────────────────────────────────────────

func randomBytes(n int) []byte {
	if n == 0 {
		return nil
	}
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func halfCloseTCP(c net.Conn) {
	switch conn := c.(type) {
	case *net.TCPConn:
		conn.CloseWrite()
	case interface{ CloseWrite() error }:
		conn.CloseWrite()
	}
}

func startEcho(t *testing.T) (net.Listener, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	done := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(conn, conn)
		conn.Close()
		done <- struct{}{}
	}()
	return ln, done
}

func socks5Connect(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("socks dial: %v", err)
	}

	greeting := []byte{5, 1, 0}
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

	atyp := reply[3]
	switch atyp {
	case 1:
		io.ReadFull(conn, make([]byte, 4+2))
	case 3:
		var l [1]byte
		io.ReadFull(conn, l[:])
		io.ReadFull(conn, make([]byte, int(l[0])+2))
	case 4:
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

// ─── 1. Local Forwarding E2E ─────────────────────────────────────────────

func TestE2ELocalProxy(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	proxy, err := relay.NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(10000)
	client, err := net.Dial("tcp", proxy.Addr().String())
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
		t.Fatalf("local proxy: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ELocalProxyLarge(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	proxy, err := relay.NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(5 * 1024 * 1024)
	client, err := net.Dial("tcp", proxy.Addr().String())
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
		t.Fatalf("local proxy 5MB: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ELocalProxyConcurrent(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	// Multi-connection echo server
	go func() {
		for {
			conn, aErr := echoLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(conn)
		}
	}()

	proxy, err := relay.NewProxy("127.0.0.1:0", echoLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	const conns = 10
	var wg sync.WaitGroup
	for range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := randomBytes(5000)
			client, dErr := net.Dial("tcp", proxy.Addr().String())
			if dErr != nil {
				t.Error(dErr)
				return
			}
			client.Write(payload)
			halfCloseTCP(client)
			got, rErr := io.ReadAll(client)
			client.Close()
			if rErr != nil {
				t.Error(rErr)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("concurrent: got %d, want %d", len(got), len(payload))
			}
		}()
	}
	wg.Wait()
}

// ─── 2. SOCKS5 E2E ──────────────────────────────────────────────────────

func TestE2ESocks5(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	proxy, err := relay.NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(10000)
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
		t.Fatalf("socks5: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ESocks5Large(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	proxy, err := relay.NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(5 * 1024 * 1024)
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
		t.Fatalf("socks5 5MB: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ESocks5DomainTarget(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	proxy, err := relay.NewSocksProxy("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	payload := randomBytes(5000)
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
		t.Fatalf("socks5 domain: got %d bytes, want %d", len(got), len(payload))
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

// ─── 3. Remote Forwarding E2E ────────────────────────────────────────────

func TestE2ERemoteForward(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(300 * time.Millisecond)

	payload := randomBytes(10000)
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
		t.Fatalf("remote forward: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ERemoteForwardLarge(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(300 * time.Millisecond)

	payload := randomBytes(5 * 1024 * 1024)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn.Write(payload)
		halfCloseTCP(conn)
	}()

	got, err := io.ReadAll(conn)
	wg.Wait()
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if !bytes.Equal(got, payload) {
		t.Fatalf("remote forward 5MB: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ERemoteForwardConcurrent(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	go func() {
		for {
			conn, aErr := echoLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(conn)
		}
	}()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "", nil, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(300 * time.Millisecond)

	const conns = 5
	var wg sync.WaitGroup
	for range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := randomBytes(5000)
			c, dErr := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
			if dErr != nil {
				t.Error(dErr)
				return
			}
			c.Write(payload)
			halfCloseTCP(c)
			got, rErr := io.ReadAll(c)
			c.Close()
			if rErr != nil {
				t.Error(rErr)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("remote concurrent: got %d, want %d", len(got), len(payload))
			}
		}()
	}
	wg.Wait()
}

// ─── 4. Remote Forwarding + TLS E2E ──────────────────────────────────────

func TestE2ERemoteTLS(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	cert, err := relay.GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "", tlsCfg, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(500 * time.Millisecond)

	payload := randomBytes(10000)
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
		t.Fatalf("remote tls: got %d bytes, want %d", len(got), len(payload))
	}
}

// ─── 5. Remote Forwarding + Token Auth E2E ───────────────────────────────

func TestE2ERemoteAuth(t *testing.T) {
	echoLn, done := startEcho(t)
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "test-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "test-token", nil, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(300 * time.Millisecond)

	payload := randomBytes(10000)
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
		t.Fatalf("remote auth: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ERemoteAuthFail(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()

	srv, err := relay.NewRemoteServer("127.0.0.1:0", "correct-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	remotePort := findFreePort(t)
	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: echoLn.Addr().String()},
	}
	client := relay.NewRemoteClient(srv.Addr().String(), "wrong-token", nil, tunnels)
	go client.Run()
	defer client.Close()

	time.Sleep(500 * time.Millisecond)

	// The remote listener should NOT have been created (auth failed)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort), 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("expected connection refused (auth should have failed)")
	}
}

// ─── 6. TLS E2E ──────────────────────────────────────────────────────────

func TestE2ETLSEcho(t *testing.T) {
	cert, err := relay.GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}

	ln, err := relay.TLSListener("127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		io.Copy(conn, conn)
		conn.Close()
	}()

	conn, err := relay.TLSDial(ln.Addr().String(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload := randomBytes(10000)
	conn.Write(payload)
	halfCloseTCP(conn)

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tls echo: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ELocalProxyTLS(t *testing.T) {
	cert, err := relay.GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}

	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, aErr := echoLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(conn)
		}
	}()

	proxy, err := relay.NewTLSProxy("127.0.0.1:0", echoLn.Addr().String(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	conn, err := relay.TLSDial(proxy.Addr().String(), tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload := randomBytes(10000)
	conn.Write(payload)
	halfCloseTCP(conn)

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tls proxy: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestE2ELocalProxyTLSCustomCert(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, aErr := echoLn.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(conn)
		}
	}()

	// SetupTLS with custom cert -> Insecure=false (verify peer cert)
	srvCfg, err := relay.SetupTLS("/tmp/test-srv.pem", "/tmp/test-srv-key.pem")
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := relay.NewTLSProxy("127.0.0.1:0", echoLn.Addr().String(), srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	// Dial with the CA cert in the root pool — verification must pass
	caCert, err := os.ReadFile("/tmp/test-ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		t.Fatal("failed to append CA cert")
	}
	tlsDialCfg := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}
	conn, err := tls.Dial("tcp", proxy.Addr().String(), tlsDialCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload := randomBytes(5000)
	conn.Write(payload)
	halfCloseTCP(conn)

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("tls custom cert: got %d bytes, want %d", len(got), len(payload))
	}
}

// ─── Helper: find a free port ────────────────────────────────────────────

func findFreePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	return port
}



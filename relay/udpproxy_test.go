package relay

import (
	"net"
	"testing"
	"time"
)

func startUDPEcho(t *testing.T, addr string) (*net.UDPConn, func()) {
	laddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUDP("udp", laddr)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, bufferSize)
		for {
			ln.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, clientAddr, err := ln.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			if n > 0 {
				packet := make([]byte, n)
				copy(packet, buf[:n])
				ln.WriteToUDP(packet, clientAddr)
			}
		}
	}()
	return ln, func() { close(stop); ln.Close() }
}

func TestUDPProxyEcho(t *testing.T) {
	echoLn, cleanup := startUDPEcho(t, "127.0.0.1:0")
	defer cleanup()

	proxy, err := NewUDPProxy("127.0.0.1:0", echoLn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	client, err := net.Dial("udp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	payload := []byte("hello udp")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, bufferSize)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("got %q, want %q", buf[:n], payload)
	}
}

func TestUDPProxyMultipleClients(t *testing.T) {
	echoLn, cleanup := startUDPEcho(t, "127.0.0.1:0")
	defer cleanup()

	proxy, err := NewUDPProxy("127.0.0.1:0", echoLn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	go proxy.Serve()
	defer proxy.Close()

	for i := range 3 {
		client, err := net.Dial("udp", proxy.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte{byte('a' + i)}
		if _, err := client.Write(payload); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, bufferSize)
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := client.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:n]) != string(payload) {
			t.Fatalf("client %d: got %q, want %q", i, buf[:n], payload)
		}
		client.Close()
	}
}

func TestUDPProxyClose(t *testing.T) {
	proxy, err := NewUDPProxy("127.0.0.1:0", "127.0.0.1:1")
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

package relay

import (
	"bytes"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Half-close over a multiplexed channel ───────────────────────────────

// TestChannelHalfCloseReverseDirection proves that CloseWrite on one end of a
// multiplexed channel closes only that direction: the peer sees io.EOF on Read
// but can still write back down the reverse direction.
func TestChannelHalfCloseReverseDirection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	chA, err := cca.OpenChannel("target:1")
	if err != nil {
		t.Fatal(err)
	}
	chB, err := ccb.AcceptChannel()
	if err != nil {
		t.Fatal(err)
	}

	// A closes its write half; B should observe EOF on Read.
	if err := chA.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	buf := make([]byte, 16)
	n, err := chB.Read(buf)
	if err != io.EOF {
		t.Fatalf("expected io.EOF after peer CloseWrite, got err=%v n=%d", err, n)
	}

	// The reverse direction must still work: B writes, A reads. The write has to
	// be concurrent because net.Pipe is synchronous.
	go func() {
		chB.Write([]byte("reply"))
	}()

	got := make([]byte, 16)
	n, err = chA.Read(got)
	if err != nil {
		t.Fatalf("reverse Read: %v", err)
	}
	if string(got[:n]) != "reply" {
		t.Fatalf("reverse Read: got %q, want %q", got[:n], "reply")
	}
}

// ─── ReadFrame error handling ────────────────────────────────────────────

// TestReadFrameTruncatedHeader ensures a short header returns an error, not a panic.
func TestReadFrameTruncatedHeader(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader([]byte{0, 0}))
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
}

// TestReadFrameTruncatedPayload ensures a payload shorter than the declared
// length returns an error, not a panic.
func TestReadFrameTruncatedPayload(t *testing.T) {
	// Header declares payload length 10 but only 3 payload bytes follow.
	data := []byte{
		0, 0, 0, 10, // length = 10 (big-endian)
		FrameChannelData, // type
		0x01, 0x02, 0x03, // only 3 of the 10 payload bytes
	}
	_, err := ReadFrame(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error for truncated payload, got nil")
	}
}

// ─── handleNewChannel robustness ─────────────────────────────────────────

// TestHandleNewChannelShortPayload ensures a payload shorter than the 6-byte
// header returns nil without panicking.
func TestHandleNewChannelShortPayload(t *testing.T) {
	cc := &CtrlConn{channels: make(map[uint32]*channel)}
	if ch := cc.handleNewChannel([]byte{0, 0, 0, 1}); ch != nil {
		t.Fatalf("expected nil channel for short payload, got %v", ch)
	}
}

// ─── ChainProxy smoke test ───────────────────────────────────────────────

// multiEcho accepts connections on ln in a loop and echoes each one until ln
// is closed. Each connection is served in its own goroutine.
func multiEcho(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			io.Copy(c, c)
			c.Close()
		}(conn)
	}
}

// TestChainProxySmoke exercises the ChainProxy forwarding path end to end.
func TestChainProxySmoke(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go multiEcho(echoLn)

	cp, err := NewChainProxy("127.0.0.1:0", []string{echoLn.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	go cp.Serve()
	defer cp.Close()

	client, err := net.Dial("tcp", cp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	payload := randomBytes(8000)
	go func() {
		client.Write(payload)
		halfCloseTCP(client)
	}()

	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("chain proxy echo: got %d bytes, want %d", len(got), len(payload))
	}
}

// ─── HealthCheck logging ─────────────────────────────────────────────────

// lockedLogBuf is a race-free io.Writer that captures log output. Both Write
// and String hold the mutex so the race detector stays quiet.
type lockedLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedLogBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedLogBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestHealthCheckDetectsDown verifies that HealthCheck logs a "down" message
// after the configured number of consecutive dial failures.
func TestHealthCheckDetectsDown(t *testing.T) {
	// Pick a port, then close it so the address is definitely dead.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := deadLn.Addr().String()
	deadLn.Close()

	orig := log.Writer()
	capture := &lockedLogBuf{}
	log.SetOutput(capture)
	defer log.SetOutput(orig)

	stop := make(chan struct{})
	go HealthCheck(deadAddr, 30*time.Millisecond, 2, stop)

	deadline := time.Now().Add(2 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if strings.Contains(capture.String(), "down") {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)

	if !found {
		t.Fatalf("expected a %q log message, got: %q", "down", capture.String())
	}
}

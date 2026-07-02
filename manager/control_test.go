package manager

import (
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

func waitForSocket(t *testing.T) {
	t.Helper()
	path := ControlSocketPath()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("control socket %s never appeared", path)
}

// startControl starts a control server and returns a stop function that closes
// it and waits for the goroutine to fully exit (so callers can safely restore
// globals afterwards without racing the server goroutine).
func startControl(t *testing.T, handler func(Request) Response) func() {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ServeControl(handler, stop)
	}()
	waitForSocket(t)
	return func() {
		close(stop)
		<-done
	}
}

// TestControlRoundTrip exercises the real Unix-socket control transport
// (ServeControl + SendControl), which was previously untested.
func TestControlRoundTrip(t *testing.T) {
	dir := t.TempDir()
	orig := DefaultDataDir
	DefaultDataDir = dir

	stopSrv := startControl(t, func(req Request) Response {
		return Response{Result: json.RawMessage(`"pong"`), ID: req.ID}
	})
	defer func() { stopSrv(); DefaultDataDir = orig }()

	res, err := SendControl("ping", nil)
	if err != nil {
		t.Fatalf("SendControl: %v", err)
	}
	if string(res) != `"pong"` {
		t.Fatalf("got %s, want \"pong\"", res)
	}
}

// TestControlSocketPermissions verifies the control socket is owner-only (0600).
func TestControlSocketPermissions(t *testing.T) {
	dir := t.TempDir()
	orig := DefaultDataDir
	DefaultDataDir = dir

	stopSrv := startControl(t, func(req Request) Response { return Response{ID: req.ID} })
	defer func() { stopSrv(); DefaultDataDir = orig }()

	info, err := os.Stat(ControlSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("control socket perms = %o, want 0600", perm)
	}
}

// TestControlReadTimeout verifies an idle client that connects but never sends
// a complete request is disconnected promptly rather than leaking a goroutine.
func TestControlReadTimeout(t *testing.T) {
	dir := t.TempDir()
	orig := DefaultDataDir
	DefaultDataDir = dir

	origTO := controlReadTimeout.Load()
	controlReadTimeout.Store(int64(200 * time.Millisecond))

	stopSrv := startControl(t, func(req Request) Response { return Response{ID: req.ID} })
	defer func() { stopSrv(); controlReadTimeout.Store(origTO); DefaultDataDir = orig }()

	conn, err := net.Dial("unix", ControlSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	start := time.Now()
	_, err = conn.Read(buf)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected idle connection to be closed by server")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("server did not close idle connection promptly (%v)", elapsed)
	}
}

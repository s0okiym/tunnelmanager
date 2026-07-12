package relay

import (
	"testing"
	"time"
)

func TestRemoteClientLastError(t *testing.T) {
	client := NewRemoteClient("127.0.0.1:1", "", nil, []RemoteTunnel{
		{RemotePort: 1234, TargetAddr: "127.0.0.1:80"},
	})

	stop := make(chan struct{})
	go func() {
		client.Run()
	}()

	// Wait for at least one failed connection attempt.
	time.Sleep(100 * time.Millisecond)

	if client.LastError() == nil {
		t.Fatal("expected LastError to be set after failed dial")
	}

	close(stop)
	client.Close()
}

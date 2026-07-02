package relay

import (
	"net"
	"testing"
)

func numChannels(cc *CtrlConn) int {
	cc.cmu.Lock()
	defer cc.cmu.Unlock()
	return len(cc.channels)
}

// TestCtrlConnChannelMapCleanup proves that closing a channel removes it from
// the multiplexer's channel map on both ends, so a long-lived control
// connection does not leak memory per handled connection.
func TestCtrlConnChannelMapCleanup(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	cca := NewCtrlConn(a)
	ccb := NewCtrlConn(b)

	const N = 20
	for i := 0; i < N; i++ {
		ch, err := cca.OpenChannel("t")
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		accepted, err := ccb.AcceptChannel()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		ch.Close()
		accepted.Close()
	}

	if n := numChannels(cca); n != 0 {
		t.Fatalf("opener channel map leaked: %d channels remain after close", n)
	}
	if n := numChannels(ccb); n != 0 {
		t.Fatalf("accepter channel map leaked: %d channels remain after close", n)
	}
}

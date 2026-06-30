package relay

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
)

const bufferSize = 32 * 1024

var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, bufferSize)
		return &b
	},
}

type Stats struct {
	SentBytes int64
	RecvBytes int64
	SentErr   error
	RecvErr   error
}

func Relay(a, b net.Conn) Stats {
	var s Stats
	var wg sync.WaitGroup
	wg.Add(2)

	// G1: copy from a (=upstream/server) to b (=downstream/client)
	go func() {
		defer wg.Done()
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		n, err := io.CopyBuffer(b, a, *buf)
		atomic.StoreInt64(&s.SentBytes, n)
		if err != nil {
			s.SentErr = err
		}
		// Done reading from a. Signal EOF on b's write side.
		closeWrite(b)
	}()

	// G2: copy from b (=downstream/client) to a (=upstream/server)
	go func() {
		defer wg.Done()
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		n, err := io.CopyBuffer(a, b, *buf)
		atomic.StoreInt64(&s.RecvBytes, n)
		if err != nil {
			s.RecvErr = err
		}
		// Done reading from b. Signal EOF on a's write side.
		closeWrite(a)
	}()

	wg.Wait()
	return s
}

func closeWrite(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.CloseWrite()
	} else {
		c.Close()
	}
}

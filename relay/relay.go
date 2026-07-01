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

	go func() {
		defer wg.Done()
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		n, err := io.CopyBuffer(b, a, *buf)
		atomic.StoreInt64(&s.SentBytes, n)
		if err != nil {
			s.SentErr = err
		}
		closeWrite(b)
	}()

	go func() {
		defer wg.Done()
		buf := bufPool.Get().(*[]byte)
		defer bufPool.Put(buf)
		n, err := io.CopyBuffer(a, b, *buf)
		atomic.StoreInt64(&s.RecvBytes, n)
		if err != nil {
			s.RecvErr = err
		}
		closeWrite(a)
	}()

	wg.Wait()
	return s
}

func closeWrite(c net.Conn) {
	switch conn := c.(type) {
	case *net.TCPConn:
		conn.CloseWrite()
	case interface{ CloseWrite() error }:
		conn.CloseWrite()
	default:
		c.Close()
	}
}

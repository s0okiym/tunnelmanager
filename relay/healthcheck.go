package relay

import (
	"log"
	"net"
	"time"
)

func HealthCheck(addr string, interval time.Duration, failures int, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	consecutive := 0

	for {
		select {
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", addr, interval/2)
			if err != nil {
				consecutive++
				if consecutive >= failures {
					log.Printf("health: %s down (%d consecutive failures)", addr, consecutive)
				}
			} else {
				conn.Close()
				if consecutive >= failures {
					log.Printf("health: %s recovered", addr)
				}
				consecutive = 0
			}
		case <-stop:
			return
		}
	}
}

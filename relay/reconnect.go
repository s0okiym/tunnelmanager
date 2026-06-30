package relay

import (
	"math/rand"
	"time"
)

type BackoffConfig struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	JitterRatio float64
}

var DefaultBackoff = BackoffConfig{
	BaseDelay:   time.Second,
	MaxDelay:    30 * time.Second,
	JitterRatio: 0.2,
}

func BackoffDelay(cfg BackoffConfig, attempt int) time.Duration {
	n := min(attempt, 30)
	d := float64(cfg.BaseDelay)
	for range n {
		d *= 2
	}
	if d > float64(cfg.MaxDelay) {
		d = float64(cfg.MaxDelay)
	}
	jitter := d * cfg.JitterRatio
	d = d - jitter + rand.Float64()*2*jitter
	return time.Duration(d)
}

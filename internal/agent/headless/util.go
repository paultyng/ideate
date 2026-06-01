package headless

import (
	"context"
	"sync"
	"time"
)

// ctxWithOptTimeout returns a context that derives from parent. If d
// is positive, the returned context also carries a timeout; otherwise
// it's a plain cancel wrapper. Always returns a non-nil cancel.
func ctxWithOptTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d > 0 {
		return context.WithTimeout(parent, d)
	}
	return context.WithCancel(parent)
}

// capBuffer is a thread-safe bounded buffer used to capture subprocess
// stderr without allowing it to grow without bound. Once cap bytes
// are written, further writes are silently discarded.
type capBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) >= c.cap {
		return len(p), nil
	}
	room := c.cap - len(c.buf)
	if len(p) > room {
		c.buf = append(c.buf, p[:room]...)
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

package retrieval

import (
	"sync/atomic"
	"time"
)

type counter struct {
	val uint64
}

func newCounter() *counter {
	return &counter{val: uint64(time.Now().UnixNano()) / uint64(time.Millisecond/time.Nanosecond)}
}

func (c *counter) next() uint64 {
	counter := atomic.AddUint64(&c.val, 1)
	return counter
}

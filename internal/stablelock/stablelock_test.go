package stablelock

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type countCloser struct{ closes atomic.Int32 }

func (c *countCloser) Close() error {
	c.closes.Add(1)
	return nil
}

func TestLeaseGroupConcurrentCloseIsIdempotent(t *testing.T) {
	first := &countCloser{}
	second := &countCloser{}
	group := &leaseGroup{leases: []io.Closer{first, second}}
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if err := group.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	callers.Wait()
	if first.closes.Load() != 1 || second.closes.Load() != 1 {
		t.Fatalf("close counts = %d, %d", first.closes.Load(), second.closes.Load())
	}
}

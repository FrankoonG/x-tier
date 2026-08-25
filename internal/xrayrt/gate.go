package xrayrt

import (
	"context"
	"sync"
)

type contextGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *contextGate) lock(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	g.once.Do(func() {
		g.token = make(chan struct{}, 1)
		g.token <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.unlock()
			return err
		}
		return nil
	}
}

func (g *contextGate) unlock() {
	g.token <- struct{}{}
}

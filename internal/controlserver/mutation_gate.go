package controlserver

import (
	"context"
	"sync"

	"github.com/FrankoonG/x-tier/internal/controlapi"
)

type mutationGate struct {
	once      sync.Once
	available chan struct{}
}

func (g *mutationGate) lock(ctx context.Context) error {
	g.once.Do(func() {
		g.available = make(chan struct{}, 1)
		g.available <- struct{}{}
	})
	select {
	case <-g.available:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *mutationGate) unlock() {
	select {
	case g.available <- struct{}{}:
	default:
		panic("controlserver: mutation gate unlocked without ownership")
	}
}

func (s *Server) mutationContext() (context.Context, context.CancelFunc) {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	timeout := s.mutationWait
	if timeout <= 0 {
		timeout = controlapi.MutationExecutionBudget
	}
	return context.WithTimeout(parent, timeout)
}

func (s *Server) acquireReloadMutation(ctx context.Context) (func(), error) {
	if err := s.reloadMutationGate.lock(ctx); err != nil {
		return nil, err
	}
	return s.reloadMutationGate.unlock, nil
}

func (s *Server) acquireRestoreMutation(ctx context.Context) (func(), error) {
	if err := s.configMutationGate.lock(ctx); err != nil {
		return nil, err
	}
	return s.configMutationGate.unlock, nil
}

func (s *Server) acquireConfigMutation(ctx context.Context) (func(), error) {
	if err := s.reloadMutationGate.lock(ctx); err != nil {
		return nil, err
	}
	if err := s.configMutationGate.lock(ctx); err != nil {
		s.reloadMutationGate.unlock()
		return nil, err
	}
	return func() {
		s.configMutationGate.unlock()
		s.reloadMutationGate.unlock()
	}, nil
}

func (s *Server) acquireCommandMutation(ctx context.Context, args []string) (func(), error) {
	switch {
	case isRuntimeReloadCommand(args):
		return s.acquireReloadMutation(ctx)
	case isConfigRestoreCommand(args):
		return s.acquireRestoreMutation(ctx)
	default:
		return s.acquireConfigMutation(ctx)
	}
}

package xrayrt

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/core"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
)

func TestInstanceBackendUsesRealManagerAddRemoveAndExplicitClose(t *testing.T) {
	instance := newRunningTestInstance(t)
	backend, err := NewInstanceBackend(instance)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	created := make(map[string]*observedHandler)
	backend.create = func(instance *core.Instance, config *core.OutboundHandlerConfig) (featureoutbound.Handler, error) {
		handler, err := createOutboundHandler(instance, config)
		if err != nil {
			return nil, err
		}
		observed := &observedHandler{Handler: handler}
		mu.Lock()
		created[config.Tag] = observed
		mu.Unlock()
		return observed, nil
	}

	manager := NewManager(backend, NewXrayStreamDialer(instance))
	config := testGenerationConfig(t)
	if _, err := manager.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	tag := qualifiedGenerationTag(1, "edge")
	installed := backend.outbound.GetHandler(tag)
	if installed == nil {
		t.Fatalf("outbound %q was not added to the real outbound manager", tag)
	}

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backend.outbound.GetHandler(tag); got != nil {
		t.Fatalf("outbound %q remains installed after removal", tag)
	}
	mu.Lock()
	observed := created[tag]
	mu.Unlock()
	if observed == nil || observed.closes.Load() != 1 {
		t.Fatalf("explicit handler close count = %v, want 1", closeCount(observed))
	}
	if !instance.IsRunning() {
		t.Fatal("manager closed the caller-owned Xray instance")
	}
}

func TestInstanceBackendRollsBackPartiallyInstalledHandlers(t *testing.T) {
	instance := newRunningTestInstance(t)
	backend, err := NewInstanceBackend(instance)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	created := make(map[string]*observedHandler)
	backend.create = func(instance *core.Instance, config *core.OutboundHandlerConfig) (featureoutbound.Handler, error) {
		handler, err := createOutboundHandler(instance, config)
		if err != nil {
			return nil, err
		}
		observed := &observedHandler{Handler: handler}
		mu.Lock()
		created[config.Tag] = observed
		mu.Unlock()
		return observed, nil
	}

	collisionTag := qualifiedGenerationTag(1, "collision")
	collision, err := createOutboundHandler(instance, freedomOutbound(collisionTag, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.outbound.AddHandler(context.Background(), collision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = backend.outbound.RemoveHandler(context.Background(), collisionTag)
		_ = collision.Close()
	})

	config, err := NewGenerationConfig([]*core.OutboundHandlerConfig{
		freedomOutbound("temporary", ""),
		freedomOutbound("collision", ""),
		freedomOutbound("never-installed", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if generation, err := backend.Build(context.Background(), 1, config); generation != nil || err == nil {
		t.Fatalf("Build = (%v, %v), want (nil, error)", generation, err)
	}
	for _, logicalTag := range []string{"temporary", "never-installed"} {
		tag := qualifiedGenerationTag(1, logicalTag)
		if got := backend.outbound.GetHandler(tag); got != nil {
			t.Fatalf("rolled-back outbound %q remains installed", tag)
		}
	}
	if got := backend.outbound.GetHandler(collisionTag); !sameHandler(got, collision) {
		t.Fatal("rollback disturbed the pre-existing collision handler")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, logicalTag := range []string{"temporary", "collision", "never-installed"} {
		tag := qualifiedGenerationTag(1, logicalTag)
		if created[tag] == nil || created[tag].closes.Load() != 1 {
			t.Fatalf("created outbound %q close count = %v, want 1", tag, closeCount(created[tag]))
		}
	}
}

type observedHandler struct {
	featureoutbound.Handler
	closes atomic.Int64
}

func (h *observedHandler) Close() error {
	h.closes.Add(1)
	return h.Handler.Close()
}

func closeCount(handler *observedHandler) any {
	if handler == nil {
		return nil
	}
	return handler.closes.Load()
}

func TestNewInstanceBackendRejectsNilAndStoppedInstances(t *testing.T) {
	if _, err := NewInstanceBackend(nil); err == nil {
		t.Fatal("nil instance was accepted")
	}
	instance, err := core.New(baseCoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewInstanceBackend(instance); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("stopped instance error = %v", err)
	}
}

func TestInstanceBackendRetriesFailedHandlerClose(t *testing.T) {
	instance := newRunningTestInstance(t)
	backend, err := NewInstanceBackend(instance)
	if err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("close failed once")
	var observed *failOnceCloseHandler
	backend.create = func(instance *core.Instance, config *core.OutboundHandlerConfig) (featureoutbound.Handler, error) {
		handler, err := createOutboundHandler(instance, config)
		if err != nil {
			return nil, err
		}
		observed = &failOnceCloseHandler{Handler: handler, err: closeFailure}
		return observed, nil
	}
	handle, err := backend.Build(context.Background(), 1, testGenerationConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Remove(handle); !errors.Is(err, closeFailure) {
		t.Fatalf("first Remove = %v, want close failure", err)
	}
	if err := backend.Remove(handle); err != nil {
		t.Fatalf("second Remove = %v", err)
	}
	if observed == nil || observed.closes.Load() != 2 {
		t.Fatalf("handler close attempts = %v, want 2", closeCountFailOnce(observed))
	}
}

func TestInstanceBackendReturnsRetryHandleForUninstalledCloseFailure(t *testing.T) {
	instance := newRunningTestInstance(t)
	backend, err := NewInstanceBackend(instance)
	if err != nil {
		t.Fatal(err)
	}
	closeFailure := errors.New("uninstalled close failed once")
	createFailure := errors.New("second handler creation failed")
	var observed *failOnceCloseHandler
	created := 0
	backend.create = func(instance *core.Instance, config *core.OutboundHandlerConfig) (featureoutbound.Handler, error) {
		created++
		if created == 2 {
			return nil, createFailure
		}
		handler, err := createOutboundHandler(instance, config)
		if err != nil {
			return nil, err
		}
		observed = &failOnceCloseHandler{Handler: handler, err: closeFailure}
		return observed, nil
	}
	config, err := NewGenerationConfig([]*core.OutboundHandlerConfig{
		freedomOutbound("first", ""),
		freedomOutbound("second", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := backend.Build(context.Background(), 1, config)
	if handle == nil || !errors.Is(err, createFailure) || !errors.Is(err, closeFailure) {
		t.Fatalf("Build = (%v, %v), want retry handle with create and close errors", handle, err)
	}
	if err := backend.Remove(handle); err != nil {
		t.Fatalf("retry Remove = %v", err)
	}
	if observed == nil || observed.closes.Load() != 2 {
		t.Fatalf("uninstalled handler close attempts = %v, want 2", closeCountFailOnce(observed))
	}
}

type failOnceCloseHandler struct {
	featureoutbound.Handler
	closes atomic.Int64
	err    error
}

func (h *failOnceCloseHandler) Close() error {
	if h.closes.Add(1) == 1 {
		return h.err
	}
	return h.Handler.Close()
}

func closeCountFailOnce(handler *failOnceCloseHandler) any {
	if handler == nil {
		return nil
	}
	return handler.closes.Load()
}

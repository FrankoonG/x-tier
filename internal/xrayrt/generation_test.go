package xrayrt

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestApplySerializesBackendBuilds(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var calls atomic.Int64
	backend := &fakeBackend{build: func(_ context.Context, id uint64, _ GenerationConfig) (Generation, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return newFakeGeneration(id), nil
	}}
	manager := NewManager(backend, nil)
	t.Cleanup(func() { _ = manager.Close() })
	config := testGenerationConfig(t)

	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Apply(context.Background(), config)
		firstDone <- err
	}()
	<-firstEntered
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Apply(context.Background(), config)
		secondDone <- err
	}()
	select {
	case <-secondEntered:
		t.Fatal("second backend build entered before first Apply completed")
	default:
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	assertCurrent(t, manager.Status(), 2, 0)
}

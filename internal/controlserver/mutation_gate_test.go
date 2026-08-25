package controlserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationGateHonorsWaiterContextAndRemainsReusable(t *testing.T) {
	var gate mutationGate
	if err := gate.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued gate error=%v, want deadline", err)
	}
	gate.unlock()
	if err := gate.lock(context.Background()); err != nil {
		t.Fatalf("gate was not reusable after canceled waiter: %v", err)
	}
	gate.unlock()
}

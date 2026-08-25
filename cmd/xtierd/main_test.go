package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunStaysResidentUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	var stdout, stderr lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{
			"--config", configPath,
			"--control", "127.0.0.1:0",
		}, &stdout, &stderr)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasPrefix(stdout.String(), "READY ") && time.Now().Before(deadline) {
		select {
		case code := <-done:
			t.Fatalf("xtierd exited before cancellation: code=%d stderr=%s", code, stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !strings.HasPrefix(stdout.String(), "READY ") {
		t.Fatalf("xtierd did not become ready: stderr=%s", stderr.String())
	}
	select {
	case code := <-done:
		t.Fatalf("xtierd was not resident: code=%d stderr=%s", code, stderr.String())
	default:
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("xtierd shutdown code=%d stderr=%s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("xtierd did not exit after cancellation")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestRunRequiresConfigPath(t *testing.T) {
	t.Setenv("XTIER_CONFIG", "")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

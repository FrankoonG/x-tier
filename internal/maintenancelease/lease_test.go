package maintenancelease

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

const (
	maintenanceLeaseHelperConfig = "XTIER_MAINTENANCE_LEASE_HELPER_CONFIG"
	maintenanceLeaseHelperReady  = "XTIER_MAINTENANCE_LEASE_HELPER_READY"
)

func TestLeaseExcludesConcurrentMaintenanceAndReleases(t *testing.T) {
	configPath := canonicalLeaseTestPath(t)
	first, err := Acquire(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Store() == nil || first.Store().ConfigPath() != configPath {
		t.Fatalf("store=%v", first.Store())
	}
	if _, err := Acquire(configPath); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second acquire error=%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	second, err := Acquire(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseHonorsKernelDaemonLock(t *testing.T) {
	configPath := canonicalLeaseTestPath(t)
	store, err := statestore.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	file, err := store.OpenLock(statestore.DaemonLock)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockInstanceFile(file); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = unlockInstanceFile(file)
		_ = file.Close()
	}()
	if _, err := Acquire(configPath); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("acquire against daemon lock error=%v", err)
	}
}

func TestLeaseConcurrentContendersHaveSingleOwner(t *testing.T) {
	configPath := canonicalLeaseTestPath(t)
	const workers = 32
	start := make(chan struct{})
	var acquired atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	var unexpected atomic.Int32
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			lease, err := Acquire(configPath)
			if err != nil {
				if !errors.Is(err, ErrDaemonRunning) {
					unexpected.Add(1)
				}
				return
			}
			acquired.Add(1)
			current := active.Add(1)
			for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			if lease.Close() != nil {
				unexpected.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	if unexpected.Load() != 0 || acquired.Load() == 0 || maximum.Load() != 1 {
		t.Fatalf("acquired=%d maximum=%d unexpected=%d", acquired.Load(), maximum.Load(), unexpected.Load())
	}
}

func TestLeaseWritesNonSecretOwnerMetadata(t *testing.T) {
	configPath := canonicalLeaseTestPath(t)
	lease, err := Acquire(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	path, err := lease.Store().DiagnosticPath(statestore.DaemonLock)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(payload), "maintenance:") || !strings.HasSuffix(string(payload), "\n") {
		t.Fatalf("metadata=%q", payload)
	}
}

func TestLeaseExcludesOtherProcessAndReleasesAfterTermination(t *testing.T) {
	configPath := canonicalLeaseTestPath(t)
	readyPath := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestMaintenanceLeaseProcessHelper$")
	command.Env = append(os.Environ(),
		maintenanceLeaseHelperConfig+"="+configPath,
		maintenanceLeaseHelperReady+"="+readyPath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		payload, err := os.ReadFile(readyPath)
		if err == nil {
			if string(payload) != "ready\n" {
				t.Fatalf("helper failed before acquiring lease: %s", payload)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for cross-process lease holder")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := Acquire(configPath); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("cross-process lease was not exclusive: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("terminated helper exited successfully")
	}
	stopped = true

	deadline = time.Now().Add(10 * time.Second)
	for {
		lease, err := Acquire(configPath)
		if err == nil {
			if closeErr := lease.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			break
		}
		if !errors.Is(err, ErrDaemonRunning) {
			t.Fatalf("lease after helper termination: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("cross-process lease was not released after termination")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMaintenanceLeaseProcessHelper(t *testing.T) {
	configPath := os.Getenv(maintenanceLeaseHelperConfig)
	readyPath := os.Getenv(maintenanceLeaseHelperReady)
	if configPath == "" && readyPath == "" {
		return
	}
	if configPath == "" || readyPath == "" {
		return
	}
	lease, err := Acquire(configPath)
	if err != nil {
		_ = os.WriteFile(readyPath, []byte("error: "+err.Error()+"\n"), 0o600)
		return
	}
	defer lease.Close()
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		return
	}
	select {}
}

func canonicalLeaseTestPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(t.TempDir(), "xtier.json"))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Clean(path)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenInstanceLockRejectsExistingWideMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".daemon.lock")
	if err := os.WriteFile(path, []byte("stale\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if file, err := openInstanceLock(path); err == nil {
		_ = file.Close()
		t.Fatal("openInstanceLock accepted an existing broad mode")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o666 {
		t.Fatalf("insecure lock was silently repaired to %04o", info.Mode().Perm())
	}
}

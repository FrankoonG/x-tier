//go:build !windows

package configstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndSaveRejectPOSIXSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := writeFileAtomic(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load(alias); err == nil {
		t.Fatal("Load accepted a symlink")
	}
	if err := Save(alias, DefaultConfig()); err == nil {
		t.Fatal("Save accepted a symlink")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "{}\n" {
		t.Fatalf("symlink target changed: %q", contents)
	}
	if _, err := CanonicalPath(alias); err == nil {
		t.Fatal("CanonicalPath accepted a symlink target")
	}
}

func TestLoadRejectsPOSIXHardlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Link(path, alias); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load hardlink error = %v, want ErrInsecureFile", err)
	}
	if err := Save(path, DefaultConfig()); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Save hardlink error = %v, want ErrInsecureFile", err)
	}
	if _, err := CanonicalPath(alias); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("CanonicalPath hardlink error = %v, want ErrInsecureFile", err)
	}
}

func TestLoadRejectsPOSIXWideMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load wide mode error = %v, want ErrInsecureFile", err)
	}
	if err := Save(path, DefaultConfig()); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Save wide mode error = %v, want ErrInsecureFile", err)
	}
}

func TestLoadRejectsPOSIXWrongOwnerWhenPrivileged(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, -1); err != nil {
		t.Skipf("chown unavailable: %v", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load wrong owner error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockRejectsPOSIXWideMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path+".lock", []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(path, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockRejectsPOSIXHardlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(lockPath, filepath.Join(dir, "lock-alias")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if err := WithLock(path, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock hardlink error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockRejectsPOSIXWrongOwnerWhenPrivileged(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing file ownership requires root")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path+".lock", []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path+".lock", 65534, -1); err != nil {
		t.Skipf("chown unavailable: %v", err)
	}
	if err := WithLock(path, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock wrong owner error = %v, want ErrInsecureFile", err)
	}
}

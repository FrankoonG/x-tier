//go:build windows

package configstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLoadAndSaveRejectWindowsReparsePoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := writeFileAtomic(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load(alias); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load reparse error = %v, want ErrInsecureFile", err)
	}
	if err := Save(alias, DefaultConfig()); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Save reparse error = %v, want ErrInsecureFile", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "{}\n" {
		t.Fatalf("reparse target changed: %q", contents)
	}
	if _, err := CanonicalPath(alias); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("CanonicalPath reparse error = %v, want ErrInsecureFile", err)
	}
}

func TestLoadRejectsWindowsHardlink(t *testing.T) {
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

func TestLoadRejectsWindowsInheritedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load inherited DACL error = %v, want ErrInsecureFile", err)
	}
	if err := Save(path, DefaultConfig()); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Save inherited DACL error = %v, want ErrInsecureFile", err)
	}
}

func TestLoadRejectsWindowsWrongOwnerWhenPermitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
		administrators,
		nil,
		nil,
		nil,
	); err != nil {
		t.Skipf("changing owner is not permitted: %v", err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("Load wrong owner error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockCreatesProtectedDACLAndRejectsInheritedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	lock, err := openWindowsSecureFile(
		lockPath,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
	)
	if err != nil {
		t.Fatalf("created lock security: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	insecurePath := filepath.Join(t.TempDir(), "config.json")
	insecureLockPath := insecurePath + ".lock"
	if err := os.WriteFile(insecureLockPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(insecurePath, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock inherited DACL error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockRejectsWindowsHardlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path+".lock", filepath.Join(dir, "lock-alias")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if err := WithLock(path, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock hardlink error = %v, want ErrInsecureFile", err)
	}
}

func TestWithLockRejectsWindowsWrongOwnerWhenPermitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		lockPath,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
		administrators,
		nil,
		nil,
		nil,
	); err != nil {
		t.Skipf("changing lock owner is not permitted: %v", err)
	}
	if err := WithLock(path, func() error { return nil }); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("WithLock wrong owner error = %v, want ErrInsecureFile", err)
	}
}

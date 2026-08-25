//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenInstanceLockCreatesExactACLAndSupportsLocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".daemon.lock")
	first, err := openInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	assertInstanceLockSecurity(t, first)

	second, err := openInstanceLock(path)
	if err != nil {
		t.Fatalf("reopen existing lock: %v", err)
	}
	defer second.Close()
	if err := lockInstanceFile(first); err != nil {
		t.Fatalf("lock newly created file: %v", err)
	}
	if err := lockInstanceFile(second); err == nil {
		_ = unlockInstanceFile(second)
		t.Fatal("second handle acquired an overlapping exclusive lock")
	}
	if err := unlockInstanceFile(first); err != nil {
		t.Fatalf("unlock first handle: %v", err)
	}
	if err := lockInstanceFile(second); err != nil {
		t.Fatalf("lock existing file after release: %v", err)
	}
	if err := unlockInstanceFile(second); err != nil {
		t.Fatalf("unlock second handle: %v", err)
	}
}

func TestOpenInstanceLockRejectsBroadACLWithoutRepairingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".daemon.lock")
	file, err := openInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	trusted, err := instanceLockTrustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(trusted)+1)
	for _, sid := range append(trusted, users) {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: instanceLockFullControl,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	if file, err := openInstanceLock(path); err == nil {
		_ = file.Close()
		t.Fatal("openInstanceLock accepted a DACL granting Builtin Users")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if unchanged == nil || int(unchanged.AceCount) != len(trusted)+1 {
		t.Fatalf("insecure DACL was changed after rejection: ACE count=%v, want %d", aceCount(unchanged), len(trusted)+1)
	}
}

func TestOpenInstanceLockRejectsReparsePoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".daemon.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Windows symlink unavailable: %v", err)
	}
	if file, err := openInstanceLock(path); err == nil {
		_ = file.Close()
		t.Fatal("openInstanceLock accepted a reparse point")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("reparse target was modified: %q", contents)
	}
}

func TestOpenInstanceLockRejectsHardlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".daemon.lock")
	file, err := openInstanceLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.lock")
	if err := os.Link(path, alias); err != nil {
		t.Skipf("Windows hardlink unavailable: %v", err)
	}
	if file, err := openInstanceLock(path); err == nil {
		_ = file.Close()
		t.Fatal("openInstanceLock accepted a multiply linked file")
	}
}

func assertInstanceLockSecurity(t *testing.T, file *os.File) {
	t.Helper()
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("lock DACL is not protected")
	}

	trusted, err := instanceLockTrustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || defaulted || !owner.Equals(trusted[0]) {
		t.Fatal("lock owner is not explicitly the current user")
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("lock DACL is missing or defaulted: DACL=%v defaulted=%v", dacl, defaulted)
	}
	if int(dacl.AceCount) != len(trusted) {
		t.Fatalf("lock ACE count=%d, want %d", dacl.AceCount, len(trusted))
	}
	seen := make([]bool, len(trusted))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatal(err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d is not an allow ACE", index)
		}
		if ace.Header.AceFlags != 0 {
			t.Fatalf("ACE %d flags=%#x, want no inheritance", index, ace.Header.AceFlags)
		}
		if ace.Mask != instanceLockFullControl {
			t.Fatalf("ACE %d mask=%#x, want %#x", index, ace.Mask, instanceLockFullControl)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matched := -1
		for trustedIndex, trustedSID := range trusted {
			if trustedSID.Equals(sid) {
				matched = trustedIndex
				break
			}
		}
		if matched < 0 || seen[matched] {
			t.Fatalf("ACE %d has an unexpected or duplicate SID", index)
		}
		seen[matched] = true
	}
	for index, present := range seen {
		if !present {
			t.Fatalf("lock DACL omits SID %s", trusted[index].String())
		}
	}
}

func aceCount(acl *windows.ACL) any {
	if acl == nil {
		return nil
	}
	return acl.AceCount
}

//go:build windows

package configstore

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const atomicFileFullControl windows.ACCESS_MASK = 0x1f01ff

func TestAtomicTempCreatedWithProtectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config.json.tmp-test")
	file, err := openAtomicTemp(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = file.Close()
		_ = os.Remove(path)
	})
	assertAtomicFileSecurity(t, file)
}

func TestAtomicWriteReplacesExistingFileOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("replace existing file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("target = %q, want new content", got)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertAtomicFileSecurity(t, file)
}

func assertAtomicFileSecurity(t *testing.T, file *os.File) {
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
		t.Fatal("file DACL inherits from its parent")
	}

	expected, owner := expectedAtomicFileSIDs(t)
	descriptorOwner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if descriptorOwner == nil || !descriptorOwner.Equals(owner) {
		t.Fatalf("owner = %v, want current user %s", descriptorOwner, owner.String())
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("DACL is missing or defaulted: dacl=%v defaulted=%v", dacl, defaulted)
	}
	if int(dacl.AceCount) != len(expected) {
		t.Fatalf("ACE count = %d, want %d", dacl.AceCount, len(expected))
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d is not an allow ACE", i)
		}
		if ace.Header.AceFlags != 0 {
			t.Fatalf("ACE %d flags = %#x, want no inheritance", i, ace.Header.AceFlags)
		}
		if ace.Mask != atomicFileFullControl {
			t.Fatalf("ACE %d mask = %#x, want full control %#x", i, ace.Mask, atomicFileFullControl)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid == nil || !sid.IsValid() {
			t.Fatalf("ACE %d has an invalid SID", i)
		}
		key := sid.String()
		seen, ok := expected[key]
		if !ok {
			t.Fatalf("ACE %d grants unexpected SID %s", i, key)
		}
		if seen {
			t.Fatalf("ACE %d duplicates SID %s", i, key)
		}
		expected[key] = true
	}
	for sid, seen := range expected {
		if !seen {
			t.Fatalf("DACL omits SID %s", sid)
		}
	}
}

func expectedAtomicFileSIDs(t *testing.T) (map[string]bool, *windows.SID) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	expected := make(map[string]bool, 3)
	for _, sid := range []*windows.SID{user.User.Sid, system, administrators} {
		expected[sid.String()] = false
	}
	return expected, user.User.Sid
}

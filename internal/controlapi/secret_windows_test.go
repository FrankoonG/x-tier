//go:build windows

package controlapi

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCreatedTokenHasOnlyTrustedDirectACEs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if _, err := CreateToken(path); err != nil {
		t.Fatal(err)
	}

	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("token DACL inherits from its parent")
	}

	trusted, err := trustedSecretSIDs()
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool, len(trusted))
	for _, sid := range trusted {
		want[sid.String()] = false
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("token DACL is missing or defaulted: dacl=%v defaulted=%v", dacl, defaulted)
	}
	if int(dacl.AceCount) != len(want) {
		t.Fatalf("direct ACE count=%d, want %d", dacl.AceCount, len(want))
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d type=%d", i, ace.Header.AceType)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			t.Fatalf("ACE %d is inherited", i)
		}
		if ace.Header.AceFlags != 0 {
			t.Fatalf("ACE %d flags=%#x", i, ace.Header.AceFlags)
		}
		if ace.Mask != secretFileFullControl {
			t.Fatalf("ACE %d mask=%#x, want full control %#x", i, ace.Mask, secretFileFullControl)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		key := sid.String()
		if _, ok := want[key]; !ok {
			t.Fatalf("ACE %d grants untrusted SID %s", i, key)
		}
		want[key] = true
	}
	for sid, seen := range want {
		if !seen {
			t.Fatalf("missing direct ACE for %s", sid)
		}
	}
}

func TestReadTokenRejectsUsersDirectACE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if _, err := CreateToken(path); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	dacl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, dacl)
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
	if _, err := ReadToken(path); err == nil {
		t.Fatal("ReadToken accepted a token readable by builtin Users")
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatalf("test identity unexpectedly lost access: %v", err)
	}
}

func TestReadTokenRejectsWindowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.token")
	if _, err := CreateToken(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.token")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink unavailable: %v", err)
	}
	if _, err := ReadToken(link); err == nil {
		t.Fatal("ReadToken accepted a Windows symlink")
	}
}

func TestReadTokenRejectsWindowsHardlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.token")
	if _, err := CreateToken(target); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.token")
	if err := os.Link(target, alias); err != nil {
		t.Skipf("Windows hardlink unavailable: %v", err)
	}
	if _, err := ReadToken(target); err == nil {
		t.Fatal("ReadToken accepted a multiply linked token")
	}
	if _, err := ReadToken(alias); err == nil {
		t.Fatal("ReadToken accepted a hardlink alias")
	}
}

//go:build windows

package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSeedDACLIsProtectedAndRejectsExtraReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keystore", "node-seed.json")
	if _, err := Create(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("protected seed did not load: %v", err)
	}

	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureSeedFile) {
		t.Fatalf("Load with broad DACL error = %v, want ErrInsecureSeedFile", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCreateRepairsDirectoryACLBeforePublish(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keystore")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := secureSecretDirectory(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node-seed.json")

	// A broad ACL inherited from a caller-created directory must be replaced
	// before any seed material is written or published.
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeValue: windows.TrusteeValueFromSID(users),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path); err != nil {
		t.Fatalf("Create did not repair the directory ACL: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

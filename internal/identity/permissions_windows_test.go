//go:build windows

package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestWindowsSeedPublicationLockCoversDirectorySetup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keystore")
	path := filepath.Join(dir, "node-seed.json")
	secretPublicationMu.Lock()

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- writeExclusiveAtomic(path, []byte("permission-race-probe\n"))
	}()
	<-started
	scopeFailure := ""
	completed := false
	var publicationErr error
	select {
	case publicationErr = <-result:
		completed = true
		scopeFailure = "publication completed while serialization lock was held"
	case <-time.After(100 * time.Millisecond):
		if _, err := os.Lstat(dir); err == nil {
			scopeFailure = "publication created its directory before acquiring the serialization lock"
		} else if !os.IsNotExist(err) {
			scopeFailure = "inspect blocked publication directory: " + err.Error()
		}
	}

	secretPublicationMu.Unlock()
	if !completed {
		publicationErr = <-result
	}
	if scopeFailure != "" {
		t.Fatal(scopeFailure)
	}
	if publicationErr != nil {
		t.Fatalf("serialized publication failed: %v", publicationErr)
	}
}

//go:build windows

package statestore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsConfigNameNormalizationAndReservedComponent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	lower, err := normalizedConfigName(root, "Mixed-Case.JSON")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := normalizedConfigName(root, "MIXED-CASE.JSON")
	if err != nil {
		t.Fatal(err)
	}
	if lower != upper {
		t.Fatalf("ordinary Windows directory did not case-fold names: %q != %q", lower, upper)
	}
	if !reservedComponent(".XTiEr-StAtE") {
		t.Fatal("mixed-case reserved state component was accepted")
	}
}

func TestWindowsCreatedDirectoryAndFileHaveExactDACLs(t *testing.T) {
	base := t.TempDir()
	parent, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := createSecureRootDirectory(parent, "state"); err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot("state")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := secureRootDirectory(root, filepath.Join(base, "state"), true); err != nil {
		t.Fatal(err)
	}
	directory, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	assertWindowsExactACL(t, directory, true, true)

	file, err := openSecureRootFile(root, "secret", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertWindowsExactACL(t, file, false, true)
}

func TestWindowsExclusiveCreateOverridesBroadParentDACL(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openSecureRootFile(root, "temporary", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertWindowsExactACL(t, file, false, true)
}

func TestWindowsExistingFileWithBroadDACLIsRejectedWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := windowsACLControlAndCount(t, path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := openSecureRootFile(root, "existing", os.O_RDONLY, 0); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("broad existing file error = %v, want ErrInsecureState", err)
	}
	after := windowsACLControlAndCount(t, path)
	if before != after {
		t.Fatalf("existing file security changed on rejection: before=%#x after=%#x", before, after)
	}
}

func TestWindowsExistingFileMayInheritOnlyTrustedDACL(t *testing.T) {
	base := t.TempDir()
	parent, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := createSecureRootDirectory(parent, "state"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "state", "inherited")
	if err := os.WriteFile(path, []byte("inherited"), 0o600); err != nil {
		t.Fatal(err)
	}
	trusted, err := windowsTrustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
		trusted[0],
		nil,
		nil,
		nil,
	); err != nil {
		t.Skipf("setting inherited file owner is unavailable: %v", err)
	}
	root, err := parent.OpenRoot("state")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openSecureRootFile(root, "inherited", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assertWindowsExactACL(t, file, false, false)
}

func TestWindowsExistingFileRejectsWrongOwner(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openSecureRootFile(root, "wrong-owner", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wrong-owner")
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
		administrators,
		nil,
		nil,
		nil,
	); err != nil {
		t.Skipf("changing owner is unavailable: %v", err)
	}
	if _, err := openSecureRootFile(root, "wrong-owner", os.O_RDONLY, 0); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("wrong owner error = %v, want ErrInsecureState", err)
	}
}

func TestWindowsSecureFileRejectsSymlinkAndJunction(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "target"), []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target", filepath.Join(dir, "alias")); err != nil {
			t.Skipf("file symlink unavailable: %v", err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if _, err := openSecureRootFile(root, "alias", os.O_RDONLY, 0); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("symlink error = %v, want ErrInsecureState", err)
		}
	})

	t.Run("junction", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		alias := filepath.Join(dir, DirectoryName)
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput(); err != nil {
			t.Skipf("junction unavailable: %v (%s)", err, output)
		}
		if _, err := Open(filepath.Join(dir, "config.json")); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("junction error = %v, want ErrInsecureState", err)
		}
	})
}

func TestWindowsSecureFileRejectsHardlink(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openSecureRootFile(root, "secret", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "secret"), filepath.Join(dir, "alias")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if _, err := openSecureRootFile(root, "secret", os.O_RDONLY, 0); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("hardlink error = %v, want ErrInsecureState", err)
	}
}

func TestWindowsPublicationRemainsBoundAcrossParentAndStateRenames(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		base := t.TempDir()
		parentPath := filepath.Join(base, "parent")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		root, err := openPinnedRoot(parentPath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		directory, err := root.Open(".")
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		createWindowsSecureTemp(t, root, "source", "parent-bound")

		movedParent := filepath.Join(base, "parent-moved")
		if err := renameWindowsDirectory(parentPath, base, "parent-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := publishExclusive(root, directory, "source", "published"); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, filepath.Join(movedParent, "published"), "parent-bound")
		if _, err := os.Stat(filepath.Join(parentPath, "published")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("publication followed rebound parent: %v", err)
		}
	})

	t.Run("state", func(t *testing.T) {
		base := t.TempDir()
		statePath := filepath.Join(base, "state")
		parent, err := openPinnedRoot(base)
		if err != nil {
			t.Fatal(err)
		}
		if err := createSecureRootDirectory(parent, "state"); err != nil {
			_ = parent.Close()
			t.Fatal(err)
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
		root, err := openPinnedRoot(statePath)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		directory, err := root.Open(".")
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		createWindowsSecureTemp(t, root, "source", "state-bound")

		movedState := filepath.Join(base, "state-moved")
		if err := renameWindowsDirectory(statePath, base, "state-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(statePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := publishExclusive(root, directory, "source", "published"); err != nil {
			t.Fatal(err)
		}
		assertFileContents(t, filepath.Join(movedState, "published"), "state-bound")
		if _, err := os.Stat(filepath.Join(statePath, "published")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("publication followed rebound state directory: %v", err)
		}
	})
}

func TestWindowsExclusiveAndReplacePublicationSemantics(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	createWindowsSecureTemp(t, root, "first", "first")
	if err := publishExclusive(root, directory, "first", "target"); err != nil {
		t.Fatal(err)
	}
	assertWindowsLinkCount(t, filepath.Join(dir, "target"), 1)

	createWindowsSecureTemp(t, root, "second", "second")
	if err := publishExclusive(root, directory, "second", "target"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive publish error = %v, want fs.ErrExist", err)
	}
	assertFileContents(t, filepath.Join(dir, "target"), "first")
	assertWindowsLinkCount(t, filepath.Join(dir, "second"), 1)

	if err := replaceRootFile(root, directory, "second", "target"); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, filepath.Join(dir, "target"), "second")
	assertWindowsLinkCount(t, filepath.Join(dir, "target"), 1)
}

func createWindowsSecureTemp(t *testing.T, root *os.Root, name, contents string) {
	t.Helper()
	file, err := openSecureRootFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertWindowsExactACL(t *testing.T, file *os.File, directory, protected bool) {
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
	if got := control&windows.SE_DACL_PROTECTED != 0; got != protected {
		t.Fatalf("DACL protected = %v, want %v", got, protected)
	}
	trusted, err := windowsTrustedSIDs()
	if err != nil {
		t.Fatal(err)
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || defaulted || !owner.Equals(trusted[0]) {
		t.Fatalf("owner = %v defaulted=%v, want current user %s", owner, defaulted, trusted[0])
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("DACL = %v defaulted=%v", dacl, defaulted)
	}
	if int(dacl.AceCount) != len(trusted) {
		t.Fatalf("DACL ACEs=%d, want %d", dacl.AceCount, len(trusted))
	}
	seen := make(map[string]bool, len(trusted))
	for _, sid := range trusted {
		seen[sid.String()] = false
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatal(err)
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		} else if !protected {
			wantFlags = windows.INHERITED_ACE
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != wantFlags || ace.Mask != windowsFullControl {
			t.Fatalf("ACE %d type/flags/mask = %v/%#x/%#x", index, ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		present, ok := seen[sid.String()]
		if !ok || present {
			t.Fatalf("ACE %d has unexpected or duplicate SID %s", index, sid)
		}
		seen[sid.String()] = true
	}
	for sid, present := range seen {
		if !present {
			t.Fatalf("DACL omitted SID %s", sid)
		}
	}
}

func windowsACLControlAndCount(t *testing.T, path string) uint32 {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	return uint32(control)<<16 | uint32(dacl.AceCount)
}

func assertWindowsLinkCount(t *testing.T, path string, want uint32) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	if info.NumberOfLinks != want {
		t.Fatalf("%s link count = %d, want %d", path, info.NumberOfLinks, want)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s contents = %q, want %q", path, contents, want)
	}
}

func renameWindowsDirectory(source, destinationDirectory, target string) error {
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return fmt.Errorf("open rename source: %w", err)
	}
	sourceHandle, err := windows.CreateFile(
		sourceName,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open rename source: %w", err)
	}
	defer windows.CloseHandle(sourceHandle)
	directoryName, err := windows.UTF16PtrFromString(destinationDirectory)
	if err != nil {
		return fmt.Errorf("encode rename destination: %w", err)
	}
	directoryHandle, err := windows.CreateFile(
		directoryName,
		windows.FILE_TRAVERSE|windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open rename destination: %w", err)
	}
	defer windows.CloseHandle(directoryHandle)
	if err := ntRenameRelative(sourceHandle, directoryHandle, target, false); err != nil {
		return fmt.Errorf("rename by handle: %w", err)
	}
	return nil
}

func renameOpenDirectoryForTest(source, target string) error {
	return renameWindowsDirectory(source, filepath.Dir(target), filepath.Base(target))
}

func openAncestorRenameBlockedForTest(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

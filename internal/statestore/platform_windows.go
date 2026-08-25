//go:build windows

package statestore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsFullControl      windows.ACCESS_MASK = 0x001f01ff
	fileRenameInformationEx                     = 65
)

var lcMapStringEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("LCMapStringEx")

var reOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

type fileCaseSensitiveInfo struct {
	Flags uint32
}

type fileRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func stableIdentityKey(parent *os.File, leaf string) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(parent.Fd()), &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fmt.Errorf("statestore parent handle is not a directory")
	}
	return strconv.FormatUint(uint64(info.VolumeSerialNumber), 16) + ":" +
		strconv.FormatUint(uint64(info.FileIndexHigh), 16) + ":" +
		strconv.FormatUint(uint64(info.FileIndexLow), 16) + ":" +
		strings.ToLower(leaf), nil
}

func normalizedConfigName(root *os.Root, name string) (string, error) {
	directory, err := root.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return "", err
	}
	defer directory.Close()

	caseSensitive, err := windowsDirectoryCaseSensitive(directory)
	if err != nil {
		return "", err
	}
	if caseSensitive {
		return name, nil
	}
	return windowsCaseFold(name)
}

func windowsDirectoryCaseSensitive(directory *os.File) (bool, error) {
	var info fileCaseSensitiveInfo
	err := windows.GetFileInformationByHandleEx(
		windows.Handle(directory.Fd()),
		windows.FileCaseSensitiveInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err == nil {
		return info.Flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR != 0, nil
	}
	if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) &&
		!errors.Is(err, windows.ERROR_NOT_SUPPORTED) &&
		!errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED) {
		return false, err
	}
	return false, nil
}

func reservedComponent(name string) bool {
	return strings.EqualFold(name, DirectoryName)
}

func openPinnedRoot(path string) (*os.Root, error) {
	volume := filepath.VolumeName(path)
	if volume == "" || !filepath.IsAbs(path) {
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	volumePath := volume + string(os.PathSeparator)
	remainder := strings.TrimLeft(strings.TrimPrefix(path, volume), `\/`)
	if remainder == "" {
		// A volume root cannot be rebound beneath another directory, so the
		// top-level OpenRoot sharing limitation is immaterial in this case.
		return os.OpenRoot(volumePath)
	}
	volumeRoot, err := os.OpenRoot(volumePath)
	if err != nil {
		return nil, err
	}
	root, openErr := volumeRoot.OpenRoot(remainder)
	closeErr := volumeRoot.Close()
	if openErr != nil {
		return nil, openErr
	}
	if closeErr != nil {
		_ = root.Close()
		return nil, closeErr
	}
	return root, nil
}

func secureRootDirectory(root *os.Root, diagnosticPath string, _ bool) error {
	directory, err := root.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := validateWindowsDirectoryObject(directory, diagnosticPath); err != nil {
		return err
	}
	if err := validateWindowsOwnerOnlyACL(directory, diagnosticPath, true); err != nil {
		return err
	}
	return validateWindowsDirectoryObject(directory, diagnosticPath)
}

func secureLegacyIdentityRootDirectory(root *os.Root, diagnosticPath string) error {
	directory, err := root.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := validateWindowsDirectoryObject(directory, diagnosticPath); err != nil {
		return err
	}
	if err := validateWindowsLegacyIdentityACL(directory, diagnosticPath, true); err != nil {
		return err
	}
	return validateWindowsDirectoryObject(directory, diagnosticPath)
}

func openLegacyIdentityRootFile(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, insecureWindowsState(name, "reparse points are not allowed")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return nil, err
	}
	fail := func(failure error) (*os.File, error) {
		_ = file.Close()
		return nil, failure
	}
	opened, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if !os.SameFile(before, opened) {
		return fail(insecureWindowsState(name, "file changed while opening"))
	}
	if err := validateWindowsFileObject(file, name); err != nil {
		return fail(err)
	}
	if err := validateWindowsLegacyIdentityACL(file, name, false); err != nil {
		return fail(err)
	}
	if err := validateWindowsFileObject(file, name); err != nil {
		return fail(err)
	}
	return file, nil
}

func openSecureRootFile(root *os.Root, name string, flag int, perm fs.FileMode) (*os.File, error) {
	before, lstatErr := root.Lstat(name)
	if lstatErr == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, insecureWindowsState(name, "reparse points are not allowed")
	}
	if lstatErr != nil && !errors.Is(lstatErr, fs.ErrNotExist) {
		return nil, lstatErr
	}
	var file *os.File
	var err error
	created := false
	if flag&os.O_CREATE != 0 {
		file, created, err = openWindowsRootFile(root, name, flag, perm)
	} else {
		file, err = root.OpenFile(name, flag|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), perm)
	}
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, insecureWindowsState(name, "reparse points are not allowed")
		}
		return nil, err
	}
	fail := func(failure error) (*os.File, error) {
		_ = file.Close()
		if created {
			_ = root.Remove(name)
		}
		return nil, failure
	}
	if !created && lstatErr == nil {
		opened, statErr := file.Stat()
		if statErr != nil {
			return fail(statErr)
		}
		if !os.SameFile(before, opened) {
			return fail(insecureWindowsState(name, "file changed while opening"))
		}
	}
	if err := validateWindowsFileObject(file, name); err != nil {
		return fail(err)
	}
	if err := validateWindowsOwnerOnlyACL(file, name, false); err != nil {
		return fail(err)
	}
	if err := validateWindowsFileObject(file, name); err != nil {
		return fail(err)
	}
	if flag&os.O_TRUNC != 0 && !created {
		if err := file.Truncate(0); err != nil {
			return fail(err)
		}
	}
	return file, nil
}

func createSecureRootDirectory(parent *os.Root, name string) error {
	if !directRootName(name) {
		return &os.PathError{Op: "mkdirat", Path: name, Err: fs.ErrInvalid}
	}
	directory, err := parent.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	descriptor, err := windowsOwnerOnlyDescriptor(true)
	if err != nil {
		return err
	}
	handle, _, err := ntCreateRootObject(
		directory,
		name,
		windows.FILE_GENERIC_READ,
		windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE,
		descriptor,
	)
	if err != nil {
		return &os.PathError{Op: "mkdirat", Path: name, Err: err}
	}
	return windows.CloseHandle(handle)
}

func openWindowsRootFile(root *os.Root, name string, flag int, perm fs.FileMode) (*os.File, bool, error) {
	if !directRootName(name) || flag&os.O_APPEND != 0 {
		return nil, false, &os.PathError{Op: "openat", Path: name, Err: fs.ErrInvalid}
	}
	directory, err := root.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	descriptor, err := windowsOwnerOnlyDescriptor(false)
	if err != nil {
		return nil, false, err
	}
	access := uint32(windows.FILE_GENERIC_READ)
	switch flag & (os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		access = windows.FILE_GENERIC_WRITE
	case os.O_RDWR:
		access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
	}
	disposition := uint32(windows.FILE_OPEN_IF)
	if flag&os.O_EXCL != 0 {
		disposition = windows.FILE_CREATE
	}
	handle, created, err := ntCreateRootObject(
		directory,
		name,
		access,
		disposition,
		windows.FILE_NON_DIRECTORY_FILE,
		descriptor,
	)
	if err != nil {
		return nil, false, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	file := os.NewFile(uintptr(handle), filepath.Join(root.Name(), name))
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, false, &os.PathError{Op: "openat", Path: name, Err: fs.ErrInvalid}
	}
	_ = perm // Windows permissions are represented by the explicit descriptor.
	return file, created, nil
}

func ntCreateRootObject(
	directory *os.File,
	name string,
	access, disposition, typeOption uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windows.Handle, bool, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, false, err
	}
	caseSensitive, err := windowsDirectoryCaseSensitive(directory)
	if err != nil {
		return windows.InvalidHandle, false, err
	}
	objectAttributes := uint32(windows.OBJ_DONT_REPARSE)
	if !caseSensitive {
		objectAttributes |= windows.OBJ_CASE_INSENSITIVE
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory:      windows.Handle(directory.Fd()),
		ObjectName:         objectName,
		Attributes:         objectAttributes,
		SecurityDescriptor: descriptor,
	}
	var handle windows.Handle
	status := &windows.IO_STATUS_BLOCK{}
	err = windows.NtCreateFile(
		&handle,
		access|windows.SYNCHRONIZE,
		attributes,
		status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		typeOption|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT,
		0,
		0,
	)
	runtime.KeepAlive(directory)
	if err != nil {
		if err == windows.STATUS_OBJECT_NAME_COLLISION || err == windows.STATUS_OBJECT_NAME_EXISTS {
			return windows.InvalidHandle, false, windows.ERROR_ALREADY_EXISTS
		}
		if status, ok := err.(windows.NTStatus); ok {
			return windows.InvalidHandle, false, status.Errno()
		}
		return windows.InvalidHandle, false, err
	}
	const fileCreated = 2
	return handle, status.Information == fileCreated, nil
}

func publishExclusive(root *os.Root, directory *os.File, source, target string) error {
	return renameRootFile(root, directory, source, target, false)
}

func replaceRootFile(root *os.Root, directory *os.File, source, target string) error {
	if targetFile, err := openSecureRootFile(root, target, os.O_RDONLY, 0); err == nil {
		if closeErr := targetFile.Close(); closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return renameRootFile(root, directory, source, target, true)
}

func renameRootFile(root *os.Root, directory *os.File, source, target string, replace bool) error {
	if !directRootName(source) || !directRootName(target) || strings.EqualFold(source, target) {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: fs.ErrInvalid}
	}
	if err := validateRootDirectoryBinding(root, directory); err != nil {
		return err
	}

	sourceFile, err := openSecureRootFile(root, source, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	renameHandle, err := reopenWindowsHandle(
		sourceFile,
		windows.DELETE|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
	)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: err}
	}
	defer windows.CloseHandle(renameHandle)
	if err := validateSameWindowsObject(sourceFile, renameHandle, false); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: err}
	}

	if err := ntRenameRelative(renameHandle, windows.Handle(directory.Fd()), target, replace); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: target, Err: err}
	}
	runtime.KeepAlive(directory)
	return nil
}

func syncRoot(root *os.Root) error {
	directory, err := root.OpenFile(".", os.O_RDONLY|int(windows.O_FILE_FLAG_OPEN_REPARSE_POINT), 0)
	if err != nil {
		return err
	}
	defer directory.Close()
	return syncDirectory(directory)
}

func syncDirectory(directory *os.File) error {
	if directory == nil {
		return fs.ErrInvalid
	}
	if _, err := directory.Stat(); err != nil {
		return err
	}
	// Windows has no supported directory-fsync operation: FlushFileBuffers is
	// for file/volume handles and fails for directory handles. Callers flush the
	// source file before the atomic NT rename, so there is no further portable
	// directory flush to issue here.
	return nil
}

func directRootName(name string) bool {
	return name != "" && name != "." && filepath.IsLocal(name) && filepath.Base(name) == name
}

func validateRootDirectoryBinding(root *os.Root, directory *os.File) error {
	if root == nil || directory == nil {
		return fs.ErrInvalid
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return err
	}
	directoryInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || !directoryInfo.IsDir() || !os.SameFile(rootInfo, directoryInfo) {
		return fmt.Errorf("%w: publication directory does not match root", ErrInsecureState)
	}
	return validateWindowsDirectoryObject(directory, directory.Name())
}

func validateWindowsDirectoryObject(directory *os.File, path string) error {
	tagInfo, err := windowsAttributeTagInfo(directory)
	if err != nil {
		return err
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return insecureWindowsState(path, "reparse points are not allowed")
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return insecureWindowsState(path, "not a directory")
	}
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return insecureWindowsState(path, "not a directory")
	}
	return nil
}

func validateWindowsFileObject(file *os.File, path string) error {
	tagInfo, err := windowsAttributeTagInfo(file)
	if err != nil {
		return err
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return insecureWindowsState(path, "reparse points are not allowed")
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return insecureWindowsState(path, "not a regular file")
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &handleInfo); err != nil {
		return err
	}
	if handleInfo.NumberOfLinks != 1 {
		return insecureWindowsState(path, fmt.Sprintf("link count is %d, want 1", handleInfo.NumberOfLinks))
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return insecureWindowsState(path, "not a regular file")
	}
	return nil
}

func windowsAttributeTagInfo(file *os.File) (fileAttributeTagInfo, error) {
	var info fileAttributeTagInfo
	err := windows.GetFileInformationByHandleEx(
		windows.Handle(file.Fd()),
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return info, err
}

func windowsOwnerOnlyDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	trusted, err := windowsTrustedSIDs()
	if err != nil {
		return nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	var aces strings.Builder
	for _, sid := range trusted {
		fmt.Fprintf(&aces, "(A;%s;GA;;;%s)", flags, sid.String())
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + trusted[0].String() + "D:P" + aces.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("build protected security descriptor: %w", err)
	}
	return descriptor, nil
}

func windowsCaseFold(name string) (string, error) {
	source, err := windows.UTF16FromString(name)
	if err != nil {
		return "", err
	}
	invariantLocale, err := windows.UTF16PtrFromString("")
	if err != nil {
		return "", err
	}
	const lcmapUppercase = 0x00000200
	length, _, callErr := lcMapStringEx.Call(
		uintptr(unsafe.Pointer(invariantLocale)),
		lcmapUppercase,
		uintptr(unsafe.Pointer(&source[0])),
		uintptr(len(source)-1),
		0,
		0,
		0,
		0,
		0,
	)
	if length == 0 {
		return "", callErr
	}
	destination := make([]uint16, int(length)+1)
	length, _, callErr = lcMapStringEx.Call(
		uintptr(unsafe.Pointer(invariantLocale)),
		lcmapUppercase,
		uintptr(unsafe.Pointer(&source[0])),
		uintptr(len(source)-1),
		uintptr(unsafe.Pointer(&destination[0])),
		uintptr(len(destination)),
		0,
		0,
		0,
	)
	if length == 0 {
		return "", callErr
	}
	return syscall.UTF16ToString(destination[:length]), nil
}

func validateWindowsOwnerOnlyACL(file *os.File, path string, directory bool) error {
	return validateWindowsOwnerOnlyACLPolicy(file, path, directory, true)
}

func validateWindowsOwnerOnlyACLPolicy(file *os.File, path string, directory, requireCurrentOwner bool) error {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if descriptor == nil || !descriptor.IsValid() {
		return insecureWindowsState(path, "security descriptor is missing or invalid")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	protected := control&windows.SE_DACL_PROTECTED != 0
	if directory && !protected {
		return insecureWindowsState(path, "directory DACL inheritance is enabled")
	}

	trusted, err := windowsTrustedSIDs()
	if err != nil {
		return err
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil {
		return err
	}
	ownerTrusted := false
	if owner != nil && !ownerDefaulted {
		for index, trustedSID := range trusted {
			if trustedSID.Equals(owner) && (!requireCurrentOwner || index == 0) {
				ownerTrusted = true
				break
			}
		}
	}
	if !ownerTrusted {
		if requireCurrentOwner {
			return insecureWindowsState(path, "owner is not explicitly the current user")
		}
		return insecureWindowsState(path, "owner is not an allowed legacy principal")
	}
	dacl, daclDefaulted, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || daclDefaulted {
		return insecureWindowsState(path, "DACL is missing or defaulted")
	}
	if int(dacl.AceCount) != len(trusted) {
		return insecureWindowsState(path, fmt.Sprintf("DACL has %d ACEs, want %d", dacl.AceCount, len(trusted)))
	}

	seen := make([]bool, len(trusted))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d is not an allow ACE", index))
		}
		wantFlags := uint8(0)
		if directory {
			wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		} else if !protected {
			wantFlags = windows.INHERITED_ACE
		}
		if ace.Header.AceFlags != wantFlags {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d flags are %#x, want %#x", index, ace.Header.AceFlags, wantFlags))
		}
		if ace.Mask != windowsFullControl {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d mask is %#x, want %#x", index, ace.Mask, windowsFullControl))
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid == nil || !sid.IsValid() {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d has an invalid SID", index))
		}
		wantSize := unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart) + uintptr(sid.Len())
		if uintptr(ace.Header.AceSize) != wantSize {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d has a non-canonical size", index))
		}
		matched := -1
		for trustedIndex, trustedSID := range trusted {
			if trustedSID.Equals(sid) {
				matched = trustedIndex
				break
			}
		}
		if matched < 0 {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d grants an unexpected SID", index))
		}
		if seen[matched] {
			return insecureWindowsState(path, fmt.Sprintf("ACE %d duplicates an allowed SID", index))
		}
		seen[matched] = true
	}
	for index, present := range seen {
		if !present {
			return insecureWindowsState(path, "DACL omits allowed SID "+trusted[index].String())
		}
	}
	return nil
}

func validateWindowsLegacyIdentityACL(file *os.File, path string, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if descriptor == nil || !descriptor.IsValid() {
		return insecureWindowsState(path, "legacy security descriptor is missing or invalid")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return insecureWindowsState(path, "legacy DACL inheritance is enabled")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return insecureWindowsState(path, "legacy DACL is missing")
	}
	trusted, err := windowsTrustedSIDs()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	ownerTrusted := false
	for _, trustedSID := range trusted {
		if owner != nil && trustedSID.Equals(owner) {
			ownerTrusted = true
			break
		}
	}
	if !ownerTrusted {
		return insecureWindowsState(path, "legacy owner is not an allowed principal")
	}

	seenEffective := make([]bool, len(trusted))
	seenInheritedByChildren := make([]bool, len(trusted))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return insecureWindowsState(path, fmt.Sprintf("legacy ACE %d is not an explicit allow ACE", index))
		}
		const required = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE |
			windows.FILE_GENERIC_EXECUTE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&required != required {
			return insecureWindowsState(path, fmt.Sprintf("legacy ACE %d lacks full owner access", index))
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matched := -1
		for trustedIndex, trustedSID := range trusted {
			if trustedSID.Equals(sid) {
				matched = trustedIndex
				break
			}
		}
		if matched < 0 {
			return insecureWindowsState(path, fmt.Sprintf("legacy ACE %d grants an unexpected SID", index))
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
			seenEffective[matched] = true
		}
		if ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) ==
			windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
			seenInheritedByChildren[matched] = true
		}
	}
	for index := range trusted {
		if !seenEffective[index] {
			return insecureWindowsState(path, "legacy DACL omits effective access for "+trusted[index].String())
		}
		if directory && !seenInheritedByChildren[index] {
			return insecureWindowsState(path, "legacy DACL omits child inheritance for "+trusted[index].String())
		}
	}
	return nil
}

func windowsTrustedSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current user SID: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("create SYSTEM SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("create Administrators SID: %w", err)
	}
	trusted := make([]*windows.SID, 0, 3)
	for _, sid := range []*windows.SID{user.User.Sid, system, administrators} {
		duplicate := false
		for _, existing := range trusted {
			if existing.Equals(sid) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			trusted = append(trusted, sid)
		}
	}
	return trusted, nil
}

func reopenWindowsHandle(file *os.File, access, attributes uint32) (windows.Handle, error) {
	handle, _, callErr := reOpenFile.Call(
		file.Fd(),
		uintptr(access),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(attributes),
	)
	runtime.KeepAlive(file)
	if windows.Handle(handle) == windows.InvalidHandle {
		return windows.InvalidHandle, callErr
	}
	return windows.Handle(handle), nil
}

func validateSameWindowsObject(file *os.File, handle windows.Handle, directory bool) error {
	var original, reopened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &original); err != nil {
		return err
	}
	if err := windows.GetFileInformationByHandle(handle, &reopened); err != nil {
		return err
	}
	wantDirectory := uint32(0)
	if directory {
		wantDirectory = windows.FILE_ATTRIBUTE_DIRECTORY
	}
	if original.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		reopened.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		original.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != wantDirectory ||
		reopened.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != wantDirectory ||
		original.VolumeSerialNumber != reopened.VolumeSerialNumber ||
		original.FileIndexHigh != reopened.FileIndexHigh ||
		original.FileIndexLow != reopened.FileIndexLow {
		return fmt.Errorf("%w: object changed while acquiring security rights", ErrInsecureState)
	}
	runtime.KeepAlive(file)
	return nil
}

func ntRenameRelative(source, directory windows.Handle, target string, replace bool) error {
	name, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	name = name[:len(name)-1]
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + len(name)*2
	if minimum := int(unsafe.Sizeof(layout)); bufferSize < minimum {
		bufferSize = minimum
	}
	buffer := make([]byte, bufferSize)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_POSIX_SEMANTICS
	if replace {
		info.Flags |= windows.FILE_RENAME_REPLACE_IF_EXISTS
	}
	info.RootDirectory = directory
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)

	status := windows.NtSetInformationFile(
		source,
		&windows.IO_STATUS_BLOCK{},
		&buffer[0],
		uint32(len(buffer)),
		fileRenameInformationEx,
	)
	if status == windows.STATUS_INVALID_INFO_CLASS || status == windows.STATUS_INVALID_PARAMETER || status == windows.STATUS_NOT_SUPPORTED {
		if replace {
			info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS
		} else {
			info.Flags = 0
		}
		status = windows.NtSetInformationFile(
			source,
			&windows.IO_STATUS_BLOCK{},
			&buffer[0],
			uint32(len(buffer)),
			windows.FileRenameInformation,
		)
	}
	if status == nil {
		return nil
	}
	if status == windows.STATUS_OBJECT_NAME_COLLISION || status == windows.STATUS_OBJECT_NAME_EXISTS {
		return windows.ERROR_ALREADY_EXISTS
	}
	if ntstatus, ok := status.(windows.NTStatus); ok {
		return ntstatus.Errno()
	}
	return status
}

func insecureWindowsState(path, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInsecureState, path, reason)
}

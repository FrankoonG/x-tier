//go:build windows

package configstore

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protectedFileFullControl windows.ACCESS_MASK = 0x1f01ff

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func openSecureConfigFile(path string) (*os.File, error) {
	return openWindowsSecureFile(
		path,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
	)
}

func openWindowsSecureFile(path string, access, share uint32) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		name,
		access,
		share,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	if err := validateWindowsSecureFile(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func createWindowsProtectedFile(path string, access, share, attributes uint32) (*os.File, error) {
	descriptor, err := protectedFileSecurityDescriptor()
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	securityAttributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		name,
		access,
		share,
		&securityAttributes,
		windows.CREATE_NEW,
		attributes,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	if err := validateWindowsSecureFile(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateWindowsSecureFile(file *os.File, path string) error {
	handle := windows.Handle(file.Fd())
	var tagInfo fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tagInfo)),
		uint32(unsafe.Sizeof(tagInfo)),
	); err != nil {
		return err
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return insecureFileError(path, "reparse points are not allowed")
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return insecureFileError(path, "not a regular file")
	}

	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		return err
	}
	if fileInfo.NumberOfLinks != 1 {
		return insecureFileError(path, fmt.Sprintf("link count is %d, want 1", fileInfo.NumberOfLinks))
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return insecureFileError(path, "not a regular file")
	}

	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return insecureFileError(path, "DACL inheritance is enabled")
	}

	allowed, err := protectedFileAllowedSIDs()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(allowed[0]) {
		return insecureFileError(path, "owner is not the current user")
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || defaulted {
		return insecureFileError(path, "DACL is missing or defaulted")
	}

	expected := make(map[string]bool, len(allowed))
	for _, sid := range allowed {
		expected[sid.String()] = false
	}
	if int(dacl.AceCount) != len(expected) {
		return insecureFileError(path, fmt.Sprintf("DACL has %d ACEs, want %d", dacl.AceCount, len(expected)))
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return insecureFileError(path, fmt.Sprintf("ACE %d is not an allow ACE", index))
		}
		if ace.Header.AceFlags != 0 {
			return insecureFileError(path, fmt.Sprintf("ACE %d has inheritance flags", index))
		}
		if ace.Mask != protectedFileFullControl {
			return insecureFileError(path, fmt.Sprintf("ACE %d does not grant exact full control", index))
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid == nil || !sid.IsValid() {
			return insecureFileError(path, fmt.Sprintf("ACE %d has an invalid SID", index))
		}
		key := sid.String()
		seen, ok := expected[key]
		if !ok {
			return insecureFileError(path, fmt.Sprintf("ACE %d grants an unexpected SID", index))
		}
		if seen {
			return insecureFileError(path, fmt.Sprintf("ACE %d duplicates an allowed SID", index))
		}
		expected[key] = true
	}
	for sid, seen := range expected {
		if !seen {
			return insecureFileError(path, "DACL omits allowed SID "+sid)
		}
	}
	return nil
}

func protectedFileSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	allowed, err := protectedFileAllowedSIDs()
	if err != nil {
		return nil, err
	}
	var aces strings.Builder
	for _, sid := range allowed {
		fmt.Fprintf(&aces, "(A;;GA;;;%s)", sid.String())
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + allowed[0].String() + "D:P" + aces.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("build protected DACL: %w", err)
	}
	return descriptor, nil
}

func protectedFileAllowedSIDs() ([]*windows.SID, error) {
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

	allowed := make([]*windows.SID, 0, 3)
	for _, sid := range []*windows.SID{user.User.Sid, system, administrators} {
		duplicate := false
		for _, existing := range allowed {
			if existing.Equals(sid) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			allowed = append(allowed, sid)
		}
	}
	return allowed, nil
}

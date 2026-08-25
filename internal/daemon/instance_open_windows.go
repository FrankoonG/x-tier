//go:build windows

package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const instanceLockFullControl windows.ACCESS_MASK = 0x001f01ff

type instanceLockAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func openInstanceLock(path string) (*os.File, error) {
	descriptor, err := instanceLockSecurityDescriptor()
	if err != nil {
		return nil, instanceLockPathError(path, err)
	}
	securityAttributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, instanceLockPathError(path, err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&securityAttributes,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, instanceLockPathError(path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, instanceLockPathError(path, fs.ErrInvalid)
	}
	if err := validateInstanceLock(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateInstanceLock(file *os.File, path string) error {
	handle := windows.Handle(file.Fd())
	var tagInfo instanceLockAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tagInfo)),
		uint32(unsafe.Sizeof(tagInfo)),
	); err != nil {
		return instanceLockPathError(path, err)
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return insecureInstanceLockError(path, "reparse points are not allowed")
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return insecureInstanceLockError(path, "not a regular file")
	}

	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		return instanceLockPathError(path, err)
	}
	if handleInfo.NumberOfLinks != 1 {
		return insecureInstanceLockError(path, fmt.Sprintf("link count is %d, want 1", handleInfo.NumberOfLinks))
	}
	info, err := file.Stat()
	if err != nil {
		return instanceLockPathError(path, err)
	}
	if !info.Mode().IsRegular() {
		return insecureInstanceLockError(path, "not a regular file")
	}

	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return instanceLockPathError(path, err)
	}
	if err := validateInstanceLockDescriptor(descriptor); err != nil {
		return insecureInstanceLockError(path, err.Error())
	}
	return nil
}

func validateInstanceLockDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	if descriptor == nil || !descriptor.IsValid() {
		return fmt.Errorf("security descriptor is missing or invalid")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read security descriptor control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("DACL inheritance is enabled")
	}

	trusted, err := instanceLockTrustedSIDs()
	if err != nil {
		return err
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read owner: %w", err)
	}
	if owner == nil || ownerDefaulted || !owner.Equals(trusted[0]) {
		return fmt.Errorf("owner is not explicitly the current user")
	}

	dacl, daclDefaulted, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read DACL: %w", err)
	}
	if dacl == nil || daclDefaulted {
		return fmt.Errorf("DACL is missing or defaulted")
	}
	if int(dacl.AceCount) != len(trusted) {
		return fmt.Errorf("DACL has %d ACEs, want %d", dacl.AceCount, len(trusted))
	}

	seen := make([]bool, len(trusted))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return fmt.Errorf("read ACE %d: %w", index, err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("ACE %d is not an allow ACE", index)
		}
		if ace.Header.AceFlags != 0 {
			return fmt.Errorf("ACE %d has inheritance flags", index)
		}
		if ace.Mask != instanceLockFullControl {
			return fmt.Errorf("ACE %d mask is %#x, want %#x", index, ace.Mask, instanceLockFullControl)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid == nil || !sid.IsValid() {
			return fmt.Errorf("ACE %d has an invalid SID", index)
		}
		wantSize := unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart) + uintptr(sid.Len())
		if uintptr(ace.Header.AceSize) != wantSize {
			return fmt.Errorf("ACE %d has a non-canonical size", index)
		}
		matched := -1
		for trustedIndex, trustedSID := range trusted {
			if trustedSID.Equals(sid) {
				matched = trustedIndex
				break
			}
		}
		if matched < 0 {
			return fmt.Errorf("ACE %d grants an unexpected SID", index)
		}
		if seen[matched] {
			return fmt.Errorf("ACE %d duplicates an allowed SID", index)
		}
		seen[matched] = true
	}
	for index, present := range seen {
		if !present {
			return fmt.Errorf("DACL omits allowed SID %s", trusted[index].String())
		}
	}
	return nil
}

func instanceLockSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	trusted, err := instanceLockTrustedSIDs()
	if err != nil {
		return nil, err
	}
	var aces strings.Builder
	for _, sid := range trusted {
		fmt.Fprintf(&aces, "(A;;GA;;;%s)", sid.String())
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + trusted[0].String() + "D:P" + aces.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("build protected lock DACL: %w", err)
	}
	return descriptor, nil
}

func instanceLockTrustedSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current user SID: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("create LocalSystem SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("create Builtin Administrators SID: %w", err)
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

func insecureInstanceLockError(path, reason string) error {
	return instanceLockPathError(path, fmt.Errorf("daemon.lock_insecure: %s", reason))
}

func instanceLockPathError(path string, err error) error {
	return &os.PathError{Op: "open", Path: path, Err: err}
}

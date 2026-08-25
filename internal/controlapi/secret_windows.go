//go:build windows

package controlapi

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const secretFileFullControl windows.ACCESS_MASK = 0x1f01ff

func createSecretFile(path string) (*os.File, error) {
	sd, err := secretSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_WRITE|windows.READ_CONTROL,
		0,
		&sa,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	if f == nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(path)
		return nil, fmt.Errorf("control.token_open_failed")
	}
	if err := validateSecretFile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

func readSecretFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	if f == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("control.token_open_failed")
	}
	defer f.Close()
	if err := validateSecretFile(f); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 4096))
}

func secretSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sids, err := trustedSecretSIDs()
	if err != nil {
		return nil, err
	}
	user := sids[0]
	var aces strings.Builder
	for _, sid := range sids {
		fmt.Fprintf(&aces, "(A;;GA;;;%s)", sid.String())
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.String() + "D:P" + aces.String())
	if err != nil {
		return nil, fmt.Errorf("control.token_acl_build: %w", err)
	}
	return sd, nil
}

func validateSecretFile(f *os.File) error {
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &fileInfo); err != nil {
		return fmt.Errorf("control.token_file_identity_read: %w", err)
	}
	if fileInfo.NumberOfLinks != 1 {
		return fmt.Errorf("control.token_link_count_invalid: %d", fileInfo.NumberOfLinks)
	}
	var tagInfo struct {
		FileAttributes uint32
		ReparseTag     uint32
	}
	if err := windows.GetFileInformationByHandleEx(
		windows.Handle(f.Fd()),
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tagInfo)),
		uint32(unsafe.Sizeof(tagInfo)),
	); err != nil {
		return fmt.Errorf("control.token_attributes_read: %w", err)
	}
	if tagInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("control.token_reparse_point_forbidden")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("control.token_not_regular")
	}
	sd, err := windows.GetSecurityInfo(
		windows.Handle(f.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("control.token_acl_read: %w", err)
	}
	return validateSecretDescriptor(sd)
}

func validateSecretDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	if sd == nil {
		return fmt.Errorf("control.token_acl_missing")
	}
	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("control.token_acl_control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("control.token_acl_inheritance_enabled")
	}

	trusted, err := trustedSecretSIDs()
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(trusted))
	for _, sid := range trusted {
		want[sid.String()] = false
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("control.token_owner_read: %w", err)
	}
	if owner == nil || !owner.Equals(trusted[0]) {
		return fmt.Errorf("control.token_owner_untrusted")
	}

	dacl, defaulted, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("control.token_dacl_missing: %w", err)
	}
	if defaulted {
		return fmt.Errorf("control.token_dacl_defaulted")
	}
	if int(dacl.AceCount) != len(want) {
		return fmt.Errorf("control.token_ace_count_invalid: %d", dacl.AceCount)
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return fmt.Errorf("control.token_ace_read: %w", err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("control.token_ace_type_invalid")
		}
		if ace.Header.AceFlags != 0 {
			return fmt.Errorf("control.token_ace_inherited")
		}
		if ace.Mask != secretFileFullControl {
			return fmt.Errorf("control.token_ace_permissions_invalid: %#x", ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid == nil || !sid.IsValid() {
			return fmt.Errorf("control.token_ace_sid_invalid")
		}
		key := sid.String()
		seen, ok := want[key]
		if !ok || seen {
			return fmt.Errorf("control.token_ace_principal_untrusted: %s", key)
		}
		want[key] = true
	}
	for sid, seen := range want {
		if !seen {
			return fmt.Errorf("control.token_ace_principal_missing: %s", sid)
		}
	}
	return nil
}

func trustedSecretSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("control.token_current_identity: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, fmt.Errorf("control.token_system_sid: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, fmt.Errorf("control.token_admin_sid: %w", err)
	}
	candidates := []*windows.SID{user.User.Sid, system, admins}
	trusted := make([]*windows.SID, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, sid := range candidates {
		key := sid.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		trusted = append(trusted, sid)
	}
	return trusted, nil
}

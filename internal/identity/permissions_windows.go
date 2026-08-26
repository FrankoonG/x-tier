//go:build windows

package identity

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var secretPublicationMu sync.Mutex

func withSecretPublicationLock(publish func() error) error {
	// Directory DACL propagation can race another creator securing its temporary
	// file. Production cross-process calls are serialized by configstore; this
	// closes the remaining in-process Windows permission race.
	secretPublicationMu.Lock()
	defer secretPublicationMu.Unlock()
	return publish()
}

func secureSecretDirectory(path string) error {
	return applyOwnerOnlyACL(path, nil, true)
}

func secureSecretFile(file *os.File) error {
	// os.CreateTemp does not request WRITE_DAC on its handle. The file lives in
	// an already protected directory, so set the DACL by its unguessable name.
	return applyOwnerOnlyACL(file.Name(), nil, false)
}

func applyOwnerOnlyACL(path string, file *os.File, directory bool) error {
	allowed, err := secretAllowedSIDs()
	if err != nil {
		return err
	}
	inheritance := uint32(0)
	if directory {
		inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(allowed))
	for _, sid := range allowed {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION |
		windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if file != nil {
		return windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, information, nil, nil, acl, nil)
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, acl, nil)
}

func validateSecretPath(path string, file *os.File, _ fs.FileInfo) error {
	if err := validateOwnerOnlyACL(file, false); err != nil {
		return fmt.Errorf("seed file ACL: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := validateOwnerOnlyACL(directory, true); err != nil {
		return fmt.Errorf("seed directory ACL: %w", err)
	}
	return nil
}

func validatePublishedSecret(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validateSecretPath(path, file, info)
}

func validateOwnerOnlyACL(file *os.File, directory bool) error {
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if descriptor == nil {
		return fmt.Errorf("security descriptor is missing")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("DACL inheritance is enabled")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("DACL is missing")
	}
	allowed, err := secretAllowedSIDs()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !sidInSet(owner, allowed) {
		return fmt.Errorf("owner SID is not an allowed principal")
	}
	seenEffective := make([]bool, len(allowed))
	seenInheritedByChildren := make([]bool, len(allowed))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return fmt.Errorf("DACL entry %d is not an explicit allow ACE", index)
		}
		const required = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE |
			windows.FILE_GENERIC_EXECUTE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&required != required {
			return fmt.Errorf("DACL entry %d mask %#x lacks required %#x", index, ace.Mask, required)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matched := false
		for allowedIndex, candidate := range allowed {
			if candidate.Equals(sid) {
				if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
					seenEffective[allowedIndex] = true
				}
				if ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == (windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE) {
					seenInheritedByChildren[allowedIndex] = true
				}
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("DACL grants unexpected SID %s", sid.String())
		}
	}
	for index := range allowed {
		if !seenEffective[index] {
			return fmt.Errorf("DACL omits effective access for SID %s", allowed[index].String())
		}
		if directory && !seenInheritedByChildren[index] {
			return fmt.Errorf("DACL omits child inheritance for SID %s", allowed[index].String())
		}
	}
	return nil
}

func sidInSet(sid *windows.SID, set []*windows.SID) bool {
	for _, candidate := range set {
		if candidate.Equals(sid) {
			return true
		}
	}
	return false
}

func secretAllowedSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	result := make([]*windows.SID, 0, 3)
	for _, sid := range []*windows.SID{user.User.Sid, system, administrators} {
		duplicate := false
		for _, existing := range result {
			if existing.Equals(sid) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, sid)
		}
	}
	return result, nil
}

func syncSecretDirectory(string) error { return nil }

func publishSecretFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// No REPLACE_EXISTING flag: identity creation must remain exclusive.
	return windows.MoveFileEx(sourcePtr, targetPtr, windows.MOVEFILE_WRITE_THROUGH)
}

//go:build windows

package configstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

const (
	finalPathNameNormalized = 0
	finalPathVolumeNameDOS  = 0
)

func canonicalPath(path string) (string, error) {
	if err := validateWindowsPath(path); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if err := validateWindowsPath(absolute); err != nil {
		return "", err
	}

	if canonical, exists, err := canonicalSecureWindowsTarget(absolute); err != nil {
		return "", err
	} else if exists {
		return canonical, nil
	}

	parent, err := canonicalExistingWindowsParent(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	canonical := filepath.Join(parent, filepath.Base(absolute))
	if err := validateWindowsPath(canonical); err != nil {
		return "", err
	}

	// Recheck after resolving the parent so a target created concurrently still
	// receives the secure-file checks and handle-based canonicalization.
	if final, exists, err := canonicalSecureWindowsTarget(canonical); err != nil {
		return "", err
	} else if exists {
		return final, nil
	}
	return canonical, nil
}

func canonicalSecureWindowsTarget(path string) (canonical string, exists bool, err error) {
	file, err := openSecureConfigFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	canonical, err = finalDOSPath(windows.Handle(file.Fd()), path)
	closeErr := file.Close()
	if err != nil {
		return "", false, err
	}
	if closeErr != nil {
		return "", false, closeErr
	}
	return canonical, true, nil
}

func canonicalExistingWindowsParent(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 2)
	for {
		resolved, err := canonicalWindowsDirectory(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}

		// A dangling reparse point exists even though following it reports that
		// its destination is missing. It is not a missing path component.
		if _, statErr := os.Lstat(current); statErr == nil {
			return "", err
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return "", statErr
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canonicalWindowsDirectory(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
	}
	defer windows.CloseHandle(handle)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", &os.PathError{Op: "canonicalize", Path: path, Err: fs.ErrInvalid}
	}
	return finalDOSPath(handle, path)
}

func finalDOSPath(handle windows.Handle, path string) (string, error) {
	buffer := make([]uint16, 256)
	for {
		length, err := windows.GetFinalPathNameByHandle(
			handle,
			&buffer[0],
			uint32(len(buffer)),
			finalPathNameNormalized|finalPathVolumeNameDOS,
		)
		if err != nil {
			return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
		}
		if length < uint32(len(buffer)) {
			resolved := windows.UTF16ToString(buffer[:length])
			resolved, err = normalizeFinalDOSPath(resolved)
			if err != nil {
				return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
			}
			if err := validateWindowsPath(resolved); err != nil {
				return "", err
			}
			return filepath.Clean(resolved), nil
		}
		buffer = make([]uint16, length+1)
	}
}

func normalizeFinalDOSPath(path string) (string, error) {
	switch {
	case hasWindowsPrefixFold(path, `\\?\UNC\`):
		return `\\` + path[len(`\\?\UNC\`):], nil
	case hasWindowsPrefixFold(path, `\\?\`):
		path = path[len(`\\?\`):]
		if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' && isWindowsSeparator(path[2]) {
			return path, nil
		}
	case hasWindowsPrefixFold(path, `\??\UNC\`):
		return `\\` + path[len(`\??\UNC\`):], nil
	case hasWindowsPrefixFold(path, `\??\`):
		path = path[len(`\??\`):]
		if len(path) >= 3 && isASCIIAlpha(path[0]) && path[1] == ':' && isWindowsSeparator(path[2]) {
			return path, nil
		}
	case filepath.IsAbs(path):
		return path, nil
	}
	return "", fmt.Errorf("%w: GetFinalPathNameByHandle returned non-DOS path %q", fs.ErrInvalid, path)
}

func validateWindowsPath(path string) error {
	if !utf8.ValidString(path) {
		return invalidWindowsPath(path, "invalid UTF-8")
	}
	volumeComponents, remainder, err := splitWindowsVolume(path)
	if err != nil {
		return invalidWindowsPath(path, err.Error())
	}
	for _, component := range volumeComponents {
		if err := validateWindowsComponent(component); err != nil {
			return invalidWindowsPath(path, "invalid volume component "+component+": "+err.Error())
		}
	}
	for remainder != "" {
		component, rest := cutWindowsComponent(remainder)
		remainder = rest
		if component == "" {
			continue
		}
		if err := validateWindowsComponent(component); err != nil {
			return invalidWindowsPath(path, "invalid component "+component+": "+err.Error())
		}
	}
	return nil
}

func splitWindowsVolume(path string) (components []string, remainder string, err error) {
	if hasWindowsPrefixFold(path, `\\?\`) {
		rest := path[len(`\\?\`):]
		if hasWindowsPrefixFold(rest, `UNC\`) {
			return splitUNCVolume(rest[len(`UNC\`):])
		}
		if len(rest) >= 3 && isASCIIAlpha(rest[0]) && rest[1] == ':' && isWindowsSeparator(rest[2]) {
			return nil, rest[2:], nil
		}
		return nil, "", errors.New("unsupported extended path namespace")
	}
	if hasWindowsPrefixFold(path, `\\.\`) || hasWindowsPrefixFold(path, `\??\`) {
		return nil, "", errors.New("device path namespaces are not allowed")
	}
	if len(path) >= 2 && isWindowsSeparator(path[0]) && isWindowsSeparator(path[1]) {
		return splitUNCVolume(path[2:])
	}
	if len(path) >= 2 && path[1] == ':' {
		if !isASCIIAlpha(path[0]) {
			return nil, "", errors.New("invalid drive designator")
		}
		if len(path) == 2 || !isWindowsSeparator(path[2]) {
			return nil, "", errors.New("drive-relative paths are not allowed")
		}
		return nil, path[2:], nil
	}
	return nil, path, nil
}

func splitUNCVolume(path string) (components []string, remainder string, err error) {
	server, rest := cutWindowsComponent(path)
	if server == "" || rest == "" {
		return nil, "", errors.New("UNC path requires a server and share")
	}
	share, remainder := cutWindowsComponent(rest)
	if share == "" {
		return nil, "", errors.New("UNC path requires a non-empty share")
	}
	return []string{server, share}, remainder, nil
}

func validateWindowsComponent(component string) error {
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return errors.New("trailing dots and spaces are not allowed")
	}
	for _, character := range component {
		if character < 32 || strings.ContainsRune(`<>:"|?*`, character) {
			return fmt.Errorf("character %q is not allowed", character)
		}
	}
	if isReservedWindowsName(component) {
		return errors.New("reserved DOS device name is not allowed")
	}
	return nil
}

func isReservedWindowsName(component string) bool {
	base := component
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if suffix, found := strings.CutPrefix(base, prefix); found {
			switch suffix {
			case "¹", "²", "³":
				return true
			}
		}
	}
	return false
}

func cutWindowsComponent(path string) (component, remainder string) {
	for len(path) > 0 && isWindowsSeparator(path[0]) {
		path = path[1:]
	}
	for index := 0; index < len(path); index++ {
		if isWindowsSeparator(path[index]) {
			return path[:index], path[index+1:]
		}
	}
	return path, ""
}

func hasWindowsPrefixFold(path, prefix string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for index := range prefix {
		if isWindowsSeparator(prefix[index]) {
			if !isWindowsSeparator(path[index]) {
				return false
			}
			continue
		}
		left, right := path[index], prefix[index]
		if 'a' <= left && left <= 'z' {
			left -= 'a' - 'A'
		}
		if 'a' <= right && right <= 'z' {
			right -= 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func isWindowsSeparator(character byte) bool {
	return character == '\\' || character == '/'
}

func isASCIIAlpha(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

func invalidWindowsPath(path, reason string) error {
	return &os.PathError{
		Op:   "canonicalize",
		Path: path,
		Err:  fmt.Errorf("%w: %s", fs.ErrInvalid, reason),
	}
}

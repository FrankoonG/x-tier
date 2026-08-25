//go:build windows

package configstore

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openLockFile(path string) (*os.File, error) {
	file, err := createWindowsProtectedFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_ATTRIBUTE_NORMAL,
	)
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, windows.ERROR_FILE_EXISTS) && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	file, err = openWindowsSecureFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
	)
	if err != nil {
		return nil, err
	}
	return file, nil
}

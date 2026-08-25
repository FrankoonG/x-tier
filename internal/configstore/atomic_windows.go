//go:build windows

package configstore

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func openAtomicTemp(path string, perm fs.FileMode) (*os.File, error) {
	attributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if perm.Perm()&0o200 == 0 {
		attributes = windows.FILE_ATTRIBUTE_READONLY
	}
	return createWindowsProtectedFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		attributes,
	)
}

func replaceFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(
		sourcePtr,
		targetPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

func syncParentDirectory(string) error {
	return nil
}

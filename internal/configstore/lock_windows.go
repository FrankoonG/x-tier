//go:build windows

package configstore

import (
	"os"

	"golang.org/x/sys/windows"
)

const wholeFileLockLength = ^uint32(0)

func lockFileExclusive(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		wholeFileLockLength,
		wholeFileLockLength,
		&overlapped,
	)
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		wholeFileLockLength,
		wholeFileLockLength,
		&overlapped,
	)
}

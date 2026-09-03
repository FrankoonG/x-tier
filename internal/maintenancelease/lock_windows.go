//go:build windows

package maintenancelease

import (
	"os"

	"golang.org/x/sys/windows"
)

const instanceLockLength = ^uint32(0)

func lockInstanceFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		instanceLockLength,
		instanceLockLength,
		&overlapped,
	)
}

func unlockInstanceFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		instanceLockLength,
		instanceLockLength,
		&overlapped,
	)
}

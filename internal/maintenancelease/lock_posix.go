//go:build !windows

package maintenancelease

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockInstanceFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockInstanceFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

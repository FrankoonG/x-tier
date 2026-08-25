//go:build !windows

package configstore

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func openLockFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	if created {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if err := validatePOSIXSecureFile(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

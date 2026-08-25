//go:build !windows

package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openInstanceLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	created := err == nil
	if err == unix.EEXIST {
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
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("daemon.lock_insecure")
	}
	if created {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

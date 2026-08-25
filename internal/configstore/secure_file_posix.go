//go:build !windows

package configstore

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openSecureConfigFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	if err := validatePOSIXSecureFile(file, path); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validatePOSIXSecureFile(file *os.File, path string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return insecureFileError(path, "missing POSIX file metadata")
	}
	if !info.Mode().IsRegular() {
		return insecureFileError(path, "not a regular file")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return insecureFileError(path, fmt.Sprintf("owner uid %d does not match effective uid %d", stat.Uid, os.Geteuid()))
	}
	if stat.Nlink != 1 {
		return insecureFileError(path, fmt.Sprintf("link count is %d, want 1", stat.Nlink))
	}
	const securityModeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if mode := info.Mode() & securityModeBits; mode != 0o600 {
		return insecureFileError(path, fmt.Sprintf("mode is %04o, want 0600", mode))
	}
	return nil
}

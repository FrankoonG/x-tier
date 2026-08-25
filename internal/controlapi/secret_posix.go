//go:build !windows

package controlapi

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func createSecretFile(path string) (*os.File, error) {
	f, err := openSecretFile(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return nil, err
	}
	if err := validateSecretFile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

func readSecretFile(path string) ([]byte, error) {
	f, err := openSecretFile(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := validateSecretFile(f); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, 4096))
}

func openSecretFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("control.token_open_failed")
	}
	return file, nil
}

func validateSecretFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("control.token_not_regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("control.token_permissions_insecure: %04o", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("control.token_owner_unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("control.token_owner_untrusted: %d", stat.Uid)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("control.token_link_count_invalid: %d", stat.Nlink)
	}
	return nil
}

//go:build linux

package statestore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func normalizedConfigName(_ *os.Root, name string) (string, error) {
	return name, nil
}

func openPinnedRoot(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}

func createSecureRootDirectory(parent *os.Root, name string) error {
	return parent.Mkdir(name, 0o700)
}

func reservedComponent(name string) bool {
	return name == DirectoryName
}

func secureRootDirectory(root *os.Root, diagnosticPath string, created bool) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s has no POSIX metadata", ErrInsecureState, diagnosticPath)
	}
	if !info.IsDir() || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: %s has invalid type or owner", ErrInsecureState, diagnosticPath)
	}
	if created {
		if err := file.Chmod(0o700); err != nil {
			return err
		}
		info, err = file.Stat()
		if err != nil {
			return err
		}
	}
	if mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); mode != 0o700 {
		return fmt.Errorf("%w: %s mode is %04o, want 0700", ErrInsecureState, diagnosticPath, mode)
	}
	return nil
}

func openSecureRootFile(root *os.Root, name string, flag int, perm fs.FileMode) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: file %q is a symlink", ErrInsecureState, name)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := root.OpenFile(name, flag|unix.O_CLOEXEC|unix.O_NOFOLLOW, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: file %q is a symlink", ErrInsecureState, name)
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: file %q has invalid type, owner, or link count", ErrInsecureState, name)
	}
	if mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); mode != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: file %q mode is %04o, want 0600", ErrInsecureState, name, mode)
	}
	return file, nil
}

func publishExclusive(_ *os.Root, directory *os.File, source, target string) error {
	return unix.Renameat2(int(directory.Fd()), source, int(directory.Fd()), target, unix.RENAME_NOREPLACE)
}

func replaceRootFile(_ *os.Root, directory *os.File, source, target string) error {
	return unix.Renameat(int(directory.Fd()), source, int(directory.Fd()), target)
}

func syncDirectory(directory *os.File) error {
	return directory.Sync()
}

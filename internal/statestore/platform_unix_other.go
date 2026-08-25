//go:build !windows && !linux

package statestore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func stableIdentityKey(parent *os.File, leaf string) (string, error) {
	absolute, err := filepath.Abs(filepath.Join(parent.Name(), leaf))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

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
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Geteuid()) {
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
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s mode is %04o, want 0700", ErrInsecureState, diagnosticPath, info.Mode().Perm())
	}
	return nil
}

func secureLegacyIdentityRootDirectory(root *os.Root, diagnosticPath string) error {
	return secureRootDirectory(root, diagnosticPath, false)
}

func openLegacyIdentityRootFile(root *os.Root, name string) (*os.File, error) {
	return openSecureRootFile(root, name, os.O_RDONLY, 0)
}

func openSecureRootFile(root *os.Root, name string, flag int, perm fs.FileMode) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: file %q is a symlink", ErrInsecureState, name)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := root.OpenFile(name, flag, perm)
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
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: file %q has invalid metadata", ErrInsecureState, name)
	}
	return file, nil
}

// Platforms without renameat2 use a hard-link publication fallback. They are
// supported for development builds but are not release-qualified statestore
// targets; Linux and Windows provide a one-name atomic publication primitive.
func publishExclusive(root *os.Root, _ *os.File, source, target string) error {
	if err := root.Link(source, target); err != nil {
		return err
	}
	return root.Remove(source)
}

func replaceRootFile(root *os.Root, _ *os.File, source, target string) error {
	return root.Rename(source, target)
}

func syncDirectory(directory *os.File) error {
	return directory.Sync()
}

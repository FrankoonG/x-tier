//go:build !windows

package identity

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func withSecretPublicationLock(publish func() error) error { return publish() }

func secureSecretDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("seed directory is not a real directory")
	}
	if err := validateOwner(info, "seed directory"); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func secureSecretFile(file *os.File) error {
	return file.Chmod(0o600)
}

func validateSecretPath(path string, _ *os.File, info fs.FileInfo) error {
	if err := validateOwner(info, "seed file"); err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("seed file mode is %04o, want 0600", info.Mode().Perm())
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if directory.Mode().Perm() != 0o700 {
		return fmt.Errorf("seed directory mode is %04o, want 0700", directory.Mode().Perm())
	}
	if !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("seed directory is not a real directory")
	}
	if err := validateOwner(directory, "seed directory"); err != nil {
		return err
	}
	return nil
}

func validateOwner(info fs.FileInfo, name string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s owner is unavailable", name)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s owner uid is %d, want %d", name, stat.Uid, os.Geteuid())
	}
	return nil
}

func validatePublishedSecret(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validateSecretPath(path, file, info)
}

func syncSecretDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func publishSecretFile(source, target string) error {
	return os.Link(source, target)
}

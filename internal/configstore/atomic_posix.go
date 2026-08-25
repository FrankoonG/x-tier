//go:build !windows

package configstore

import (
	"io/fs"
	"os"
)

func openAtomicTemp(path string, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
}

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

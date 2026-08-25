package configstore

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type replaceFunc func(source, target string) error

var ErrCommitOutcomeUnknown = errors.New("config: commit outcome unknown")

const atomicTempCreateAttempts = 10_000

func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return writeFileAtomicWith(path, data, perm, replaceFile)
}

func writeFileAtomicWith(path string, data []byte, perm fs.FileMode, replace replaceFunc) (err error) {
	dir := filepath.Dir(path)
	tmp, err := createAtomicTemp(path, perm)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmp != nil {
			if closeErr := tmp.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if removeErr := os.Remove(tmpPath); err == nil && removeErr != nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = io.Copy(tmp, bytes.NewReader(data)); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	tmp = nil
	if err = validateAtomicTarget(path); err != nil {
		return err
	}
	if err = replace(tmpPath, path); err != nil {
		return err
	}
	if err = syncParentDirectory(dir); err != nil {
		return fmt.Errorf("%w: sync parent directory: %v", ErrCommitOutcomeUnknown, err)
	}
	return nil
}

func validateAtomicTarget(path string) error {
	file, err := openSecureConfigFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return file.Close()
}

func createAtomicTemp(target string, perm fs.FileMode) (*os.File, error) {
	dir := filepath.Dir(target)
	prefix := "." + filepath.Base(target) + ".tmp-"
	for range atomicTempCreateAttempts {
		path := filepath.Join(dir, prefix+rand.Text())
		file, err := openAtomicTemp(path, perm)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	return nil, &os.PathError{
		Op:   "createtemp",
		Path: filepath.Join(dir, prefix+"*"),
		Err:  fs.ErrExist,
	}
}

//go:build !windows

package configstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if err := validateCanonicalTarget(absolute); err != nil {
		return "", err
	}

	parent, err := canonicalExistingParent(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	canonical := filepath.Join(parent, filepath.Base(absolute))
	if err := validateCanonicalTarget(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func validateCanonicalTarget(path string) error {
	file, err := openSecureConfigFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return file.Close()
}

func canonicalExistingParent(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 2)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			info, statErr := os.Stat(resolved)
			if statErr != nil {
				return "", statErr
			}
			if !info.IsDir() {
				return "", &os.PathError{Op: "canonicalize", Path: current, Err: fs.ErrInvalid}
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Abs(resolved)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", &os.PathError{Op: "canonicalize", Path: path, Err: err}
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

//go:build !linux && !windows

package stablelock

import (
	"io"
	"path/filepath"
)

func acquire(string, string) (io.Closer, error) {
	return nopCloser{}, nil
}

func pathKey(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

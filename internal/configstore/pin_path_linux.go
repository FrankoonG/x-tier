//go:build linux

package configstore

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func pinConfigPath(path string) (string, func() error, error) {
	fd, err := unix.Open(filepath.Dir(path), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	directory := os.NewFile(uintptr(fd), filepath.Dir(path))
	if directory == nil {
		_ = unix.Close(fd)
		return "", nil, configErrorf("config.pin_parent_failed")
	}
	pinned := filepath.Join("/proc/self/fd", fmt.Sprintf("%d", fd), filepath.Base(path))
	return pinned, directory.Close, nil
}

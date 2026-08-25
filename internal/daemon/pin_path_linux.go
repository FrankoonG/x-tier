//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func pinInstanceConfigPath(path string) (string, func() error, error) {
	fd, err := unix.Open(filepath.Dir(path), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	directory := os.NewFile(uintptr(fd), filepath.Dir(path))
	if directory == nil {
		_ = unix.Close(fd)
		return "", nil, fmt.Errorf("daemon.pin_parent_failed")
	}
	return filepath.Join("/proc/self/fd", fmt.Sprintf("%d", fd), filepath.Base(path)), directory.Close, nil
}

//go:build linux

package stablelock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func acquire(namespace, key string) (io.Closer, error) {
	digest := sha256.Sum256([]byte(namespace + "\x00" + key))
	name := "\x00xtier-" + namespace + "-" + hex.EncodeToString(digest[:])
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("stable lock %s: %w", namespace, err)
	}
	return listener, nil
}

func pathKey(path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Dir(path), &stat); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(stat.Dev), 16) + ":" +
		strconv.FormatUint(stat.Ino, 16) + ":" + filepath.Base(path), nil
}

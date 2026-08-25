//go:build windows

package stablelock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

type mutexLease struct {
	handle windows.Handle
	once   sync.Once
	err    error
}

func acquire(namespace, key string) (io.Closer, error) {
	digest := sha256.Sum256([]byte(namespace + "\x00" + key))
	name, err := windows.UTF16PtrFromString(`Global\xtier-` + namespace + `-` + hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("stable lock %s: %w", namespace, err)
	}
	if err != nil {
		return nil, fmt.Errorf("stable lock %s: %w", namespace, err)
	}
	return &mutexLease{handle: handle}, nil
}

func (l *mutexLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.handle == 0 {
			return
		}
		l.err = windows.CloseHandle(l.handle)
		l.handle = 0
	})
	return l.err
}

func pathKey(path string) (string, error) {
	parent := filepath.Dir(path)
	name, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fmt.Errorf("stable lock parent is not a directory: %s", parent)
	}
	return strconv.FormatUint(uint64(info.VolumeSerialNumber), 16) + ":" +
		strconv.FormatUint(uint64(info.FileIndexHigh), 16) + ":" +
		strconv.FormatUint(uint64(info.FileIndexLow), 16) + ":" +
		strings.ToLower(filepath.Base(path)), nil
}

package stablelock

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
)

// Acquire reserves a process-lifetime ownership name derived from key. Linux
// uses an abstract Unix socket, which has no directory entry that can be
// renamed or replaced. Windows uses a process-independent named mutex.
func Acquire(namespace, key string) (io.Closer, error) {
	return acquire(namespace, key)
}

// AcquirePath reserves the underlying parent directory object plus basename,
// rather than the path spelling. This survives aliases and directory renames.
func AcquirePath(namespace, path string) (io.Closer, error) {
	return AcquirePathIdentity(namespace, path, path)
}

// AcquirePathIdentity binds both the stable external name and the currently
// opened filesystem object. objectPath may be a /proc/self/fd pinned path.
func AcquirePathIdentity(namespace, namePath, objectPath string) (io.Closer, error) {
	nameLease, err := acquire(namespace+"-name", filepath.Clean(namePath))
	if err != nil {
		return nil, err
	}
	key, err := pathKey(objectPath)
	if err != nil {
		_ = nameLease.Close()
		return nil, err
	}
	return acquirePathIdentityKey(namespace, key, nameLease)
}

// AcquirePathIdentityKey binds the stable external name to an object identity
// obtained from an already-open filesystem handle.
func AcquirePathIdentityKey(namespace, namePath, objectKey string) (io.Closer, error) {
	if objectKey == "" {
		return nil, fmt.Errorf("stable lock object key is empty")
	}
	nameLease, err := acquire(namespace+"-name", filepath.Clean(namePath))
	if err != nil {
		return nil, err
	}
	return acquirePathIdentityKey(namespace, objectKey, nameLease)
}

func acquirePathIdentityKey(namespace, objectKey string, nameLease io.Closer) (io.Closer, error) {
	objectLease, err := acquire(namespace+"-object", objectKey)
	if err != nil {
		_ = nameLease.Close()
		return nil, err
	}
	return &leaseGroup{leases: []io.Closer{nameLease, objectLease}}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

type leaseGroup struct {
	leases []io.Closer
	once   sync.Once
	err    error
}

func (g *leaseGroup) Close() error {
	g.once.Do(func() {
		for index := len(g.leases) - 1; index >= 0; index-- {
			g.err = errors.Join(g.err, g.leases[index].Close())
		}
		g.leases = nil
	})
	return g.err
}

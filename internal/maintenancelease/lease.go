package maintenancelease

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/FrankoonG/x-tier/internal/stablelock"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

// ErrDaemonRunning means the daemon lifetime lease could not be acquired.
var ErrDaemonRunning = errors.New("maintenance: daemon lifetime lease is held")

// Lease excludes xtierd and other local maintenance processes for one config.
type Lease struct {
	file         *os.File
	store        *statestore.Store
	stable       io.Closer
	configStable io.Closer
	once         sync.Once
	err          error
}

// Acquire pins the config identity and takes the exact lifetime lock used by
// xtierd. The caller owns Store until Close returns.
func Acquire(configPath string) (*Lease, error) {
	info, err := os.Lstat(configPath)
	if err != nil {
		return nil, fmt.Errorf("maintenance.config_unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("maintenance.config_not_regular")
	}
	store, err := statestore.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("maintenance.state_open: %w", err)
	}
	keepStore := false
	defer func() {
		if !keepStore {
			_ = store.Close()
		}
	}()

	stableIdentity, err := store.StableIdentityKey()
	if err != nil {
		return nil, fmt.Errorf("maintenance.config_identity: %w", err)
	}
	stable, err := stablelock.AcquirePathIdentityKey("daemon", configPath, stableIdentity)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDaemonRunning, err)
	}
	keepStable := false
	defer func() {
		if !keepStable {
			_ = stable.Close()
		}
	}()

	configStable, err := stablelock.AcquirePathIdentityKey("config", configPath, stableIdentity)
	if err != nil {
		return nil, fmt.Errorf("maintenance.config_owned: %w", err)
	}
	keepConfigStable := false
	defer func() {
		if !keepConfigStable {
			_ = configStable.Close()
		}
	}()

	file, err := store.OpenLock(statestore.DaemonLock)
	if err != nil {
		return nil, fmt.Errorf("maintenance.lock_open: %w", err)
	}
	if err := lockInstanceFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", ErrDaemonRunning, err)
	}
	lease := &Lease{file: file, store: store, stable: stable, configStable: configStable}
	keepStore = true
	keepStable = true
	keepConfigStable = true
	if err := file.Truncate(0); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("maintenance.lock_metadata: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("maintenance.lock_metadata: %w", err)
	}
	if _, err := fmt.Fprintf(file, "maintenance:%d\n", os.Getpid()); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("maintenance.lock_metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("maintenance.lock_metadata: %w", err)
	}
	return lease, nil
}

func (l *Lease) Store() *statestore.Store {
	if l == nil {
		return nil
	}
	return l.store
}

func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = unlockInstanceFile(l.file)
		l.err = errors.Join(l.err, l.file.Close())
		if l.stable != nil {
			l.err = errors.Join(l.err, l.stable.Close())
		}
		if l.configStable != nil {
			l.err = errors.Join(l.err, l.configStable.Close())
		}
		if l.store != nil {
			l.err = errors.Join(l.err, l.store.Close())
		}
	})
	return l.err
}

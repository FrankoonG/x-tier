package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/FrankoonG/x-tier/internal/stablelock"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

type instanceLease struct {
	file         *os.File
	store        *statestore.Store
	stable       io.Closer
	configStable io.Closer
	once         sync.Once
	err          error
}

func acquireInstanceLease(configPath string) (*instanceLease, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("daemon.lock_directory: %w", err)
	}
	store, err := statestore.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.state_open: %w", err)
	}
	keepStore := false
	defer func() {
		if !keepStore {
			_ = store.Close()
		}
	}()
	stableIdentity, err := store.StableIdentityKey()
	if err != nil {
		return nil, fmt.Errorf("daemon.config_identity: %w", err)
	}
	stable, err := stablelock.AcquirePathIdentityKey("daemon", configPath, stableIdentity)
	if err != nil {
		return nil, fmt.Errorf("daemon.already_running: %w", err)
	}
	keepStable := false
	defer func() {
		if !keepStable {
			_ = stable.Close()
		}
	}()
	configStable, err := stablelock.AcquirePathIdentityKey("config", configPath, stableIdentity)
	if err != nil {
		return nil, fmt.Errorf("daemon.config_owned: %w", err)
	}
	keepConfigStable := false
	defer func() {
		if !keepConfigStable {
			_ = configStable.Close()
		}
	}()
	file, err := store.OpenLock(statestore.DaemonLock)
	if err != nil {
		return nil, fmt.Errorf("daemon.lock_open: %w", err)
	}
	if err := lockInstanceFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("daemon.already_running: %w", err)
	}
	lease := &instanceLease{
		file:         file,
		store:        store,
		stable:       stable,
		configStable: configStable,
	}
	keepStable = true
	keepConfigStable = true
	keepStore = true
	if err := file.Truncate(0); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("daemon.lock_metadata: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("daemon.lock_metadata: %w", err)
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("daemon.lock_metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = lease.Close()
		return nil, fmt.Errorf("daemon.lock_metadata: %w", err)
	}
	return lease, nil
}

func (l *instanceLease) ConfigPath() string {
	if l == nil {
		return ""
	}
	return l.store.ConfigPath()
}

func (l *instanceLease) Store() *statestore.Store {
	if l == nil {
		return nil
	}
	return l.store
}

func (l *instanceLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = unlockInstanceFile(l.file)
		if closeErr := l.file.Close(); l.err == nil {
			l.err = closeErr
		}
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

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/FrankoonG/x-tier/internal/stablelock"
)

type instanceLease struct {
	file         *os.File
	stable       io.Closer
	configStable io.Closer
	pinnedPath   string
	closePin     func() error
	once         sync.Once
	err          error
}

func acquireInstanceLease(configPath string) (*instanceLease, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return nil, fmt.Errorf("daemon.lock_directory: %w", err)
	}
	pinnedPath, closePin, err := pinInstanceConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.config_pin: %w", err)
	}
	keepPin := false
	defer func() {
		if !keepPin {
			_ = closePin()
		}
	}()
	stable, err := stablelock.AcquirePathIdentity("daemon", configPath, pinnedPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.already_running: %w", err)
	}
	keepStable := false
	defer func() {
		if !keepStable {
			_ = stable.Close()
		}
	}()
	configStable, err := stablelock.AcquirePathIdentity("config", configPath, pinnedPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.config_owned: %w", err)
	}
	keepConfigStable := false
	defer func() {
		if !keepConfigStable {
			_ = configStable.Close()
		}
	}()
	path := pinnedPath + ".daemon.lock"
	file, err := openInstanceLock(path)
	if err != nil {
		return nil, fmt.Errorf("daemon.lock_open: %w", err)
	}
	if err := lockInstanceFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("daemon.already_running: %w", err)
	}
	lease := &instanceLease{file: file, stable: stable, configStable: configStable, pinnedPath: pinnedPath, closePin: closePin}
	keepStable = true
	keepConfigStable = true
	keepPin = true
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
	return l.pinnedPath
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
		if l.closePin != nil {
			l.err = errors.Join(l.err, l.closePin())
		}
		if l.stable != nil {
			l.err = errors.Join(l.err, l.stable.Close())
		}
		if l.configStable != nil {
			l.err = errors.Join(l.err, l.configStable.Close())
		}
	})
	return l.err
}

package configstore

import (
	"os"
	"time"
)

const (
	configLockRetryInterval = 5 * time.Millisecond
	configLockWaitTimeout   = 5 * time.Second
)

func lockFileExclusiveBounded(file *os.File) error {
	deadline := time.Now().Add(configLockWaitTimeout)
	for {
		err := lockFileExclusive(file)
		if err == nil || !isLockContention(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(configLockRetryInterval)
	}
}

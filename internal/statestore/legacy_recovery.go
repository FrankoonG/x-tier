package statestore

import (
	"fmt"
	"io/fs"
	"strings"
	"time"
)

const legacyBackupTimestampLayout = "20060102T150405.000000000"

// HasLegacyRecoveryCandidates reports old adjacent recovery files whose owner
// cannot be proven. Callers must surface them for explicit operator handling;
// they must never import them into the object-bound store automatically.
func (s *Store) HasLegacyRecoveryCandidates() (bool, error) {
	if s == nil || s.parent == nil {
		return false, fmt.Errorf("statestore.closed")
	}
	entries, err := fs.ReadDir(s.parent.FS(), ".")
	if err != nil {
		return false, err
	}
	lastGood, err := normalizedConfigName(s.parent, s.configLeaf+".last-good")
	if err != nil {
		return false, err
	}
	backupPrefix, err := normalizedConfigName(s.parent, s.configLeaf+".bak.")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		name, err := normalizedConfigName(s.parent, entry.Name())
		if err != nil {
			return false, err
		}
		if name == lastGood || validLegacyBackupCandidate(name, backupPrefix) {
			return true, nil
		}
	}
	return false, nil
}

func validLegacyBackupCandidate(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	timestamp := strings.TrimPrefix(name, prefix)
	parsed, err := time.Parse(legacyBackupTimestampLayout, timestamp)
	return err == nil && parsed.Format(legacyBackupTimestampLayout) == timestamp
}

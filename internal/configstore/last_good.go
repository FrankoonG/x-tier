package configstore

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

const lastKnownGoodSuffix = ".last-good"

// LastKnownGoodPath returns the private runtime checkpoint associated with a
// configuration. The checkpoint is never used as the configured source of
// truth; it exists only to keep the daemon control plane recoverable.
func LastKnownGoodPath(configPath string) string {
	return configPath + lastKnownGoodSuffix
}

// RestorePinnedLastKnownGood repairs an invalid active document while the
// daemon owns the pinned config path. It never bypasses validation of the
// checkpoint and refuses to overwrite a currently valid active config.
func RestorePinnedLastKnownGood(configPath, canonicalKey string, expectedRevision int64, dryRun bool) (UpdateResult, error) {
	if canonicalKey == "" {
		return UpdateResult{}, configErrorf("config.ownership_key_required")
	}
	if expectedRevision < 0 && !dryRun {
		return UpdateResult{}, configErrorf("config.revision_required")
	}
	var result UpdateResult
	err := withLockedPathKey(configPath, canonicalKey, false, false, func(lockedPath string) error {
		if _, activeErr := LoadExisting(lockedPath); activeErr == nil {
			return configErrorf("config.restore_not_required")
		} else if !IsContentError(activeErr) {
			return configErrorf("config.restore_active_unavailable: %w", activeErr)
		}

		checkpoint, err := LoadLastKnownGood(lockedPath)
		if err != nil {
			return configErrorf("config.restore_checkpoint_unavailable: %w", err)
		}
		if err := ValidateRevision(checkpoint, expectedRevision); err != nil {
			return err
		}
		if checkpoint.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		result.BeforeRevision = checkpoint.Revision
		result.AfterRevision = checkpoint.Revision + 1
		if dryRun {
			return nil
		}
		checkpoint.Revision = result.AfterRevision
		if err := Save(lockedPath, checkpoint); err != nil {
			return configErrorf("config.restore_write: %w", err)
		}
		return nil
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func SaveLastKnownGood(configPath string, cfg Config) error {
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return configErrorf("config.last_good_validate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return configErrorf("config.last_good_directory: %w", err)
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return configErrorf("config.last_good_encode: %w", err)
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(LastKnownGoodPath(configPath), payload, 0o600); err != nil {
		return configErrorf("config.last_good_write: %w", err)
	}
	return nil
}

func LoadLastKnownGood(configPath string) (Config, error) {
	payload, err := readSecureFile(LastKnownGoodPath(configPath))
	if err != nil {
		return Config{}, configErrorf("config.last_good_read: %w", err)
	}
	schemaVersion, versioned, err := configSchemaFromJSON(payload)
	if err != nil {
		return Config{}, configErrorf("config.last_good_decode: %w", err)
	}
	if versioned && schemaVersion > CurrentSchemaVersion {
		return Config{}, fmt.Errorf(
			"config.last_good_decode: config.schema_newer: have %d support %d",
			schemaVersion,
			CurrentSchemaVersion,
		)
	}
	var cfg Config
	if versioned && schemaVersion == CurrentSchemaVersion {
		cfg, err = decodeConfig(payload)
	} else {
		cfg, err = decodeMigratableConfig(payload, schemaVersion, versioned)
	}
	if err != nil {
		return Config{}, configErrorf("config.last_good_decode: %w", err)
	}
	return cfg, nil
}

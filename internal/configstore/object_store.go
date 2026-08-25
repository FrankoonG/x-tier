package configstore

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"reflect"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func LoadStore(store *statestore.Store) (Config, error) {
	cfg, err := LoadStoreExisting(store)
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultConfig(), nil
	}
	return cfg, err
}

func LoadStoreExisting(store *statestore.Store) (Config, error) {
	if store == nil {
		return Config{}, configErrorf("config.store_required")
	}
	payload, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(payload)
}

func SaveStore(store *statestore.Store, cfg Config) error {
	if store == nil {
		return configErrorf("config.store_required")
	}
	return withStoreConfigLock(store, func() error {
		return saveStoreUnlocked(store, cfg)
	})
}

func saveStoreUnlocked(store *statestore.Store, cfg Config) error {
	payload, err := EncodeDocument(cfg)
	if err != nil {
		return err
	}
	previous, readErr := store.ReadConfig(maxConfigFileBytes)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return configErrorf("config.backup_read: %w", readErr)
	}
	if existed {
		if _, err := store.CreateBackup(previous); err != nil {
			return configErrorf("config.backup_write: %w", err)
		}
		err = store.ReplaceConfig(payload)
	} else {
		err = store.CreateConfigExclusive(payload)
	}
	if err != nil {
		if errors.Is(err, statestore.ErrCommitOutcomeUnknown) {
			return fmt.Errorf("%w: config.store_write: %v", ErrCommitOutcomeUnknown, err)
		}
		return configErrorf("config.write: %w", err)
	}
	if existed {
		if err := store.PruneBackups(maxConfigBackups); err != nil {
			return &commitVisibleError{
				revision: cfg.Revision,
				cause:    configErrorf("config.backup_prune: %w", err),
			}
		}
	}
	return nil
}

func LoadStoreOrMigrate(store *statestore.Store, persistMissing bool) (Config, bool, error) {
	if store == nil {
		return Config{}, false, configErrorf("config.store_required")
	}
	var cfg Config
	migrated := false
	err := withStoreConfigLock(store, func() error {
		payload, readErr := store.ReadConfig(maxConfigFileBytes)
		if errors.Is(readErr, fs.ErrNotExist) {
			cfg = DefaultConfig()
			if persistMissing {
				if err := saveStoreUnlocked(store, cfg); err != nil {
					return configErrorf("config.initial_write: %w", err)
				}
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
		schemaVersion, versioned, err := configSchemaFromJSON(payload)
		if err != nil {
			return err
		}
		if versioned && schemaVersion > CurrentSchemaVersion {
			return configErrorf("config.schema_newer: have %d support %d", schemaVersion, CurrentSchemaVersion)
		}
		if versioned && schemaVersion == CurrentSchemaVersion {
			cfg, err = decodeConfig(payload)
			return err
		}
		cfg, err = decodeMigratableConfig(payload, schemaVersion, versioned)
		if err != nil {
			return configErrorf("config.legacy_decode: %w", err)
		}
		if cfg.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		cfg.SchemaVersion = CurrentSchemaVersion
		cfg.Revision++
		if err := saveStoreUnlocked(store, cfg); err != nil {
			return configErrorf("config.legacy_migration_write: %w", err)
		}
		migrated = true
		return nil
	})
	if err != nil {
		return Config{}, false, err
	}
	return cfg, migrated, nil
}

func UpdateStoreCAS(store *statestore.Store, expectedRevision int64, mutate func(*Config) error) (UpdateResult, error) {
	if store == nil {
		return UpdateResult{}, configErrorf("config.store_required")
	}
	if expectedRevision < 0 {
		return UpdateResult{}, configErrorf("config.revision_required")
	}
	if mutate == nil {
		return UpdateResult{}, configErrorf("config.mutation_required")
	}
	var result UpdateResult
	err := withStoreConfigLock(store, func() error {
		cfg, err := LoadStoreExisting(store)
		if err != nil {
			return err
		}
		result.BeforeRevision = cfg.Revision
		if err := ValidateRevision(cfg, expectedRevision); err != nil {
			return err
		}
		if cfg.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		if err := mutate(&cfg); err != nil {
			return err
		}
		cfg.Revision = result.BeforeRevision + 1
		if err := Validate(cfg); err != nil {
			return err
		}
		err = saveStoreUnlocked(store, cfg)
		if err == nil {
			result.AfterRevision = cfg.Revision
			return nil
		}
		if CommitVisible(err) {
			result.AfterRevision = cfg.Revision
			return err
		}
		if !errors.Is(err, ErrCommitOutcomeUnknown) {
			return err
		}
		observed, loadErr := LoadStoreExisting(store)
		expected := cfg
		normalize(&expected)
		if loadErr == nil && reflect.DeepEqual(observed, expected) {
			if syncErr := store.SyncConfigParent(); syncErr != nil {
				return errors.Join(err, configErrorf("config.commit_confirmation_sync: %w", syncErr))
			}
			result.AfterRevision = cfg.Revision
			return &commitVisibleError{revision: cfg.Revision, cause: err}
		}
		if loadErr == nil {
			loadErr = configErrorf("config.commit_confirmation_mismatch")
		}
		return errors.Join(err, loadErr)
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func SaveStoreLastKnownGood(store *statestore.Store, cfg Config) error {
	if store == nil {
		return configErrorf("config.store_required")
	}
	payload, err := EncodeDocument(cfg)
	if err != nil {
		return configErrorf("config.last_good_validate: %w", err)
	}
	if err := store.Replace(statestore.LastKnownGood, payload); err != nil {
		return configErrorf("config.last_good_write: %w", err)
	}
	return nil
}

func LoadStoreLastKnownGood(store *statestore.Store) (Config, error) {
	if store == nil {
		return Config{}, configErrorf("config.store_required")
	}
	payload, err := store.Read(statestore.LastKnownGood, maxConfigFileBytes)
	if err != nil {
		return Config{}, configErrorf("config.last_good_read: %w", err)
	}
	return decodeLastKnownGood(payload)
}

func RestoreStoreLastKnownGood(store *statestore.Store, expectedRevision int64, dryRun bool) (UpdateResult, error) {
	if store == nil {
		return UpdateResult{}, configErrorf("config.store_required")
	}
	if expectedRevision < 0 && !dryRun {
		return UpdateResult{}, configErrorf("config.revision_required")
	}
	var result UpdateResult
	err := withStoreConfigLock(store, func() error {
		if _, activeErr := LoadStoreExisting(store); activeErr == nil {
			return configErrorf("config.restore_not_required")
		} else if !IsContentError(activeErr) {
			return configErrorf("config.restore_active_unavailable: %w", activeErr)
		}
		checkpoint, err := LoadStoreLastKnownGood(store)
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
		if err := saveStoreUnlocked(store, checkpoint); err != nil {
			return configErrorf("config.restore_write: %w", err)
		}
		return nil
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func withStoreConfigLock(store *statestore.Store, fn func() error) (err error) {
	current, err := store.OpenLock(statestore.ConfigLock)
	if err != nil {
		return configErrorf("config.lock_open: %w", err)
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	if err := lockFileExclusive(current); err != nil {
		return configErrorf("config.locked: %w", err)
	}
	defer func() { err = errors.Join(err, unlockFile(current)) }()
	return fn()
}

// WithStoreLock serializes a state-layout transition with v2 config writers.
// Daemon lifetime ownership across released and current binaries is enforced
// separately by stablelock. The callback must not acquire this lock again.
func WithStoreLock(store *statestore.Store, fn func() error) error {
	if store == nil || fn == nil {
		return configErrorf("config.store_lock_invalid")
	}
	return withStoreConfigLock(store, fn)
}

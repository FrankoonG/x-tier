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
	cfg, err := decodeConfig(payload)
	if err != nil {
		return Config{}, err
	}
	ledger, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil {
		return Config{}, err
	}
	if err := validateConfigMatchesCredentialLedger(cfg, ledger, exists); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	payload, err := prepareStoreConfigPublication(store, cfg)
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

func saveMigratedStoreUnlocked(store *statestore.Store, cfg Config, source []byte) error {
	payload, err := prepareStoreConfigPublication(store, cfg)
	if err != nil {
		return err
	}
	if err := store.Replace(statestore.PreMigrationConfig, source); err != nil {
		return configErrorf("config.pre_migration_write: %w", err)
	}
	if err := store.ReplaceConfig(payload); err != nil {
		if errors.Is(err, statestore.ErrCommitOutcomeUnknown) {
			return fmt.Errorf("%w: config.store_write: %v", ErrCommitOutcomeUnknown, err)
		}
		return configErrorf("config.write: %w", err)
	}
	return nil
}

// saveRestoredStoreUnlocked retains the most recently replaced active payload
// in one secure, non-authoritative diagnostic object.
func saveRestoredStoreUnlocked(store *statestore.Store, cfg Config, rejected []byte) error {
	payload, err := prepareStoreConfigPublication(store, cfg)
	if err != nil {
		return err
	}
	if err := store.Replace(statestore.RejectedConfig, rejected); err != nil {
		return configErrorf("config.rejected_write: %w", err)
	}
	if err := store.ReplaceConfig(payload); err != nil {
		if errors.Is(err, statestore.ErrCommitOutcomeUnknown) {
			return fmt.Errorf("%w: config.store_write: %v", ErrCommitOutcomeUnknown, err)
		}
		return configErrorf("config.write: %w", err)
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
			cfg, err = decodeRepairableConfig(payload)
			if err != nil {
				return err
			}
			ledger, ledgerExists, ledgerErr := loadStorePeerCredentialLedger(store)
			if ledgerErr != nil {
				return ledgerErr
			}
			if !ledgerExists {
				return peerCredentialLedgerMissingError()
			}
			changed, quarantineErr := mergeCredentialLedgerIntoConfig(&cfg, ledger)
			if quarantineErr != nil {
				return markContentError(quarantineErr)
			}
			ledgerCurrent := ledgerExists && reflect.DeepEqual(ledger, cfg.PeerCredentialQuarantines)
			if !changed {
				if !ledgerCurrent {
					if _, err := persistStorePeerCredentialLedger(store, cfg.PeerCredentialQuarantines); err != nil {
						return err
					}
				}
				return nil
			}
			if cfg.Revision == math.MaxInt64 {
				return configErrorf("config.revision_exhausted")
			}
			cfg.Revision++
			saveErr := saveStoreUnlocked(store, cfg)
			if saveErr = confirmMigrationWrite(cfg, saveErr, func() (Config, error) {
				return LoadStoreExisting(store)
			}, store.SyncConfigParent); saveErr != nil {
				return configErrorf("config.peer_credential_quarantine_write: %w", saveErr)
			}
			migrated = true
			return nil
		}
		cfg, err = decodeMigratableConfig(payload, schemaVersion, versioned)
		if err != nil {
			return configErrorf("config.legacy_decode: %w", err)
		}
		if cfg.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		cfg.SchemaVersion = CurrentSchemaVersion
		ledger, ledgerExists, ledgerErr := loadStorePeerCredentialLedger(store)
		if ledgerErr != nil {
			return ledgerErr
		}
		if !ledgerExists {
			return peerCredentialLedgerMissingError()
		}
		if _, quarantineErr := mergeCredentialLedgerIntoConfig(&cfg, ledger); quarantineErr != nil {
			return markContentError(quarantineErr)
		}
		cfg.Revision++
		saveErr := saveMigratedStoreUnlocked(store, cfg, payload)
		if saveErr = confirmMigrationWrite(cfg, saveErr, func() (Config, error) {
			return LoadStoreExisting(store)
		}, store.SyncConfigParent); saveErr != nil {
			return configErrorf("config.legacy_migration_write: %w", saveErr)
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
	return withStoreConfigLock(store, func() error {
		return saveStoreLastKnownGoodUnlocked(store, cfg)
	})
}

func saveStoreLastKnownGoodUnlocked(store *statestore.Store, cfg Config) error {
	normalize(&cfg)
	payload, err := EncodeDocument(cfg)
	if err != nil {
		return configErrorf("config.last_good_validate: %w", err)
	}
	ledger, ledgerExists, err := loadStorePeerCredentialLedger(store)
	if err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	if err := requireConfigCoversCredentialLedger(cfg, ledger, ledgerExists); err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	persistedLedger, err := persistStorePeerCredentialLedger(store, cfg.PeerCredentialQuarantines)
	if err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	if err := validateConfigMatchesCredentialLedger(cfg, persistedLedger, true); err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
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
		activePayload, activeReadErr := store.ReadConfig(maxConfigFileBytes)
		active := Config{}
		activeErr := activeReadErr
		if activeReadErr == nil {
			if err := guardRestoreSchema(activePayload); err != nil {
				return err
			}
			active, activeErr = decodeRepairableConfig(activePayload)
		}
		activeRevision, activeRevisionKnown := recoverableDocumentRevision(activePayload, activeReadErr)
		if activeErr == nil {
			activeRevision = active.Revision
			activeRevisionKnown = true
		}
		if activeErr == nil {
			activeErr = Validate(active)
			if activeErr != nil {
				activeErr = markContentError(activeErr)
			}
		}
		if activeErr != nil && !IsContentError(activeErr) {
			return configErrorf("config.restore_active_unavailable: %w", activeErr)
		}
		ledger, ledgerExists, err := loadStorePeerCredentialLedger(store)
		if err != nil {
			return configErrorf("config.restore_credential_ledger_unavailable: %w", err)
		}
		if activeErr == nil {
			activeErr = validateConfigMatchesCredentialLedger(active, ledger, ledgerExists)
			if activeErr == nil {
				return configErrorf("config.restore_not_required")
			}
		}
		checkpoint, err := LoadStoreLastKnownGood(store)
		if err != nil {
			return configErrorf("config.restore_checkpoint_unavailable: %w", err)
		}
		checkpoint, _, err = prepareLastKnownGoodRestore(
			checkpoint,
			activePayload,
			activeReadErr,
			ledger,
			ledgerExists,
		)
		if err != nil {
			return err
		}
		backupRevisionHighWater, err := storeBackupRevisionHighWater(store)
		if err != nil {
			return err
		}
		reservedRevisionHighWater, reservationExists, err := loadStoreConfigRevisionHighWater(store)
		if err != nil {
			return configErrorf("config.restore_revision_high_water_unavailable: %w", err)
		}
		if !reservationExists {
			reservedRevisionHighWater = -1
		}
		beforeRevision, afterRevision, err := lastKnownGoodRestoreRevisions(
			activeRevision,
			activeRevisionKnown,
			backupRevisionHighWater,
			reservedRevisionHighWater,
			checkpoint,
			expectedRevision,
		)
		if err != nil {
			return err
		}
		result.BeforeRevision = beforeRevision
		result.AfterRevision = afterRevision
		if dryRun {
			return nil
		}
		if err := persistStoreConfigRevisionHighWater(store, result.AfterRevision); err != nil {
			return configErrorf("config.restore_revision_reserve: %w", err)
		}
		checkpoint.Revision = result.AfterRevision
		if err := saveRestoredStoreUnlocked(store, checkpoint, activePayload); err != nil {
			return configErrorf("config.restore_write: %w", err)
		}
		return nil
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

// LoadStoreLastKnownGoodForRecovery constructs and durably anchors a
// fail-closed checkpoint candidate without replacing the active config.
func LoadStoreLastKnownGoodForRecovery(store *statestore.Store) (Config, error) {
	if store == nil {
		return Config{}, configErrorf("config.store_required")
	}
	var recovered Config
	err := withStoreConfigLock(store, func() error {
		activePayload, activeReadErr := store.ReadConfig(maxConfigFileBytes)
		if activeReadErr != nil {
			return configErrorf("config.recovery_active_unavailable: %w", activeReadErr)
		}
		checkpoint, err := LoadStoreLastKnownGood(store)
		if err != nil {
			return err
		}
		ledger, ledgerExists, err := loadStorePeerCredentialLedger(store)
		if err != nil {
			return err
		}
		var authority []PeerCredentialQuarantine
		recovered, authority, err = prepareLastKnownGoodRestore(
			checkpoint,
			activePayload,
			nil,
			ledger,
			ledgerExists,
		)
		if err != nil {
			return err
		}
		persisted, err := persistStorePeerCredentialLedger(store, authority)
		if err != nil {
			return err
		}
		return validateConfigMatchesCredentialLedger(recovered, persisted, true)
	})
	return recovered, err
}

func storeBackupRevisionHighWater(store *statestore.Store) (int64, error) {
	names, err := store.BackupNames()
	if err != nil {
		return 0, configErrorf("config.restore_backup_list: %w", err)
	}
	highWater := int64(-1)
	for _, name := range names {
		payload, readErr := store.ReadBackup(name, maxConfigFileBytes)
		if readErr != nil {
			return 0, configErrorf("config.restore_backup_read: %s: %w", name, readErr)
		}
		revision, known := recoverableBackupRevision(payload)
		if !known {
			return 0, configErrorf("config.restore_backup_revision_unavailable: %s", name)
		}
		if revision > highWater {
			highWater = revision
		}
	}
	return highWater, nil
}

func withStoreConfigLock(store *statestore.Store, fn func() error) (err error) {
	current, err := store.OpenLock(statestore.ConfigLock)
	if err != nil {
		return configErrorf("config.lock_open: %w", err)
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	if err := lockFileExclusiveBounded(current); err != nil {
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

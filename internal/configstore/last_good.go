package configstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
		activePayload, activeReadErr := readSecureFile(lockedPath)
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
		ledger, ledgerExists, err := loadPathPeerCredentialLedger(lockedPath)
		if err != nil {
			return configErrorf("config.restore_credential_ledger_unavailable: %w", err)
		}
		if activeErr == nil {
			activeErr = validateConfigMatchesCredentialLedger(active, ledger, ledgerExists)
			if activeErr == nil {
				return configErrorf("config.restore_not_required")
			}
		}

		checkpoint, err := LoadLastKnownGood(lockedPath)
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
		backupRevisionHighWater, err := pathBackupRevisionHighWater(lockedPath)
		if err != nil {
			return err
		}
		reservedRevisionHighWater, reservationExists, err := loadPathConfigRevisionHighWater(lockedPath)
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
		if err := persistPathConfigRevisionHighWater(lockedPath, result.AfterRevision); err != nil {
			return configErrorf("config.restore_revision_reserve: %w", err)
		}
		checkpoint.Revision = result.AfterRevision
		if err := saveRestoredPath(lockedPath, checkpoint, activePayload); err != nil {
			return configErrorf("config.restore_write: %w", err)
		}
		return nil
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func guardRestoreSchema(payload []byte) error {
	schemaVersion, versioned, err := configSchemaFromJSON(payload)
	if err != nil {
		return nil
	}
	if versioned && schemaVersion > CurrentSchemaVersion {
		return configErrorf(
			"config.restore_schema_newer: have %d support %d",
			schemaVersion,
			CurrentSchemaVersion,
		)
	}
	return nil
}

func lastKnownGoodRestoreRevisions(
	activeRevision int64,
	activeRevisionKnown bool,
	backupRevisionHighWater int64,
	reservedRevisionHighWater int64,
	checkpoint Config,
	expectedRevision int64,
) (int64, int64, error) {
	base := checkpoint.Revision
	before := checkpoint.Revision
	if activeRevisionKnown {
		if err := ValidateRevision(Config{Revision: activeRevision}, expectedRevision); err != nil {
			return 0, 0, err
		}
		before = activeRevision
		if activeRevision > base {
			base = activeRevision
		}
	} else {
		if err := ValidateRevision(checkpoint, expectedRevision); err != nil {
			return 0, 0, err
		}
	}
	if backupRevisionHighWater >= 0 {
		if backupRevisionHighWater == math.MaxInt64 {
			return 0, 0, configErrorf("config.revision_exhausted")
		}
		inferredSuccessor := backupRevisionHighWater + 1
		if inferredSuccessor > base {
			base = inferredSuccessor
		}
	}
	if reservedRevisionHighWater > base {
		base = reservedRevisionHighWater
	}
	if base >= math.MaxInt64 {
		return 0, 0, configErrorf("config.revision_exhausted")
	}
	return before, base + 1, nil
}

func recoverableDocumentRevision(payload []byte, readErr error) (int64, bool) {
	if readErr != nil || preflightConfigJSON(payload) != nil {
		return 0, false
	}
	var envelope struct {
		Revision json.RawMessage `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil {
		return 0, false
	}
	trimmed := bytes.TrimSpace(envelope.Revision)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false
	}
	var revision int64
	if err := json.Unmarshal(trimmed, &revision); err != nil || revision < 0 {
		return 0, false
	}
	return revision, true
}

func recoverableBackupRevision(payload []byte) (int64, bool) {
	if revision, known := recoverableDocumentRevision(payload, nil); known {
		return revision, true
	}
	schemaVersion, versioned, err := configSchemaFromJSON(payload)
	if err != nil || versioned {
		return 0, false
	}
	var envelope struct {
		Revision json.RawMessage `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil || len(envelope.Revision) != 0 {
		return 0, false
	}
	legacy, err := decodeMigratableConfig(payload, schemaVersion, false)
	if err != nil {
		return 0, false
	}
	return legacy.Revision, true
}

func pathBackupRevisionHighWater(configPath string) (int64, error) {
	matches, err := filepath.Glob(configPath + ".bak.*")
	if err != nil {
		return 0, configErrorf("config.restore_backup_list: %w", err)
	}
	highWater := int64(-1)
	for _, backupPath := range matches {
		name := filepath.Base(backupPath)
		timestamp := strings.TrimPrefix(name, filepath.Base(configPath)+".bak.")
		if _, parseErr := time.Parse("20060102T150405.000000000", timestamp); parseErr != nil {
			continue
		}
		payload, readErr := readSecureFile(backupPath)
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

func prepareLastKnownGoodRestore(
	checkpoint Config,
	activePayload []byte,
	activeReadErr error,
	ledger []PeerCredentialQuarantine,
	ledgerExists bool,
) (Config, []PeerCredentialQuarantine, error) {
	if !ledgerExists {
		return Config{}, nil, configErrorf(
			"config.restore_credential_ledger_missing: %w",
			ErrPeerCredentialLedgerMissing,
		)
	}
	if activeNodeID, known := recoverableConfigNodeID(activePayload, activeReadErr); known &&
		activeNodeID != checkpoint.Node.NodeID {
		return Config{}, nil, configErrorf(
			"config.restore_node_id_mismatch: active %q checkpoint %q",
			activeNodeID,
			checkpoint.Node.NodeID,
		)
	}
	activeQuarantines, known, err := recoverablePeerCredentialQuarantines(activePayload, activeReadErr)
	if err != nil {
		return Config{}, nil, configErrorf("config.restore_active_quarantine_invalid: %w", err)
	}
	authoritative, err := mergePeerCredentialQuarantines(
		ledger,
		checkpoint.PeerCredentialQuarantines,
	)
	if err != nil {
		return Config{}, nil, configErrorf("config.restore_quarantine_merge: %w", err)
	}
	if known {
		authoritative, err = mergePeerCredentialQuarantines(
			authoritative,
			activeQuarantines,
		)
		if err != nil {
			return Config{}, nil, configErrorf("config.restore_quarantine_merge: %w", err)
		}
	}
	if _, err := mergeCredentialLedgerIntoConfig(&checkpoint, authoritative); err != nil {
		return Config{}, nil, configErrorf("config.restore_quarantine_apply: %w", err)
	}
	return checkpoint, authoritative, nil
}

func recoverableConfigNodeID(payload []byte, readErr error) (string, bool) {
	if readErr != nil {
		return "", false
	}
	schemaVersion, versioned, err := configSchemaFromJSON(payload)
	if err != nil || versioned && schemaVersion > CurrentSchemaVersion {
		return "", false
	}
	var cfg Config
	if versioned && schemaVersion == CurrentSchemaVersion {
		cfg, err = decodeRepairableConfig(payload)
	} else {
		cfg, err = decodeMigratableConfig(payload, schemaVersion, versioned)
	}
	if err != nil {
		return "", false
	}
	return cfg.Node.NodeID, true
}

func recoverablePeerCredentialQuarantines(
	payload []byte,
	readErr error,
) ([]PeerCredentialQuarantine, bool, error) {
	if readErr != nil || preflightConfigJSON(payload) != nil {
		return nil, false, nil
	}
	var envelope struct {
		Quarantines json.RawMessage `json:"peer_credential_quarantines"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil || ensureJSONEOF(decoder) != nil {
		return nil, false, nil
	}
	trimmed := bytes.TrimSpace(envelope.Quarantines)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, true, nil
	}
	var quarantines []PeerCredentialQuarantine
	fieldDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	fieldDecoder.DisallowUnknownFields()
	if err := fieldDecoder.Decode(&quarantines); err != nil {
		return nil, true, err
	}
	if err := ensureJSONEOF(fieldDecoder); err != nil {
		return nil, true, err
	}
	if _, err := validatePeerCredentialQuarantines(quarantines); err != nil {
		return nil, true, err
	}
	return quarantines, true, nil
}

func mergePeerCredentialQuarantines(
	base []PeerCredentialQuarantine,
	additional []PeerCredentialQuarantine,
) ([]PeerCredentialQuarantine, error) {
	if _, err := validatePeerCredentialQuarantines(base); err != nil {
		return nil, err
	}
	if _, err := validatePeerCredentialQuarantines(additional); err != nil {
		return nil, err
	}
	type mergedRecord struct {
		reason  string
		peerIDs map[string]struct{}
	}
	records := make(map[string]*mergedRecord, len(base)+len(additional))
	for _, quarantine := range append(append([]PeerCredentialQuarantine(nil), base...), additional...) {
		record := records[quarantine.CredentialFingerprint]
		if record == nil {
			record = &mergedRecord{reason: quarantine.Reason, peerIDs: map[string]struct{}{}}
			records[quarantine.CredentialFingerprint] = record
		}
		if record.reason != quarantine.Reason {
			return nil, configErrorf("config.peer_credential_quarantine_reason_conflict")
		}
		for _, nodeID := range quarantine.PeerNodeIDs {
			record.peerIDs[nodeID] = struct{}{}
		}
	}
	if len(records) == 0 {
		return nil, nil
	}
	result := make([]PeerCredentialQuarantine, 0, len(records))
	for fingerprint, record := range records {
		peerIDs := make([]string, 0, len(record.peerIDs))
		for nodeID := range record.peerIDs {
			peerIDs = append(peerIDs, nodeID)
		}
		sort.Strings(peerIDs)
		result = append(result, PeerCredentialQuarantine{
			CredentialFingerprint: fingerprint,
			PeerNodeIDs:           peerIDs,
			Reason:                record.reason,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CredentialFingerprint < result[j].CredentialFingerprint
	})
	return result, nil
}

func SaveLastKnownGood(configPath string, cfg Config) error {
	canonical, err := CanonicalPath(configPath)
	if err != nil {
		return err
	}
	return withLockedPathKey(canonical, canonical, false, true, func(lockedPath string) error {
		return saveLastKnownGoodUnlocked(lockedPath, cfg)
	})
}

func saveLastKnownGoodUnlocked(configPath string, cfg Config) error {
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return configErrorf("config.last_good_validate: %w", err)
	}
	ledger, ledgerExists, err := loadPathPeerCredentialLedger(configPath)
	if err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	if err := requireConfigCoversCredentialLedger(cfg, ledger, ledgerExists); err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	persistedLedger, err := persistPathPeerCredentialLedger(configPath, cfg.PeerCredentialQuarantines)
	if err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	if err := validateConfigMatchesCredentialLedger(cfg, persistedLedger, true); err != nil {
		return configErrorf("config.last_good_credential_ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(LastKnownGoodPath(configPath)), 0o700); err != nil {
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
	return decodeLastKnownGood(payload)
}

// LoadPinnedLastKnownGoodForRecovery constructs the same fail-closed recovery
// candidate used by an explicit restore without publishing it as active.
func LoadPinnedLastKnownGoodForRecovery(configPath, canonicalKey string) (Config, error) {
	if canonicalKey == "" {
		return Config{}, configErrorf("config.ownership_key_required")
	}
	var recovered Config
	err := withLockedPathKey(configPath, canonicalKey, false, false, func(lockedPath string) error {
		activePayload, activeReadErr := readSecureFile(lockedPath)
		if activeReadErr != nil {
			return configErrorf("config.recovery_active_unavailable: %w", activeReadErr)
		}
		checkpoint, err := LoadLastKnownGood(lockedPath)
		if err != nil {
			return err
		}
		ledger, ledgerExists, err := loadPathPeerCredentialLedger(lockedPath)
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
		persisted, err := persistPathPeerCredentialLedger(lockedPath, authority)
		if err != nil {
			return err
		}
		return validateConfigMatchesCredentialLedger(recovered, persisted, true)
	})
	return recovered, err
}

func decodeLastKnownGood(payload []byte) (Config, error) {
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
		cfg, err = decodeRepairableConfig(payload)
	} else {
		cfg, err = decodeMigratableConfig(payload, schemaVersion, versioned)
	}
	if err != nil {
		return Config{}, configErrorf("config.last_good_decode: %w", err)
	}
	if _, quarantineErr := quarantinePeerCredentialCollisions(&cfg); quarantineErr != nil {
		return Config{}, configErrorf("config.last_good_decode: %w", markContentError(quarantineErr))
	}
	if err := Validate(cfg); err != nil {
		return Config{}, configErrorf("config.last_good_decode: %w", markContentError(err))
	}
	return cfg, nil
}

// DecodeCheckpointDocument applies the same strict and schema-aware rules as
// loading a persisted last-known-good object without performing path I/O.
func DecodeCheckpointDocument(payload []byte) (Config, error) {
	return decodeLastKnownGood(payload)
}

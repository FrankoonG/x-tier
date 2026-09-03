package configstore

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"sort"
	"time"

	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

const missingCredentialLedgerRecoveryVersion = 1

const (
	CredentialRecoveryModeProtectedSourceUnion = "protected_source_union"
	CredentialRecoveryModeEvidenceUnverified   = "credential_evidence_unverified"
)

// MissingCredentialLedgerRecoverySource describes one protected local source
// without exposing raw credentials or source contents.
type MissingCredentialLedgerRecoverySource struct {
	Kind                   string `json:"kind"`
	Name                   string `json:"name"`
	Present                bool   `json:"present"`
	Protected              bool   `json:"protected"`
	Size                   int    `json:"size,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
	SchemaVersion          *int   `json:"schema_version,omitempty"`
	Revision               *int64 `json:"revision,omitempty"`
	NodeID                 string `json:"node_id,omitempty"`
	QuarantinesRecoverable bool   `json:"quarantines_recoverable"`
	QuarantineCount        int    `json:"quarantine_count,omitempty"`
	QuarantineSetSHA256    string `json:"quarantine_set_sha256,omitempty"`
	Issue                  string `json:"issue,omitempty"`
}

// MissingCredentialLedgerRecoveryConfirmation is the exact, source-bound
// operator confirmation required by an applying recovery invocation.
type MissingCredentialLedgerRecoveryConfirmation struct {
	Version                      int                        `json:"version"`
	Mode                         string                     `json:"mode"`
	NodeID                       string                     `json:"node_id"`
	CheckpointRevision           int64                      `json:"checkpoint_revision"`
	BeforeRevision               int64                      `json:"before_revision"`
	AfterRevision                int64                      `json:"after_revision"`
	ActiveSHA256                 string                     `json:"active_sha256"`
	SourceSetSHA256              string                     `json:"source_set_sha256"`
	QuarantineSetSHA256          string                     `json:"quarantine_set_sha256"`
	CredentialEvidenceUnverified bool                       `json:"credential_evidence_unverified"`
	Quarantines                  []PeerCredentialQuarantine `json:"quarantines"`
}

type MissingCredentialLedgerRecoveryPlan struct {
	Mode                         string                                      `json:"mode"`
	CredentialEvidenceUnverified bool                                        `json:"credential_evidence_unverified"`
	Sources                      []MissingCredentialLedgerRecoverySource     `json:"sources"`
	Confirmation                 MissingCredentialLedgerRecoveryConfirmation `json:"confirmation"`
}

type MissingCredentialLedgerRecoveryOptions struct {
	DryRun                          bool
	AcceptMissingCredentialEvidence bool
	Confirmation                    *MissingCredentialLedgerRecoveryConfirmation
}

type MissingCredentialLedgerRecoveryResult struct {
	Plan            MissingCredentialLedgerRecoveryPlan `json:"plan"`
	Applied         bool                                `json:"applied"`
	ForensicRecord  string                              `json:"forensic_record,omitempty"`
	AuditIntent     string                              `json:"audit_intent,omitempty"`
	AuditCompletion string                              `json:"audit_completion,omitempty"`
}

type credentialRecoverySnapshot struct {
	summary     MissingCredentialLedgerRecoverySource
	quarantines []PeerCredentialQuarantine
}

type credentialRecoveryCandidate struct {
	plan              MissingCredentialLedgerRecoveryPlan
	target            Config
	activePayload     []byte
	restoreCheckpoint bool
}

type credentialRecoveryAudit struct {
	Version        int                                         `json:"version"`
	Event          string                                      `json:"event"`
	CreatedAt      string                                      `json:"created_at"`
	Confirmation   MissingCredentialLedgerRecoveryConfirmation `json:"confirmation"`
	Sources        []MissingCredentialLedgerRecoverySource     `json:"sources"`
	ForensicRecord string                                      `json:"forensic_record,omitempty"`
	Published      bool                                        `json:"published"`
}

// InspectStoreMissingCredentialLedgerRecovery builds a stable dry-run plan.
// The caller still has to present this exact confirmation during apply.
func InspectStoreMissingCredentialLedgerRecovery(store *statestore.Store) (MissingCredentialLedgerRecoveryPlan, error) {
	if store == nil {
		return MissingCredentialLedgerRecoveryPlan{}, configErrorf("config.store_required")
	}
	var plan MissingCredentialLedgerRecoveryPlan
	err := withStoreConfigLock(store, func() error {
		candidate, err := buildMissingCredentialLedgerRecoveryCandidate(store)
		if err != nil {
			return err
		}
		plan = candidate.plan
		return nil
	})
	return plan, err
}

// RecoverStoreMissingCredentialLedger reconstructs a missing credential
// quarantine ledger from protected evidence, or performs the explicitly
// accepted fail-closed fallback. Daemon exclusion is enforced by the caller's
// maintenance lease; this function additionally serializes config writers.
func RecoverStoreMissingCredentialLedger(
	store *statestore.Store,
	options MissingCredentialLedgerRecoveryOptions,
) (MissingCredentialLedgerRecoveryResult, error) {
	return recoverStoreMissingCredentialLedger(store, options, nil)
}

func recoverStoreMissingCredentialLedger(
	store *statestore.Store,
	options MissingCredentialLedgerRecoveryOptions,
	stageHook func(string) error,
) (MissingCredentialLedgerRecoveryResult, error) {
	if store == nil {
		return MissingCredentialLedgerRecoveryResult{}, configErrorf("config.store_required")
	}
	var result MissingCredentialLedgerRecoveryResult
	err := withStoreConfigLock(store, func() error {
		candidate, err := buildMissingCredentialLedgerRecoveryCandidate(store)
		if err != nil {
			return err
		}
		result.Plan = candidate.plan
		if options.DryRun {
			return nil
		}
		if candidate.plan.CredentialEvidenceUnverified != options.AcceptMissingCredentialEvidence {
			if candidate.plan.CredentialEvidenceUnverified {
				return configErrorf("config.credential_recovery_acceptance_required")
			}
			return configErrorf("config.credential_recovery_acceptance_unexpected")
		}
		if options.Confirmation == nil {
			return configErrorf("config.credential_recovery_confirmation_required")
		}
		matches, err := equalCredentialRecoveryConfirmation(
			candidate.plan.Confirmation,
			*options.Confirmation,
		)
		if err != nil {
			return err
		}
		if !matches {
			return configErrorf("config.credential_recovery_confirmation_mismatch")
		}

		forensicName, err := store.CreateCredentialRecoveryForensic(candidate.activePayload)
		if err != nil {
			return configErrorf("config.credential_recovery_forensic_write: %w", err)
		}
		result.ForensicRecord = forensicName
		if err := runCredentialRecoveryStageHook(stageHook, "forensic_saved"); err != nil {
			return err
		}

		intent, err := encodeCredentialRecoveryAudit(
			"intent",
			candidate.plan,
			forensicName,
			false,
		)
		if err != nil {
			return err
		}
		intentName, err := store.CreateCredentialRecoveryAudit(intent)
		if err != nil {
			return configErrorf("config.credential_recovery_audit_intent_write: %w", err)
		}
		result.AuditIntent = intentName
		if err := runCredentialRecoveryStageHook(stageHook, "intent_saved"); err != nil {
			return err
		}

		if err := persistStoreConfigRevisionHighWater(store, candidate.plan.Confirmation.AfterRevision); err != nil {
			if errors.Is(err, statestore.ErrCommitOutcomeUnknown) || errors.Is(err, ErrCommitOutcomeUnknown) {
				return credentialRecoveryPartialError("revision_reserve", err)
			}
			return configErrorf("config.credential_recovery_revision_reserve: %w", err)
		}
		if err := runCredentialRecoveryStageHook(stageHook, "revision_reserved"); err != nil {
			return credentialRecoveryPartialError("revision_reserved", err)
		}

		persisted, err := persistStorePeerCredentialLedger(
			store,
			candidate.plan.Confirmation.Quarantines,
		)
		if err != nil {
			return credentialRecoveryPartialError("ledger_write", err)
		}
		if !equalPeerCredentialQuarantineSets(persisted, candidate.plan.Confirmation.Quarantines) {
			return credentialRecoveryPartialError(
				"ledger_write",
				configErrorf("config.peer_credential_ledger_stale"),
			)
		}
		if err := runCredentialRecoveryStageHook(stageHook, "ledger_persisted"); err != nil {
			return credentialRecoveryPartialError("ledger_persisted", err)
		}

		var publishErr error
		if candidate.restoreCheckpoint {
			publishErr = saveRestoredStoreUnlocked(store, candidate.target, candidate.activePayload)
		} else {
			publishErr = saveStoreUnlocked(store, candidate.target)
		}
		if publishErr != nil {
			return credentialRecoveryPartialError("config_publish", publishErr)
		}
		result.Applied = true
		if err := runCredentialRecoveryStageHook(stageHook, "config_published"); err != nil {
			return credentialRecoveryPartialError("config_published", err)
		}

		completion, err := encodeCredentialRecoveryAudit(
			"complete",
			candidate.plan,
			forensicName,
			true,
		)
		if err != nil {
			return credentialRecoveryPartialError("audit_complete_encode", err)
		}
		completionName, err := store.CreateCredentialRecoveryAudit(completion)
		if err != nil {
			return credentialRecoveryPartialError("audit_complete_write", err)
		}
		result.AuditCompletion = completionName
		if err := runCredentialRecoveryStageHook(stageHook, "completion_saved"); err != nil {
			return credentialRecoveryPartialError("completion_saved", err)
		}
		return nil
	})
	return result, err
}

func buildMissingCredentialLedgerRecoveryCandidate(store *statestore.Store) (credentialRecoveryCandidate, error) {
	activePayload, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_active_unavailable: %w", err)
	}
	if err := guardRestoreSchema(activePayload); err != nil {
		return credentialRecoveryCandidate{}, err
	}
	if _, exists, err := loadStorePeerCredentialLedger(store); err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.restore_credential_ledger_unavailable: %w", err)
	} else if exists {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_ledger_present")
	}
	active, activeHistoricalSchema, activeErr := decodeCredentialRecoveryActive(activePayload)
	activeValid := activeErr == nil
	if activeValid {
		activeErr = Validate(active)
		activeValid = activeErr == nil
	}

	checkpointPayload, checkpointReadErr := store.Read(statestore.LastKnownGood, maxConfigFileBytes)
	checkpointPresent := checkpointReadErr == nil
	var checkpoint Config
	if checkpointPresent {
		checkpoint, err = decodeLastKnownGood(checkpointPayload)
		if err != nil {
			return credentialRecoveryCandidate{}, configErrorf("config.restore_checkpoint_unavailable: %w", err)
		}
	} else if errors.Is(checkpointReadErr, fs.ErrNotExist) && activeValid {
		checkpoint = active
		checkpointPayload = nil
	} else {
		return credentialRecoveryCandidate{}, configErrorf(
			"config.restore_checkpoint_unavailable: %w",
			checkpointReadErr,
		)
	}
	if activeValid && checkpointPresent && active.Node.NodeID != checkpoint.Node.NodeID {
		return credentialRecoveryCandidate{}, configErrorf(
			"config.credential_recovery_node_id_mismatch: active %q checkpoint %q",
			active.Node.NodeID,
			checkpoint.Node.NodeID,
		)
	}

	snapshots := make([]credentialRecoverySnapshot, 0, 5)
	snapshots = append(snapshots,
		inspectCredentialRecoverySource("active", "active-config", activePayload, true),
	)
	if checkpointPresent {
		snapshots = append(snapshots,
			inspectCredentialRecoverySource("last_known_good", "last-known-good.json", checkpointPayload, true),
		)
	} else {
		snapshots = append(snapshots, credentialRecoverySnapshot{summary: MissingCredentialLedgerRecoverySource{
			Kind: "last_known_good", Name: "last-known-good.json",
		}})
	}
	for _, object := range []struct {
		kind   string
		name   string
		object statestore.Object
	}{
		{kind: "rejected", name: "rejected-config.json", object: statestore.RejectedConfig},
		{kind: "pre_migration", name: "pre-migration-config.json", object: statestore.PreMigrationConfig},
	} {
		payload, readErr := store.Read(object.object, maxConfigFileBytes)
		if errors.Is(readErr, fs.ErrNotExist) {
			snapshots = append(snapshots, credentialRecoverySnapshot{summary: MissingCredentialLedgerRecoverySource{
				Kind: object.kind, Name: object.name,
			}})
			continue
		}
		if readErr != nil {
			return credentialRecoveryCandidate{}, configErrorf(
				"config.credential_recovery_source_unavailable: %s: %w",
				object.kind,
				readErr,
			)
		}
		snapshots = append(snapshots, inspectCredentialRecoverySource(object.kind, object.name, payload, true))
	}
	backupNames, err := store.BackupNames()
	if err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.restore_backup_list: %w", err)
	}
	for _, name := range backupNames {
		payload, err := store.ReadBackup(name, maxConfigFileBytes)
		if err != nil {
			return credentialRecoveryCandidate{}, configErrorf("config.restore_backup_read: %s: %w", name, err)
		}
		snapshots = append(snapshots, inspectCredentialRecoverySource("backup", name, payload, true))
	}

	rawEvidence := append([]PeerCredentialQuarantine(nil), checkpoint.PeerCredentialQuarantines...)
	canonicalEvidence, mergeErr := mergePeerCredentialQuarantines(nil, checkpoint.PeerCredentialQuarantines)
	if mergeErr != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_checkpoint_evidence_invalid: %w", mergeErr)
	}
	uncertain := !activeValid || activeHistoricalSchema || !checkpointPresent
	for _, snapshot := range snapshots {
		if snapshot.summary.Present {
			rawEvidence = append(rawEvidence, snapshot.quarantines...)
			merged, err := mergePeerCredentialQuarantines(canonicalEvidence, snapshot.quarantines)
			if err != nil {
				uncertain = true
			} else {
				canonicalEvidence = merged
			}
		}
		if !snapshot.summary.Present {
			continue
		}
		if snapshot.summary.Issue != "" || !snapshot.summary.QuarantinesRecoverable {
			uncertain = true
		}
		if snapshot.summary.NodeID != checkpoint.Node.NodeID {
			uncertain = true
		}
		if len(snapshot.quarantines) != 0 && snapshot.summary.Revision == nil {
			uncertain = true
		}
	}

	target := checkpoint
	restoreCheckpoint := !activeValid
	if activeValid {
		target = active
	}

	unverified := uncertain || len(canonicalEvidence) == 0
	quarantines := canonicalEvidence
	mode := CredentialRecoveryModeProtectedSourceUnion
	if unverified {
		mode = CredentialRecoveryModeEvidenceUnverified
		target.PeerCredentialQuarantines = nil
		quarantines, err = unverifiedConfigCredentialQuarantines(target, rawEvidence)
		if err != nil {
			return credentialRecoveryCandidate{}, err
		}
	}
	if _, err := mergeCredentialLedgerIntoConfig(&target, quarantines); err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_quarantine_apply: %w", err)
	}
	backupRevisionHighWater, err := storeBackupRevisionHighWater(store)
	if err != nil {
		return credentialRecoveryCandidate{}, err
	}
	reservedRevisionHighWater, exists, err := loadStoreConfigRevisionHighWater(store)
	if err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.restore_revision_high_water_unavailable: %w", err)
	}
	if !exists {
		reservedRevisionHighWater = -1
	}
	activeRevision, activeRevisionKnown := recoverableDocumentRevision(activePayload, nil)
	expectedRevision := checkpoint.Revision
	if activeRevisionKnown {
		expectedRevision = activeRevision
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
		return credentialRecoveryCandidate{}, err
	}
	target.Revision = afterRevision
	if err := Validate(target); err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_target_invalid: %w", err)
	}

	sources := make([]MissingCredentialLedgerRecoverySource, len(snapshots))
	for index := range snapshots {
		sources[index] = snapshots[index].summary
	}
	sourceSetSHA, err := canonicalJSONSHA256(sources)
	if err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_source_hash: %w", err)
	}
	quarantineSetSHA, err := canonicalJSONSHA256(quarantines)
	if err != nil {
		return credentialRecoveryCandidate{}, configErrorf("config.credential_recovery_quarantine_hash: %w", err)
	}
	confirmation := MissingCredentialLedgerRecoveryConfirmation{
		Version:                      missingCredentialLedgerRecoveryVersion,
		Mode:                         mode,
		NodeID:                       checkpoint.Node.NodeID,
		CheckpointRevision:           checkpoint.Revision,
		BeforeRevision:               beforeRevision,
		AfterRevision:                afterRevision,
		ActiveSHA256:                 bytesSHA256(activePayload),
		SourceSetSHA256:              sourceSetSHA,
		QuarantineSetSHA256:          quarantineSetSHA,
		CredentialEvidenceUnverified: unverified,
		Quarantines:                  cloneCredentialQuarantines(quarantines),
	}
	return credentialRecoveryCandidate{
		plan: MissingCredentialLedgerRecoveryPlan{
			Mode:                         mode,
			CredentialEvidenceUnverified: unverified,
			Sources:                      sources,
			Confirmation:                 confirmation,
		},
		target:            target,
		activePayload:     append([]byte(nil), activePayload...),
		restoreCheckpoint: restoreCheckpoint,
	}, nil
}

func decodeCredentialRecoveryActive(payload []byte) (Config, bool, error) {
	schema, versioned, err := configSchemaFromJSON(payload)
	if err != nil {
		return Config{}, false, err
	}
	if versioned && schema > CurrentSchemaVersion {
		return Config{}, false, configErrorf(
			"config.restore_schema_newer: have %d support %d",
			schema,
			CurrentSchemaVersion,
		)
	}
	historical := !versioned || schema < CurrentSchemaVersion
	if !historical {
		cfg, err := decodeRepairableConfig(payload)
		return cfg, false, err
	}
	cfg, err := decodeMigratableConfig(payload, schema, versioned)
	if err != nil {
		return Config{}, true, err
	}
	if _, err := quarantinePeerCredentialCollisions(&cfg); err != nil {
		return Config{}, true, markContentError(err)
	}
	return cfg, true, nil
}

func inspectCredentialRecoverySource(kind, name string, payload []byte, protected bool) credentialRecoverySnapshot {
	summary := MissingCredentialLedgerRecoverySource{
		Kind: kind, Name: name, Present: true, Protected: protected,
		Size: len(payload), SHA256: bytesSHA256(payload),
	}
	schema, versioned, schemaErr := configSchemaFromJSON(payload)
	historicalSchema := schemaErr == nil && (!versioned || schema < CurrentSchemaVersion)
	if schemaErr == nil {
		if versioned {
			value := schema
			summary.SchemaVersion = &value
			if schema > CurrentSchemaVersion {
				summary.Issue = "schema_newer"
			}
		}
	} else {
		summary.Issue = "document_unreadable"
	}
	if schemaErr == nil && summary.Issue == "" {
		var strict Config
		var strictErr error
		if versioned && schema == CurrentSchemaVersion {
			strict, strictErr = decodeRepairableConfig(payload)
		} else {
			strict, strictErr = decodeMigratableConfig(payload, schema, versioned)
		}
		if strictErr != nil {
			summary.Issue = "document_invalid"
		} else {
			summary.NodeID = strict.Node.NodeID
			revision := strict.Revision
			summary.Revision = &revision
		}
	}
	if revision, known := recoverableBackupRevision(payload); known {
		value := revision
		summary.Revision = &value
	}
	if summary.NodeID == "" && preflightConfigJSON(payload) == nil {
		var envelope struct {
			Node struct {
				NodeID string `json:"node_id"`
			} `json:"node"`
		}
		if err := json.Unmarshal(payload, &envelope); err == nil {
			summary.NodeID = envelope.Node.NodeID
		}
	}
	quarantines, known, err := recoverablePeerCredentialQuarantines(payload, nil)
	if err != nil {
		summary.Issue = "quarantine_field_invalid"
		return credentialRecoverySnapshot{summary: summary}
	}
	summary.QuarantinesRecoverable = known
	if !known {
		if summary.Issue == "" {
			summary.Issue = "document_unreadable"
		}
		return credentialRecoverySnapshot{summary: summary}
	}
	canonical, err := mergePeerCredentialQuarantines(nil, quarantines)
	if err != nil {
		summary.Issue = "quarantine_set_invalid"
		return credentialRecoverySnapshot{summary: summary}
	}
	summary.QuarantineCount = len(canonical)
	if len(canonical) != 0 {
		summary.QuarantineSetSHA256, _ = canonicalJSONSHA256(canonical)
	}
	if historicalSchema {
		summary.Issue = "credential_ledger_not_representable"
		summary.QuarantinesRecoverable = false
	}
	return credentialRecoverySnapshot{summary: summary, quarantines: canonical}
}

func unverifiedConfigCredentialQuarantines(
	cfg Config,
	evidence []PeerCredentialQuarantine,
) ([]PeerCredentialQuarantine, error) {
	type record struct {
		nodeIDs map[string]struct{}
	}
	records := make(map[string]*record)
	add := func(fingerprint string, nodeIDs []string) {
		entry := records[fingerprint]
		if entry == nil {
			entry = &record{nodeIDs: map[string]struct{}{}}
			records[fingerprint] = entry
		}
		for _, nodeID := range nodeIDs {
			if nodeID != "" {
				entry.nodeIDs[nodeID] = struct{}{}
			}
		}
	}
	for _, quarantine := range evidence {
		add(quarantine.CredentialFingerprint, quarantine.PeerNodeIDs)
	}
	profileNodeIDs := make(map[string][]string)
	var collectPeers func([]PeerConfig)
	collectPeers = func(peers []PeerConfig) {
		for _, peer := range peers {
			profileNodeIDs[peer.XrayProfileID] = append(profileNodeIDs[peer.XrayProfileID], peer.NodeID)
			collectPeers(peer.Children)
		}
	}
	collectPeers(cfg.Peers)
	for profileID, profile := range cfg.XrayProfiles {
		if profile.Kind != "vless" || profile.VLESS == nil {
			continue
		}
		fingerprint, err := xraycredential.VLESSFingerprint(profile.VLESS.UUID)
		if err != nil {
			return nil, configErrorf("config.credential_recovery_profile_invalid: %s", profileID)
		}
		add(fingerprint, profileNodeIDs[profileID])
	}
	result := make([]PeerCredentialQuarantine, 0, len(records))
	for fingerprint, entry := range records {
		nodeIDs := make([]string, 0, len(entry.nodeIDs))
		for nodeID := range entry.nodeIDs {
			nodeIDs = append(nodeIDs, nodeID)
		}
		sort.Strings(nodeIDs)
		result = append(result, PeerCredentialQuarantine{
			CredentialFingerprint: fingerprint,
			PeerNodeIDs:           nodeIDs,
			Reason:                PeerCredentialEvidenceUnverifiedReason,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CredentialFingerprint < result[j].CredentialFingerprint
	})
	if _, err := validatePeerCredentialQuarantines(result); err != nil {
		return nil, configErrorf("config.credential_recovery_quarantine_invalid: %w", err)
	}
	return result, nil
}

func cloneCredentialQuarantines(source []PeerCredentialQuarantine) []PeerCredentialQuarantine {
	clone := append([]PeerCredentialQuarantine(nil), source...)
	for index := range clone {
		clone[index].PeerNodeIDs = append([]string(nil), source[index].PeerNodeIDs...)
	}
	return clone
}

func equalPeerCredentialQuarantineSets(first, second []PeerCredentialQuarantine) bool {
	firstHash, firstErr := canonicalJSONSHA256(first)
	secondHash, secondErr := canonicalJSONSHA256(second)
	return firstErr == nil && secondErr == nil && subtle.ConstantTimeCompare([]byte(firstHash), []byte(secondHash)) == 1
}

func equalCredentialRecoveryConfirmation(
	expected MissingCredentialLedgerRecoveryConfirmation,
	provided MissingCredentialLedgerRecoveryConfirmation,
) (bool, error) {
	expectedPayload, err := json.Marshal(expected)
	if err != nil {
		return false, configErrorf("config.credential_recovery_confirmation_encode: %w", err)
	}
	providedPayload, err := json.Marshal(provided)
	if err != nil {
		return false, configErrorf("config.credential_recovery_confirmation_encode: %w", err)
	}
	expectedDigest := sha256.Sum256(expectedPayload)
	providedDigest := sha256.Sum256(providedPayload)
	return subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) == 1, nil
}

func encodeCredentialRecoveryAudit(
	event string,
	plan MissingCredentialLedgerRecoveryPlan,
	forensicRecord string,
	published bool,
) ([]byte, error) {
	payload, err := json.MarshalIndent(credentialRecoveryAudit{
		Version:        missingCredentialLedgerRecoveryVersion,
		Event:          event,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		Confirmation:   plan.Confirmation,
		Sources:        plan.Sources,
		ForensicRecord: forensicRecord,
		Published:      published,
	}, "", "  ")
	if err != nil {
		return nil, configErrorf("config.credential_recovery_audit_encode: %w", err)
	}
	return append(payload, '\n'), nil
}

func canonicalJSONSHA256(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return bytesSHA256(payload), nil
}

func bytesSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func runCredentialRecoveryStageHook(hook func(string) error, stage string) error {
	if hook == nil {
		return nil
	}
	if err := hook(stage); err != nil {
		return configErrorf("config.credential_recovery_interrupted: %s: %w", stage, err)
	}
	return nil
}

func credentialRecoveryPartialError(stage string, err error) error {
	return configErrorf(
		"config.credential_recovery_outcome_indeterminate: stage=%s: %w",
		stage,
		errors.Join(ErrCommitOutcomeUnknown, err),
	)
}

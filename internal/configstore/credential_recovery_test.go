package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

func TestMissingCredentialLedgerRecoveryRebuildsProtectedEvidence(t *testing.T) {
	store, checkpoint, _ := prepareMissingCredentialLedgerRecovery(t, true)
	defer store.Close()
	active, activePayload := installValidPreQuarantineRecoveryActive(t, store, checkpoint)
	if _, _, err := LoadStoreOrMigrate(store, true); err == nil ||
		!strings.Contains(err.Error(), "config.peer_credential_ledger_missing") {
		t.Fatalf("ordinary load without ledger error=%v", err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != CredentialRecoveryModeProtectedSourceUnion || plan.CredentialEvidenceUnverified {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Confirmation.CheckpointRevision != checkpoint.Revision ||
		plan.Confirmation.AfterRevision <= checkpoint.Revision ||
		plan.Confirmation.BeforeRevision != active.Revision ||
		plan.Confirmation.ActiveSHA256 != bytesSHA256(activePayload) ||
		len(plan.Confirmation.Quarantines) != 1 {
		t.Fatalf("confirmation=%+v", plan.Confirmation)
	}

	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		Confirmation: &plan.Confirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.ForensicRecord == "" || result.AuditIntent == "" || result.AuditCompletion == "" {
		t.Fatalf("result=%+v", result)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != plan.Confirmation.AfterRevision || len(restored.PeerCredentialQuarantines) != 1 {
		t.Fatalf("restored=%+v", restored)
	}
	if restored.Node.DisplayName != active.Node.DisplayName {
		t.Fatalf("valid active config was replaced instead of recovered in place: node=%+v", restored.Node)
	}
	if restored.Peers[0].Enabled || !IsPeerCredentialQuarantined(restored.Peers[0]) || restored.NodeInbound[0].Enabled {
		t.Fatalf("unsafe dependencies survived recovery: peer=%+v inbound=%+v", restored.Peers[0], restored.NodeInbound[0])
	}
	forensic, err := store.ReadCredentialRecoveryRecord(result.ForensicRecord, maxConfigFileBytes)
	if err != nil || !bytes.Equal(forensic, activePayload) {
		t.Fatalf("forensic=%q err=%v", forensic, err)
	}
	for _, name := range []string{result.AuditIntent, result.AuditCompletion} {
		audit, err := store.ReadCredentialRecoveryRecord(name, maxConfigFileBytes)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(audit, []byte(checkpoint.XrayProfiles["vless"].VLESS.UUID)) {
			t.Fatalf("audit %s contains a raw VLESS credential", name)
		}
	}
}

func TestMissingCredentialLedgerRecoveryUsesMonotonicUnion(t *testing.T) {
	store, checkpoint, _ := prepareMissingCredentialLedgerRecovery(t, true)
	defer store.Close()
	installValidPreQuarantineRecoveryActive(t, store, checkpoint)

	other := credentialRecoveryRecord(t, "ea594ad3-7040-4e9c-9fdc-53d3401f4474", "node-old")
	historical := DefaultConfig()
	historical.Revision = checkpoint.Revision - 1
	historical.PeerCredentialQuarantines = []PeerCredentialQuarantine{other}
	payload, err := EncodeDocument(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackup(payload); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CredentialEvidenceUnverified || len(plan.Confirmation.Quarantines) != 2 {
		t.Fatalf("union plan=%+v", plan)
	}
	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		Confirmation: &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ledger, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil || !exists || len(ledger) != 2 {
		t.Fatalf("ledger=%+v exists=%v err=%v", ledger, exists, err)
	}
}

func TestMissingCredentialLedgerRecoveryTreatsUnknownHistoricalFieldsAsUnverified(t *testing.T) {
	store, checkpoint, _ := prepareMissingCredentialLedgerRecovery(t, true)
	defer store.Close()
	installValidPreQuarantineRecoveryActive(t, store, checkpoint)

	historical := DefaultConfig()
	historical.Revision = checkpoint.Revision - 1
	payload, err := EncodeDocument(historical)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	document["future_extension"] = json.RawMessage("true")
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackup(payload); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CredentialEvidenceUnverified || plan.Mode != CredentialRecoveryModeEvidenceUnverified {
		t.Fatalf("plan=%+v", plan)
	}
	foundInvalid := false
	for _, source := range plan.Sources {
		if source.Kind == "backup" && source.Issue == "document_invalid" {
			foundInvalid = true
		}
	}
	if !foundInvalid {
		t.Fatalf("unknown-field backup was not classified invalid: %+v", plan.Sources)
	}
}

func TestMissingCredentialLedgerRecoveryRejectsValidActiveIdentityMismatch(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := validRuntimeConfigForValidation()
	checkpoint.Revision = 4
	checkpoint.Node.NodeID = testLegacyNodeID
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	active := checkpoint
	active.Node.NodeID = "node-fedcba9876543210fedcba9876543210"
	payload, err := EncodeDocument(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig(payload); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectStoreMissingCredentialLedgerRecovery(store); err == nil ||
		!strings.Contains(err.Error(), "config.credential_recovery_node_id_mismatch") {
		t.Fatalf("identity mismatch error=%v", err)
	}
	assertMissingRecoveryUnmodified(t, store, payload)
}

func TestMissingCredentialLedgerRecoveryRejectsValidV1ActiveIdentityMismatch(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := validRuntimeConfigForValidation()
	checkpoint.Revision = 4
	checkpoint.Node.NodeID = testLegacyNodeID
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	active := checkpoint
	active.Node.NodeID = "node-fedcba9876543210fedcba9876543210"
	payload := encodeCredentialRecoveryV1(t, active)
	if err := store.ReplaceConfig(payload); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectStoreMissingCredentialLedgerRecovery(store); err == nil ||
		!strings.Contains(err.Error(), "config.credential_recovery_node_id_mismatch") {
		t.Fatalf("v1 identity mismatch error=%v", err)
	}
	assertMissingRecoveryUnmodified(t, store, payload)
}

func TestMissingCredentialLedgerRecoveryPreservesV1ActiveAndDisablesEveryVLESSProfile(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := validRuntimeConfigForValidation()
	checkpoint.Revision = 4
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	active := checkpoint
	active.Revision = 5
	active.Node.DisplayName = "preserve-v1-active"
	active.PeerCredentialQuarantines = nil
	active.Peers[0].Enabled = true
	active.Peers[0].DisabledCause = ""
	active.NodeInbound[0].Enabled = true
	active.NodeInbound[0].DisabledCause = ""
	payload := encodeCredentialRecoveryV1(t, active)
	if err := store.ReplaceConfig(payload); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CredentialEvidenceUnverified || plan.Mode != CredentialRecoveryModeEvidenceUnverified {
		t.Fatalf("v1 recovery plan=%+v", plan)
	}
	foundHistorical := false
	for _, source := range plan.Sources {
		if source.Kind == "active" && source.Issue == "credential_ledger_not_representable" &&
			!source.QuarantinesRecoverable {
			foundHistorical = true
		}
	}
	if !foundHistorical {
		t.Fatalf("v1 source was not marked incomplete: %+v", plan.Sources)
	}
	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: true,
		Confirmation:                    &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("v1 recovery result=%+v err=%v", result, err)
	}
	recovered, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SchemaVersion != CurrentSchemaVersion || recovered.Node.DisplayName != active.Node.DisplayName ||
		recovered.Revision <= active.Revision {
		t.Fatalf("v1 active settings were not preserved: %+v", recovered)
	}
	if recovered.Peers[0].Enabled || recovered.Peers[0].DisabledCause != PeerCredentialEvidenceUnverifiedCause ||
		recovered.NodeInbound[0].Enabled ||
		recovered.NodeInbound[0].DisabledCause != PeerCredentialEvidenceUnverifiedInboundCause {
		t.Fatalf("v1 credentials were not disabled: peer=%+v inbound=%+v", recovered.Peers[0], recovered.NodeInbound[0])
	}
}

func TestMissingCredentialLedgerRecoveryPreservesUnversionedActiveAndDisablesEveryVLESSProfile(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := validRuntimeConfigForValidation()
	checkpoint.Revision = 4
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	active := checkpoint
	active.Revision = 5
	active.Node.DisplayName = "preserve-unversioned-active"
	active.PeerCredentialQuarantines = nil
	active.Peers[0].Enabled = true
	active.Peers[0].DisabledCause = ""
	active.NodeInbound[0].Enabled = true
	active.NodeInbound[0].DisabledCause = ""
	payload := encodeCredentialRecoveryUnversioned(t, active)
	if err := store.ReplaceConfig(payload); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CredentialEvidenceUnverified || plan.Mode != CredentialRecoveryModeEvidenceUnverified ||
		plan.Confirmation.NodeID != active.Node.NodeID {
		t.Fatalf("unversioned recovery plan=%+v", plan)
	}
	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: true,
		Confirmation:                    &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("unversioned recovery result=%+v err=%v", result, err)
	}
	recovered, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SchemaVersion != CurrentSchemaVersion || recovered.Node.DisplayName != active.Node.DisplayName ||
		recovered.Revision <= active.Revision || recovered.Peers[0].Enabled ||
		recovered.Peers[0].DisabledCause != PeerCredentialEvidenceUnverifiedCause {
		t.Fatalf("unversioned active was not preserved fail-closed: %+v", recovered)
	}
}

func TestMissingCredentialLedgerRecoveryWithoutCheckpointUsesValidActiveUnverified(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	active := validRuntimeConfigForValidation()
	active.Revision = 3
	if err := SaveStore(store, active); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CredentialEvidenceUnverified || plan.Confirmation.NodeID != active.Node.NodeID {
		t.Fatalf("active-only recovery plan=%+v", plan)
	}
	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: true,
		Confirmation:                    &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("active-only recovery result=%+v err=%v", result, err)
	}
	assertRecoveredPeersDisabled(t, store)
}

func TestMissingCredentialLedgerRecoveryRequiresExplicitUnverifiedAcceptance(t *testing.T) {
	store, checkpoint, invalid := prepareMissingCredentialLedgerRecovery(t, false)
	defer store.Close()

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != CredentialRecoveryModeEvidenceUnverified || !plan.CredentialEvidenceUnverified ||
		len(plan.Confirmation.Quarantines) != 1 ||
		plan.Confirmation.Quarantines[0].Reason != PeerCredentialEvidenceUnverifiedReason {
		t.Fatalf("plan=%+v", plan)
	}
	if result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		Confirmation: &plan.Confirmation,
	}); err == nil || !strings.Contains(err.Error(), "config.credential_recovery_acceptance_required") || result.Applied {
		t.Fatalf("unaccepted result=%+v err=%v", result, err)
	}
	assertMissingRecoveryUnmodified(t, store, invalid)

	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: true,
		Confirmation:                    &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("accepted result=%+v err=%v", result, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Peers[0].Enabled || restored.Peers[0].DisabledCause != PeerCredentialEvidenceUnverifiedCause {
		t.Fatalf("peer not fail-closed: %+v", restored.Peers[0])
	}
	if restored.NodeInbound[0].Enabled ||
		restored.NodeInbound[0].DisabledCause != PeerCredentialEvidenceUnverifiedInboundCause {
		t.Fatalf("dependent inbound stayed enabled: %+v", restored.NodeInbound[0])
	}
	if checkpoint.XrayProfiles["vless"].VLESS.UUID == "" {
		t.Fatal("test checkpoint lost its credential")
	}
}

func TestMissingCredentialLedgerRecoveryConfirmationBindsSourceSet(t *testing.T) {
	store, _, invalid := prepareMissingCredentialLedgerRecovery(t, true)
	defer store.Close()
	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("{\n  \"revision\": 999")
	if err := store.ReplaceConfig(changed); err != nil {
		t.Fatal(err)
	}
	if result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: plan.CredentialEvidenceUnverified,
		Confirmation:                    &plan.Confirmation,
	}); err == nil || !strings.Contains(err.Error(), "config.credential_recovery_confirmation_mismatch") || result.Applied {
		t.Fatalf("changed-source result=%+v err=%v", result, err)
	}
	active, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil || !bytes.Equal(active, changed) || bytes.Equal(active, invalid) {
		t.Fatalf("active=%q err=%v", active, err)
	}
	if names, err := store.CredentialRecoveryRecordNames(); err != nil || len(names) != 0 {
		t.Fatalf("records=%v err=%v", names, err)
	}
}

func TestMissingCredentialLedgerRecoveryNormalizesConflictingReasonsWhenEvidenceIsUncertain(t *testing.T) {
	store, checkpoint, _ := prepareMissingCredentialLedgerRecovery(t, true)
	defer store.Close()
	if err := store.Replace(statestore.RejectedConfig, []byte("{\n")); err != nil {
		t.Fatal(err)
	}

	plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CredentialEvidenceUnverified || len(plan.Confirmation.Quarantines) != 1 ||
		plan.Confirmation.Quarantines[0].Reason != PeerCredentialEvidenceUnverifiedReason {
		t.Fatalf("plan=%+v", plan)
	}
	result, err := RecoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
		AcceptMissingCredentialEvidence: true,
		Confirmation:                    &plan.Confirmation,
	})
	if err != nil || !result.Applied {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertRecoveredPeersDisabled(t, store)
	loaded, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Peers[0].DisabledCause != PeerCredentialEvidenceUnverifiedCause ||
		loaded.NodeInbound[0].DisabledCause != PeerCredentialEvidenceUnverifiedInboundCause ||
		checkpoint.XrayProfiles["vless"].VLESS.UUID == "" {
		t.Fatalf("uncertain evidence was not normalized fail-closed: peer=%+v inbound=%+v", loaded.Peers[0], loaded.NodeInbound[0])
	}
}

func TestMissingCredentialLedgerRecoveryCrashStagesFailClosed(t *testing.T) {
	stages := []string{
		"forensic_saved",
		"intent_saved",
		"revision_reserved",
		"ledger_persisted",
		"config_published",
		"completion_saved",
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			store, checkpoint, invalid := prepareMissingCredentialLedgerRecovery(t, false)
			defer store.Close()
			plan, err := InspectStoreMissingCredentialLedgerRecovery(store)
			if err != nil {
				t.Fatal(err)
			}
			result, err := recoverStoreMissingCredentialLedger(store, MissingCredentialLedgerRecoveryOptions{
				AcceptMissingCredentialEvidence: true,
				Confirmation:                    &plan.Confirmation,
			}, func(current string) error {
				if current == stage {
					return errors.New("injected interruption")
				}
				return nil
			})
			if err == nil {
				t.Fatalf("stage %s unexpectedly succeeded: %+v", stage, result)
			}

			ledger, ledgerExists, ledgerErr := loadStorePeerCredentialLedger(store)
			if ledgerErr != nil {
				t.Fatal(ledgerErr)
			}
			active, readErr := store.ReadConfig(maxConfigFileBytes)
			if readErr != nil {
				t.Fatal(readErr)
			}
			published := stage == "config_published" || stage == "completion_saved"
			if !published {
				if !bytes.Equal(active, invalid) {
					t.Fatalf("stage %s published active config", stage)
				}
				if ledgerExists {
					if len(ledger) != 1 || ledger[0].Reason != PeerCredentialEvidenceUnverifiedReason {
						t.Fatalf("stage %s ledger=%+v", stage, ledger)
					}
					update, restoreErr := RestoreStoreLastKnownGood(store, checkpoint.Revision, false)
					if restoreErr != nil || update.AfterRevision <= plan.Confirmation.AfterRevision {
						t.Fatalf("stage %s continuation=%+v err=%v", stage, update, restoreErr)
					}
					assertRecoveredPeersDisabled(t, store)
				}
				return
			}
			if !ledgerExists || bytes.Equal(active, invalid) {
				t.Fatalf("stage %s ledgerExists=%v active=%q", stage, ledgerExists, active)
			}
			assertRecoveredPeersDisabled(t, store)
		})
	}
}

func TestUnverifiedQuarantineMayRepresentUnboundProfile(t *testing.T) {
	record := credentialRecoveryRecord(t, "f0f5907c-f63f-476a-b5ae-e2cc47448924")
	record.Reason = PeerCredentialEvidenceUnverifiedReason
	record.PeerNodeIDs = nil
	cfg := DefaultConfig()
	cfg.PeerCredentialQuarantines = []PeerCredentialQuarantine{record}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func prepareMissingCredentialLedgerRecovery(
	t *testing.T,
	checkpointHasEvidence bool,
) (*statestore.Store, Config, []byte) {
	t.Helper()
	store := openConfigObjectStore(t)
	checkpoint := validRuntimeConfigForValidation()
	checkpoint.Revision = 4
	if checkpointHasEvidence {
		record := credentialRecoveryRecord(t, checkpoint.XrayProfiles["vless"].VLESS.UUID, checkpoint.Peers[0].NodeID)
		checkpoint.PeerCredentialQuarantines = []PeerCredentialQuarantine{record}
		checkpoint.Peers[0].Enabled = false
		checkpoint.Peers[0].DisabledCause = PeerCredentialQuarantineCause
		checkpoint.NodeInbound[0].Enabled = false
		checkpoint.NodeInbound[0].DisabledCause = PeerCredentialQuarantineInboundCause
	}
	if err := SaveStore(store, checkpoint); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		store.Close()
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		store.Close()
		t.Fatal(err)
	}
	invalid := []byte("{\n")
	if err := store.ReplaceConfig(invalid); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, checkpoint, invalid
}

func installValidPreQuarantineRecoveryActive(
	t *testing.T,
	store *statestore.Store,
	checkpoint Config,
) (Config, []byte) {
	t.Helper()
	checkpointPayload, err := EncodeDocument(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	active, err := DecodeDocument(checkpointPayload)
	if err != nil {
		t.Fatal(err)
	}
	active.Revision--
	active.Node.DisplayName = "valid-active-before-ledger-recovery"
	active.PeerCredentialQuarantines = nil
	active.Peers[0].Enabled = true
	active.Peers[0].DisabledCause = ""
	active.NodeInbound[0].Enabled = true
	active.NodeInbound[0].DisabledCause = ""
	activePayload, err := EncodeDocument(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig(activePayload); err != nil {
		t.Fatal(err)
	}
	return active, activePayload
}

func credentialRecoveryRecord(t *testing.T, credential string, nodeIDs ...string) PeerCredentialQuarantine {
	t.Helper()
	fingerprint, err := xraycredential.VLESSFingerprint(credential)
	if err != nil {
		t.Fatal(err)
	}
	return PeerCredentialQuarantine{
		CredentialFingerprint: fingerprint,
		PeerNodeIDs:           append([]string(nil), nodeIDs...),
		Reason:                PeerCredentialCollisionReason,
	}
}

func encodeCredentialRecoveryV1(t *testing.T, cfg Config) []byte {
	t.Helper()
	payload, err := json.Marshal(configV1{
		SchemaVersion: 1,
		Revision:      cfg.Revision,
		Node:          cfg.Node,
		System:        cfg.System,
		NodeInbound:   cfg.NodeInbound,
		Peers:         cfg.Peers,
		XrayProfiles:  cfg.XrayProfiles,
		PeerTrust:     cfg.PeerTrust,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, '\n')
}

func encodeCredentialRecoveryUnversioned(t *testing.T, cfg Config) []byte {
	t.Helper()
	payload, err := EncodeDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "schema_version")
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, '\n')
}

func assertMissingRecoveryUnmodified(t *testing.T, store *statestore.Store, wantActive []byte) {
	t.Helper()
	active, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil || !bytes.Equal(active, wantActive) {
		t.Fatalf("active=%q err=%v", active, err)
	}
	if _, exists, err := loadStorePeerCredentialLedger(store); err != nil || exists {
		t.Fatalf("ledger exists=%v err=%v", exists, err)
	}
	if names, err := store.CredentialRecoveryRecordNames(); err != nil || len(names) != 0 {
		t.Fatalf("records=%v err=%v", names, err)
	}
}

func assertRecoveredPeersDisabled(t *testing.T, store *statestore.Store) {
	t.Helper()
	loaded, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Peers) != 1 || loaded.Peers[0].Enabled || !IsPeerCredentialQuarantined(loaded.Peers[0]) {
		t.Fatalf("peer revived: %+v", loaded.Peers)
	}
	if len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].Enabled {
		t.Fatalf("dependent inbound revived: %+v", loaded.NodeInbound)
	}
	ledger, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil || !exists || !reflect.DeepEqual(ledger, loaded.PeerCredentialQuarantines) {
		t.Fatalf("ledger=%+v config=%+v exists=%v err=%v", ledger, loaded.PeerCredentialQuarantines, exists, err)
	}
}

func TestMissingCredentialLedgerRecoveryDoesNotAcceptAbsentActive(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	if _, err := InspectStoreMissingCredentialLedgerRecovery(store); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing active error=%v", err)
	}
}

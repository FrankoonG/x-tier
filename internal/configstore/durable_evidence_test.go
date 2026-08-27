package configstore

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

func TestPathCredentialLedgerAndRevisionReservationAreMonotonic(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	record := credentialLedgerTestRecord(t)
	if _, err := persistPathPeerCredentialLedger(configPath, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	if _, err := persistPathPeerCredentialLedger(configPath, nil); err != nil {
		t.Fatal(err)
	}
	ledger, exists, err := loadPathPeerCredentialLedger(configPath)
	if err != nil || !exists || !reflect.DeepEqual(ledger, []PeerCredentialQuarantine{record}) {
		t.Fatalf("ledger=%+v exists=%v error=%v", ledger, exists, err)
	}
	if err := persistPathConfigRevisionHighWater(configPath, 41); err != nil {
		t.Fatal(err)
	}
	if err := persistPathConfigRevisionHighWater(configPath, 7); err != nil {
		t.Fatal(err)
	}
	revision, exists, err := loadPathConfigRevisionHighWater(configPath)
	if err != nil || !exists || revision != 41 {
		t.Fatalf("revision=%d exists=%v error=%v", revision, exists, err)
	}
}

func TestObjectCredentialLedgerAndRevisionReservationAreMonotonic(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	record := credentialLedgerTestRecord(t)
	if _, err := persistStorePeerCredentialLedger(store, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	if _, err := persistStorePeerCredentialLedger(store, nil); err != nil {
		t.Fatal(err)
	}
	ledger, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil || !exists || !reflect.DeepEqual(ledger, []PeerCredentialQuarantine{record}) {
		t.Fatalf("ledger=%+v exists=%v error=%v", ledger, exists, err)
	}
	if err := persistStoreConfigRevisionHighWater(store, 41); err != nil {
		t.Fatal(err)
	}
	if err := persistStoreConfigRevisionHighWater(store, 7); err != nil {
		t.Fatal(err)
	}
	revision, exists, err := loadStoreConfigRevisionHighWater(store)
	if err != nil || !exists || revision != 41 {
		t.Fatalf("revision=%d exists=%v error=%v", revision, exists, err)
	}
}

func TestSaveCannotDropCredentialLedgerEvidence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := Save(configPath, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	record := credentialLedgerTestRecord(t)
	if _, err := persistPathPeerCredentialLedger(configPath, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	if err := Save(configPath, DefaultConfig()); err == nil || !strings.Contains(err.Error(), "config.peer_credential_ledger_stale") {
		t.Fatalf("save discarded ledger evidence: %v", err)
	}
}

func TestPathLastKnownGoodWriterCannotRaceCredentialLedgerPublication(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- WithLock(configPath, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	writerResult := make(chan error, 1)
	go func() { writerResult <- SaveLastKnownGood(configPath, checkpoint) }()
	select {
	case err := <-writerResult:
		t.Fatalf("last-known-good writer left held config lock early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	record := credentialLedgerTestRecord(t)
	if _, err := persistPathPeerCredentialLedger(configPath, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writerResult:
		if err == nil || !strings.Contains(err.Error(), "config.peer_credential_ledger_stale") {
			t.Fatalf("last-known-good writer error=%v, want stale ledger refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("last-known-good writer did not leave the config lock")
	}
	ledger, exists, err := loadPathPeerCredentialLedger(configPath)
	if err != nil || !exists || !reflect.DeepEqual(ledger, []PeerCredentialQuarantine{record}) {
		t.Fatalf("ledger=%+v exists=%v error=%v", ledger, exists, err)
	}
}

func TestObjectLastKnownGoodWriterCannotRaceCredentialLedgerPublication(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- WithStoreLock(store, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	writerResult := make(chan error, 1)
	go func() { writerResult <- SaveStoreLastKnownGood(store, checkpoint) }()
	select {
	case err := <-writerResult:
		t.Fatalf("last-known-good writer left held config lock early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	record := credentialLedgerTestRecord(t)
	if _, err := persistStorePeerCredentialLedger(store, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writerResult:
		if err == nil || !strings.Contains(err.Error(), "config.peer_credential_ledger_stale") {
			t.Fatalf("last-known-good writer error=%v, want stale ledger refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("last-known-good writer did not leave the config lock")
	}
	ledger, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil || !exists || !reflect.DeepEqual(ledger, []PeerCredentialQuarantine{record}) {
		t.Fatalf("ledger=%+v exists=%v error=%v", ledger, exists, err)
	}
}

func TestPathCASWaitsForShortLastKnownGoodContention(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := Save(configPath, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- withLockedPathKey(configPath, configPath, false, true, func(string) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	result := make(chan struct {
		update UpdateResult
		err    error
	}, 1)
	go func() {
		update, err := UpdatePinnedCAS(configPath, configPath, 0, func(cfg *Config) error {
			cfg.Node.DisplayName = "updated"
			return nil
		})
		result <- struct {
			update UpdateResult
			err    error
		}{update, err}
	}()
	select {
	case early := <-result:
		t.Fatalf("CAS did not wait for short checkpoint contention: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.update != (UpdateResult{BeforeRevision: 0, AfterRevision: 1}) {
			t.Fatalf("CAS result=%+v error=%v", got.update, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CAS did not resume after checkpoint contention")
	}
}

func TestObjectCASWaitsForShortLastKnownGoodContention(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	if err := SaveStore(store, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- WithStoreLock(store, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	result := make(chan struct {
		update UpdateResult
		err    error
	}, 1)
	go func() {
		update, err := UpdateStoreCAS(store, 0, func(cfg *Config) error {
			cfg.Node.DisplayName = "updated"
			return nil
		})
		result <- struct {
			update UpdateResult
			err    error
		}{update, err}
	}()
	select {
	case early := <-result:
		t.Fatalf("CAS did not wait for short checkpoint contention: %+v", early)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.update != (UpdateResult{BeforeRevision: 0, AfterRevision: 1}) {
			t.Fatalf("CAS result=%+v error=%v", got.update, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CAS did not resume after checkpoint contention")
	}
}

func TestPathRestoreRepairsLedgerAheadOfValidActiveConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	record := credentialLedgerTestRecord(t)
	if _, err := persistPathPeerCredentialLedger(configPath, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false)
	if err != nil || result.AfterRevision != 5 {
		t.Fatalf("restore result=%+v error=%v", result, err)
	}
	restored, err := LoadExisting(configPath)
	if err != nil || !reflect.DeepEqual(restored.PeerCredentialQuarantines, []PeerCredentialQuarantine{record}) {
		t.Fatalf("restored quarantines=%+v error=%v", restored.PeerCredentialQuarantines, err)
	}
}

func TestObjectRestoreRepairsLedgerAheadOfValidActiveConfig(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	record := credentialLedgerTestRecord(t)
	if _, err := persistStorePeerCredentialLedger(store, []PeerCredentialQuarantine{record}); err != nil {
		t.Fatal(err)
	}
	result, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, false)
	if err != nil || result.AfterRevision != 5 {
		t.Fatalf("restore result=%+v error=%v", result, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil || !reflect.DeepEqual(restored.PeerCredentialQuarantines, []PeerCredentialQuarantine{record}) {
		t.Fatalf("restored quarantines=%+v error=%v", restored.PeerCredentialQuarantines, err)
	}
}

func TestPathRestoreFailsClosedWhenUnreadableActivePredatesCredentialLedger(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	invalid := []byte("{\n")
	if err := writeFileAtomic(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	payload, err := EncodeDocument(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(LastKnownGoodPath(configPath), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false); err == nil ||
		!strings.Contains(err.Error(), "config.restore_credential_ledger_missing") {
		t.Fatalf("restore without ledger error=%v", err)
	}
	active, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(active, invalid) {
		t.Fatalf("failed restore changed active=%q error=%v", active, err)
	}
}

func TestObjectRestoreFailsClosedWhenUnreadableActivePredatesCredentialLedger(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	if err := store.CreateConfigExclusive([]byte("{\n")); err != nil {
		t.Fatal(err)
	}
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	payload, err := EncodeDocument(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(statestore.LastKnownGood, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, false); err == nil ||
		!strings.Contains(err.Error(), "config.restore_credential_ledger_missing") {
		t.Fatalf("restore without ledger error=%v", err)
	}
}

func TestPathRestoreAcceptsManagedLegacyBackupWithoutRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"node":{},"system":{}}`)
	backup := configPath + ".bak.20000101T000000.000000000"
	if err := writeFileAtomic(backup, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"revision":4`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false)
	if err != nil || result.AfterRevision != 5 {
		t.Fatalf("legacy backup restore result=%+v error=%v", result, err)
	}
}

func TestObjectRestoreAcceptsManagedLegacyBackupWithoutRevision(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackup([]byte(`{"node":{},"system":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig([]byte(`{"revision":4`)); err != nil {
		t.Fatal(err)
	}
	result, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, false)
	if err != nil || result.AfterRevision != 5 {
		t.Fatalf("legacy backup restore result=%+v error=%v", result, err)
	}
}

func credentialLedgerTestRecord(t *testing.T) PeerCredentialQuarantine {
	t.Helper()
	fingerprint, err := xraycredential.VLESSFingerprint("e2f7cbec-a847-44ed-b3df-ceae1f9aa252")
	if err != nil {
		t.Fatal(err)
	}
	return PeerCredentialQuarantine{
		CredentialFingerprint: fingerprint,
		PeerNodeIDs:           []string{"node-a", "node-b"},
		Reason:                PeerCredentialCollisionReason,
	}
}

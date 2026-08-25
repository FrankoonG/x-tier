package configstore

import (
	"path/filepath"
	"testing"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestObjectStorePersistsConfigCASBackupsAndCheckpoint(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()

	cfg, migrated, err := LoadStoreOrMigrate(store, true)
	if err != nil {
		t.Fatal(err)
	}
	if migrated || cfg.Revision != 0 {
		t.Fatalf("initial config migrated=%v revision=%d", migrated, cfg.Revision)
	}
	result, err := UpdateStoreCAS(store, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "first"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 0 || result.AfterRevision != 1 {
		t.Fatalf("CAS result=%+v", result)
	}
	if _, err := UpdateStoreCAS(store, 0, func(*Config) error { return nil }); err == nil {
		t.Fatal("stale revision was accepted")
	}
	stored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 1 || stored.Node.DisplayName != "first" {
		t.Fatalf("stored config=%+v", stored)
	}
	backups, err := store.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count=%d, want 1", len(backups))
	}
	if err := SaveStoreLastKnownGood(store, stored); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadStoreLastKnownGood(store)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Revision != stored.Revision || checkpoint.Node.DisplayName != stored.Node.DisplayName {
		t.Fatalf("checkpoint=%+v stored=%+v", checkpoint, stored)
	}
}

func TestObjectStoreRestoresOnlyInvalidActiveConfig(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Revision = 4
	cfg.Node.DisplayName = "checkpoint"
	if err := SaveStore(store, cfg); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStoreLastKnownGood(store, 4, false); err == nil {
		t.Fatal("valid active config was overwritten")
	}
	if err := store.ReplaceConfig([]byte("{invalid")); err != nil {
		t.Fatal(err)
	}
	result, err := RestoreStoreLastKnownGood(store, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 4 || result.AfterRevision != 5 {
		t.Fatalf("restore result=%+v", result)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 5 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
}

func TestObjectStoreRejectsInvalidCheckpointContent(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	if err := store.Replace(statestore.LastKnownGood, []byte(`{"future":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStoreLastKnownGood(store); err == nil || !IsContentError(err) {
		t.Fatalf("invalid checkpoint error=%v", err)
	}
}

func TestObjectStoreRequiresStore(t *testing.T) {
	if _, err := LoadStoreExisting(nil); err == nil {
		t.Fatal("nil store accepted")
	}
	if err := SaveStore(nil, DefaultConfig()); err == nil {
		t.Fatal("nil store save accepted")
	}
	if _, err := UpdateStoreCAS(nil, 0, func(*Config) error { return nil }); err == nil {
		t.Fatal("nil store CAS accepted")
	}
	if _, _, err := LoadStoreOrMigrate(nil, true); err == nil {
		t.Fatal("nil store migration accepted")
	}
}

func openConfigObjectStore(t *testing.T) *statestore.Store {
	t.Helper()
	store, err := statestore.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

package configstore

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestObjectStoreMigratesExplicitV1ToV2ExactlyOnceWithoutGrants(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	v1 := []byte(`{"schema_version":1,"revision":7,"node":{},"system":{}}`)
	if err := store.CreateConfigExclusive(v1); err != nil {
		t.Fatal(err)
	}

	cfg, migrated, err := LoadStoreOrMigrate(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || cfg.SchemaVersion != CurrentSchemaVersion || cfg.Revision != 8 {
		t.Fatalf("first migration = %+v migrated=%v", cfg, migrated)
	}
	if cfg.NodeEgressGrants == nil || len(cfg.NodeEgressGrants) != 0 {
		t.Fatalf("v1 migration forged grants: %#v", cfg.NodeEgressGrants)
	}

	second, migratedAgain, err := LoadStoreOrMigrate(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if migratedAgain || second.SchemaVersion != CurrentSchemaVersion || second.Revision != 8 {
		t.Fatalf("second load = %+v migrated=%v", second, migratedAgain)
	}
	if second.NodeEgressGrants == nil || len(second.NodeEgressGrants) != 0 {
		t.Fatalf("second load forged grants: %#v", second.NodeEgressGrants)
	}
	backups, err := store.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count=%d, want exactly one migration write", len(backups))
	}
}

func TestObjectStoreV1MigrationRejectsUnknownFieldsWithoutWriting(t *testing.T) {
	fixtures := map[string][]byte{
		"top level": []byte(`{"schema_version":1,"revision":7,"node":{},"system":{},"future":true}`),
		"nested":    []byte(`{"schema_version":1,"revision":7,"node":{"future":true},"system":{}}`),
		"v2 grant":  []byte(`{"schema_version":1,"revision":7,"node":{},"system":{},"node_egress_grants":{}}`),
	}
	for name, payload := range fixtures {
		t.Run(name, func(t *testing.T) {
			store := openConfigObjectStore(t)
			defer store.Close()
			if err := store.CreateConfigExclusive(payload); err != nil {
				t.Fatal(err)
			}

			if _, migrated, err := LoadStoreOrMigrate(store, false); err == nil || migrated || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("migration result migrated=%v err=%v", migrated, err)
			}
			assertObjectStoreConfigUnchanged(t, store, payload)
		})
	}
}

func TestObjectStoreV1MigrationRejectsRevisionExhaustionWithoutWriting(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	payload := []byte(fmt.Sprintf(
		`{"schema_version":1,"revision":%d,"node":{},"system":{}}`,
		int64(math.MaxInt64),
	))
	if err := store.CreateConfigExclusive(payload); err != nil {
		t.Fatal(err)
	}

	if _, migrated, err := LoadStoreOrMigrate(store, false); err == nil || migrated || !strings.Contains(err.Error(), "config.revision_exhausted") {
		t.Fatalf("migration result migrated=%v err=%v", migrated, err)
	}
	assertObjectStoreConfigUnchanged(t, store, payload)
}

func TestObjectStoreConcurrentV1MigrationPublishesOneRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := statestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	v1 := []byte(`{"schema_version":1,"revision":41,"node":{},"system":{}}`)
	if err := first.CreateConfigExclusive(v1); err != nil {
		t.Fatal(err)
	}

	type migrationResult struct {
		cfg      Config
		migrated bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan migrationResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*statestore.Store{first, second} {
		go func(store *statestore.Store) {
			ready.Done()
			<-start
			cfg, migrated, err := LoadStoreOrMigrate(store, false)
			results <- migrationResult{cfg: cfg, migrated: migrated, err: err}
		}(store)
	}
	ready.Wait()
	close(start)

	migrationWinners := 0
	successes := 0
	lockRejections := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			if !strings.Contains(result.err.Error(), "config.locked") {
				t.Fatal(result.err)
			}
			lockRejections++
			continue
		}
		successes++
		if result.migrated {
			migrationWinners++
		}
		if result.cfg.SchemaVersion != CurrentSchemaVersion || result.cfg.Revision != 42 {
			t.Fatalf("concurrent migration result = %+v migrated=%v", result.cfg, result.migrated)
		}
		if len(result.cfg.NodeEgressGrants) != 0 {
			t.Fatalf("concurrent migration forged grants: %#v", result.cfg.NodeEgressGrants)
		}
	}
	if migrationWinners != 1 {
		t.Fatalf("migration winners=%d, want exactly one", migrationWinners)
	}
	if successes+lockRejections != 2 || successes == 0 {
		t.Fatalf("successes=%d lock rejections=%d", successes, lockRejections)
	}
	stored, err := LoadStoreExisting(first)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SchemaVersion != CurrentSchemaVersion || stored.Revision != 42 {
		t.Fatalf("stored config after concurrent migration = %+v", stored)
	}
	backups, err := first.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count=%d, want one committed migration", len(backups))
	}
}

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

func assertObjectStoreConfigUnchanged(t *testing.T, store *statestore.Store, want []byte) {
	t.Helper()
	got, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("rejected config was rewritten: before=%s after=%s", want, got)
	}
	backups, err := store.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("rejected config created backups: %v", backups)
	}
}

package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"reflect"
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
	if len(backups) != 0 {
		t.Fatalf("legacy migration created authoritative backups: %v", backups)
	}
	archived, err := store.Read(statestore.PreMigrationConfig, maxConfigFileBytes)
	if err != nil || !bytes.Equal(archived, v1) {
		t.Fatalf("pre-migration archive=%s error=%v", archived, err)
	}
}

func TestObjectStoreCredentialCollisionQuarantineFailsClosed(t *testing.T) {
	for _, schemaVersion := range []int{1, CurrentSchemaVersion} {
		name := fmt.Sprintf("schema-%d", schemaVersion)
		t.Run(name, func(t *testing.T) {
			store := openConfigObjectStore(t)
			defer store.Close()

			cfg := peerCredentialCollisionMigrationConfig()
			cfg.SchemaVersion = schemaVersion
			if schemaVersion == CurrentSchemaVersion {
				cfg.NodeEgressGrants["node-c"] = NodeEgressGrant{
					SourceNodeID: "node-c", Network: "tcp", AllowCIDRs: []string{"8.0.0.0/8"},
					AllowPorts: []EgressPortRange{{From: 443, To: 443}},
				}
			}
			payload, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateConfigExclusive(append(payload, '\n')); err != nil {
				t.Fatal(err)
			}
			if schemaVersion == CurrentSchemaVersion {
				if _, err := LoadStoreExisting(store); err == nil || !IsContentError(err) ||
					!strings.Contains(err.Error(), "config.peer_credential_duplicate") {
					t.Fatalf("ordinary object read accepted repair-only collision: %v", err)
				}
			}

			quarantined, changed, err := LoadStoreOrMigrate(store, false)
			if err != nil {
				t.Fatalf("LoadStoreOrMigrate: %v", err)
			}
			if !changed || quarantined.SchemaVersion != CurrentSchemaVersion || quarantined.Revision != 8 {
				t.Fatalf("quarantine metadata = changed:%v config:%+v", changed, quarantined)
			}
			for _, peerName := range []string{"alpha", "zeta"} {
				peer, _, found := FindPeer(quarantined.Peers, peerName)
				if !found || peer.Enabled || peer.XrayProfileID != "shared" || !IsPeerCredentialQuarantined(peer) {
					t.Fatalf("credential collision peer %q was not quarantined: %+v", peerName, quarantined.Peers)
				}
			}
			safe, _, found := FindPeer(quarantined.Peers, "D")
			if !found || !safe.Enabled || safe.XrayProfileID != "safe" || safe.DisabledCause != "" {
				t.Fatalf("unrelated peer changed during quarantine: %+v", safe)
			}
			if len(quarantined.NodeInbound) != 1 || quarantined.NodeInbound[0].Enabled || quarantined.NodeInbound[0].ExitPeer != "zeta" ||
				!strings.Contains(quarantined.NodeInbound[0].DisabledCause, "exit peer credential was quarantined") {
				t.Fatalf("unsafe exit dependency was not disabled: %+v", quarantined.NodeInbound)
			}
			wantGrants := 0
			if schemaVersion == CurrentSchemaVersion {
				wantGrants = 1
			}
			if len(quarantined.NodeEgressGrants) != wantGrants || len(quarantined.PeerTrust) != 2 {
				t.Fatalf("quarantine destroyed repair evidence: grants=%+v trust=%+v", quarantined.NodeEgressGrants, quarantined.PeerTrust)
			}
			assertCredentialQuarantineRecord(t, quarantined, "shared", []string{"node-b", "node-c"})
			if err := Validate(quarantined); err != nil {
				t.Fatalf("quarantined config is not runtime-valid: %v", err)
			}

			persisted, err := LoadStoreExisting(store)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(persisted, quarantined) {
				t.Fatalf("persisted quarantine differs\ngot:  %+v\nwant: %+v", persisted, quarantined)
			}
			second, changedAgain, err := LoadStoreOrMigrate(store, false)
			if err != nil {
				t.Fatal(err)
			}
			if changedAgain || !reflect.DeepEqual(second, quarantined) {
				t.Fatalf("quarantine repeated = changed:%v config:%+v", changedAgain, second)
			}
			backups, err := store.BackupNames()
			if err != nil {
				t.Fatal(err)
			}
			wantBackups := 1
			if schemaVersion == 1 {
				wantBackups = 0
			}
			if len(backups) != wantBackups {
				t.Fatalf("backup count=%d, want %d", len(backups), wantBackups)
			}
			if schemaVersion == 1 {
				archived, err := store.Read(statestore.PreMigrationConfig, maxConfigFileBytes)
				if err != nil || !bytes.Equal(archived, append(payload, '\n')) {
					t.Fatalf("pre-migration archive=%s error=%v", archived, err)
				}
			}
		})
	}
}

func TestObjectStoreCompletesPartialCredentialQuarantineBeforeRuntime(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	cfg := peerCredentialCollisionConfig()
	cfg.Revision = 9
	cfg.Peers[1].Enabled = false
	cfg.Peers[1].DisabledCause = PeerCredentialQuarantineCause
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateConfigExclusive(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}

	repaired, changed, err := LoadStoreOrMigrate(store, false)
	if err != nil {
		t.Fatalf("LoadStoreOrMigrate: %v", err)
	}
	if !changed || repaired.Revision != 10 {
		t.Fatalf("partial quarantine metadata = changed:%v config:%+v", changed, repaired)
	}
	for _, name := range []string{"B", "C"} {
		peer, _, found := FindPeer(repaired.Peers, name)
		if !found || peer.Enabled || !IsPeerCredentialQuarantined(peer) {
			t.Fatalf("partial quarantine peer %q was not contained: %+v", name, peer)
		}
	}
	assertCredentialQuarantineRecord(t, repaired, "shared", []string{"node-b", "node-c"})
	if err := Validate(repaired); err != nil {
		t.Fatalf("repaired partial quarantine crossed runtime invalid: %v", err)
	}
}

func TestObjectStoreCurrentCredentialCollisionAtMaxRevisionIsNotRewritten(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	cfg := peerCredentialCollisionConfig()
	cfg.Revision = math.MaxInt64
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := store.CreateConfigExclusive(payload); err != nil {
		t.Fatal(err)
	}

	if _, changed, err := LoadStoreOrMigrate(store, false); err == nil || changed || !strings.Contains(err.Error(), "config.revision_exhausted") {
		t.Fatalf("max-revision quarantine = changed:%v error:%v", changed, err)
	}
	assertObjectStoreConfigUnchanged(t, store, payload)
}

func TestObjectStoreConcurrentCurrentCredentialQuarantinePublishesOnce(t *testing.T) {
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
	cfg := peerCredentialCollisionConfig()
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CreateConfigExclusive(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}

	type result struct {
		cfg      Config
		migrated bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*statestore.Store{first, second} {
		go func(store *statestore.Store) {
			ready.Done()
			<-start
			loaded, migrated, loadErr := LoadStoreOrMigrate(store, false)
			results <- result{cfg: loaded, migrated: migrated, err: loadErr}
		}(store)
	}
	ready.Wait()
	close(start)

	winners := 0
	successes := 0
	lockRejections := 0
	for range 2 {
		got := <-results
		if got.err != nil {
			if !strings.Contains(got.err.Error(), "config.locked") {
				t.Fatal(got.err)
			}
			lockRejections++
			continue
		}
		successes++
		if got.migrated {
			winners++
		}
		if got.cfg.Revision != 1 || Validate(got.cfg) != nil {
			t.Fatalf("concurrent quarantine result=%+v migrated=%v", got.cfg, got.migrated)
		}
	}
	if winners != 1 || successes == 0 || successes+lockRejections != 2 {
		t.Fatalf("winners=%d successes=%d lock_rejections=%d", winners, successes, lockRejections)
	}
	persisted, err := LoadStoreExisting(first)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != 1 || Validate(persisted) != nil {
		t.Fatalf("persisted concurrent quarantine=%+v", persisted)
	}
	backups, err := first.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup count=%d, want one committed quarantine", len(backups))
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
	if len(backups) != 0 {
		t.Fatalf("concurrent legacy migration created authoritative backups: %v", backups)
	}
	archived, err := first.Read(statestore.PreMigrationConfig, maxConfigFileBytes)
	if err != nil || !bytes.Equal(archived, v1) {
		t.Fatalf("concurrent pre-migration archive=%s error=%v", archived, err)
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
	reserved, exists, err := loadStoreConfigRevisionHighWater(store)
	if err != nil || !exists || reserved != result.AfterRevision {
		t.Fatalf("restore reservation=%d exists=%v error=%v", reserved, exists, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 5 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
	rejected, err := store.Read(statestore.RejectedConfig, maxConfigFileBytes)
	if err != nil || !bytes.Equal(rejected, []byte("{invalid")) {
		t.Fatalf("rejected active archive=%q error=%v", rejected, err)
	}
	backups, err := store.BackupNames()
	if err != nil || len(backups) != 0 {
		t.Fatalf("restore retained invalid active as revision history: backups=%v error=%v", backups, err)
	}
	if err := store.ReplaceConfig([]byte("{\n")); err != nil {
		t.Fatal(err)
	}
	secondDryRun, err := RestoreStoreLastKnownGood(store, cfg.Revision, true)
	if err != nil || secondDryRun.AfterRevision != 6 {
		t.Fatalf("durable reservation did not advance repeated restore: result=%+v error=%v", secondDryRun, err)
	}
}

func TestObjectStoreRestoreTreatsCredentialCollisionAsInvalidActive(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	collision := peerCredentialCollisionConfig()
	collision.Revision = 100
	payload, err := json.MarshalIndent(collision, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, true); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("checkpoint revision bypassed active CAS: %v", err)
	}
	dryRun, err := RestoreStoreLastKnownGood(store, collision.Revision, true)
	if err != nil {
		t.Fatalf("dry-run rejected repairable active collision: %v", err)
	}
	result, err := RestoreStoreLastKnownGood(store, collision.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != dryRun || result.BeforeRevision != 100 || result.AfterRevision != 101 {
		t.Fatalf("restore=%+v dry-run=%+v", result, dryRun)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(restored); err != nil || restored.Revision != 101 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v error=%v", restored, err)
	}
}

func TestObjectStoreRestoreRefusesNewerSchemaWithoutSideEffects(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	active := []byte(`{"schema_version":3,"revision":100,"node":{},"system":{},"future_extension":true}`)
	if err := store.ReplaceConfig(active); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStoreLastKnownGood(store, 100, false); err == nil ||
		!strings.Contains(err.Error(), "config.restore_schema_newer") {
		t.Fatalf("newer schema restore error=%v", err)
	}
	after, err := store.ReadConfig(maxConfigFileBytes)
	if err != nil || !bytes.Equal(after, active) {
		t.Fatalf("newer schema active=%q error=%v", after, err)
	}
	if _, err := store.Read(statestore.RejectedConfig, maxConfigFileBytes); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refused restore created rejected archive: %v", err)
	}
	if _, exists, err := loadStoreConfigRevisionHighWater(store); err != nil || exists {
		t.Fatalf("refused restore reserved revision: exists=%v error=%v", exists, err)
	}
}

func TestObjectStoreRestorePreservesStructurallyReadableActiveRevision(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	active := []byte(`{"schema_version":2,"revision":100,"node":{},"system":{},"future_extension":true}`)
	if err := store.ReplaceConfig(active); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStoreLastKnownGood(store, 7, true); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("checkpoint revision bypassed readable active revision: %v", err)
	}
	dryRun, err := RestoreStoreLastKnownGood(store, 100, true)
	if err != nil || dryRun.BeforeRevision != 100 || dryRun.AfterRevision != 101 {
		t.Fatalf("dry-run=%+v error=%v", dryRun, err)
	}
	result, err := RestoreStoreLastKnownGood(store, 100, false)
	if err != nil || result != dryRun {
		t.Fatalf("restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 101 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
}

func TestObjectStoreRestoreUsesBackupsToSkipUnreadableActiveRevision(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{40, 41} {
		active := checkpoint
		active.Revision = revision
		active.Node.DisplayName = "active"
		if err := SaveStore(store, active); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceConfig([]byte(`{"revision":41`)); err != nil {
		t.Fatal(err)
	}

	dryRun, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, true)
	if err != nil || dryRun.BeforeRevision != checkpoint.Revision || dryRun.AfterRevision != 42 {
		t.Fatalf("dry-run=%+v error=%v", dryRun, err)
	}
	result, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, false)
	if err != nil || result != dryRun {
		t.Fatalf("restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 42 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
}

func TestObjectStoreRestoreCarriesForwardDurableCredentialQuarantine(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	checkpoint := peerCredentialCollisionConfig()
	checkpoint.Revision = 7
	checkpoint.Peers = []PeerConfig{checkpoint.Peers[0], checkpoint.Peers[2]}
	delete(checkpoint.PeerTrust, "C")
	delete(checkpoint.NodeEgressGrants, "node-c")
	if err := SaveStore(store, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveStoreLastKnownGood(store, checkpoint); err != nil {
		t.Fatal(err)
	}

	active := peerCredentialCollisionConfig()
	active.Revision = 8
	if changed, err := quarantinePeerCredentialCollisions(&active); err != nil || !changed {
		t.Fatalf("prepare quarantine changed=%v error=%v", changed, err)
	}
	active.Peers = []PeerConfig{active.Peers[0], active.Peers[2]}
	delete(active.PeerTrust, "C")
	delete(active.NodeEgressGrants, "node-c")
	if err := SaveStore(store, active); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig([]byte(`{"revision":8`)); err != nil {
		t.Fatal(err)
	}

	result, err := RestoreStoreLastKnownGood(store, checkpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 7 || result.AfterRevision != 9 {
		t.Fatalf("restore result=%+v", result)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, found := FindPeer(restored.Peers, "B")
	if !found || peer.Enabled || !IsPeerCredentialQuarantined(peer) {
		t.Fatalf("restored peer escaped quarantine: %+v", restored.Peers)
	}
	assertCredentialQuarantineRecord(t, restored, "shared", []string{"node-b", "node-c"})
}

func TestObjectStoreCredentialCollisionCheckpointIsQuarantinedForRecovery(t *testing.T) {
	store := openConfigObjectStore(t)
	defer store.Close()
	if err := store.CreateConfigExclusive([]byte("{invalid")); err != nil {
		t.Fatal(err)
	}
	collision := peerCredentialCollisionConfig()
	collision.Revision = 7
	payload, err := json.MarshalIndent(collision, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkpointPayload := append(payload, '\n')
	if err := store.Replace(statestore.LastKnownGood, checkpointPayload); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadStoreLastKnownGood(store)
	if err != nil {
		t.Fatalf("repairable checkpoint load: %v", err)
	}
	if err := Validate(checkpoint); err != nil || checkpoint.Revision != 7 {
		t.Fatalf("quarantined checkpoint=%+v error=%v", checkpoint, err)
	}
	if _, err := persistStorePeerCredentialLedger(store, checkpoint.PeerCredentialQuarantines); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"B", "C"} {
		peer, _, found := FindPeer(checkpoint.Peers, name)
		if !found || peer.Enabled || peer.XrayProfileID != "shared" || !IsPeerCredentialQuarantined(peer) {
			t.Fatalf("checkpoint peer %q was not quarantined: %+v", name, checkpoint.Peers)
		}
	}
	dryRun, err := RestoreStoreLastKnownGood(store, 7, true)
	if err != nil || dryRun.BeforeRevision != 7 || dryRun.AfterRevision != 8 {
		t.Fatalf("checkpoint dry-run=%+v error=%v", dryRun, err)
	}
	result, err := RestoreStoreLastKnownGood(store, 7, false)
	if err != nil || result != dryRun {
		t.Fatalf("checkpoint restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(restored); err != nil || restored.Revision != 8 {
		t.Fatalf("restored active config=%+v error=%v", restored, err)
	}
	afterCheckpoint, err := store.Read(statestore.LastKnownGood, maxConfigFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterCheckpoint, checkpointPayload) {
		t.Fatal("in-memory checkpoint quarantine rewrote checkpoint")
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

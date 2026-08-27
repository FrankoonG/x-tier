package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLastKnownGoodRoundTripUsesPrivateAtomicFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := DefaultConfig()
	cfg.Revision = 7
	cfg.Node.NodeID = testLegacyNodeID
	cfg.Node.DisplayName = "checkpoint"
	if err := SaveLastKnownGood(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("last-known-good = %+v, want %+v", got, cfg)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(LastKnownGoodPath(configPath))
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("last-known-good mode = %o, want 600", gotMode)
		}
	}
}

func TestLoadLastKnownGoodDoesNotInventMissingCheckpoint(t *testing.T) {
	_, err := LoadLastKnownGood(filepath.Join(t.TempDir(), "xtier.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing checkpoint error = %v, want fs.ErrNotExist", err)
	}
}

func TestRestorePinnedLastKnownGoodRepairsOnlyInvalidActiveConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}

	invalid := []byte(`{"schema_version":1,"revision":8,"node":{},"system":{},"node_inbound":[{"kind":"socks"},{"kind":"socks"}]}`)
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, 8, true)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.BeforeRevision != 8 || dryRun.AfterRevision != 9 {
		t.Fatalf("dry-run result = %+v", dryRun)
	}
	if active, err := os.ReadFile(configPath); err != nil || !bytes.Equal(active, invalid) {
		t.Fatalf("dry run changed invalid active config: %q err=%v", active, err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 6, false); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("stale checkpoint revision error = %v", err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 8 || result.AfterRevision != 9 {
		t.Fatalf("restore result = %+v", result)
	}
	reserved, exists, err := loadPathConfigRevisionHighWater(configPath)
	if err != nil || !exists || reserved != result.AfterRevision {
		t.Fatalf("restore reservation=%d exists=%v error=%v", reserved, exists, err)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 9 || restored.Node.DisplayName != checkpoint.Node.DisplayName {
		t.Fatalf("restored config = %+v", restored)
	}
	rejected, err := readSecureFile(configPath + rejectedConfigSuffix)
	if err != nil || !bytes.Equal(rejected, invalid) {
		t.Fatalf("rejected active archive=%q error=%v", rejected, err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 7, false); err == nil || !strings.Contains(err.Error(), "config.restore_not_required") {
		t.Fatalf("valid active config was eligible for restore: %v", err)
	}
	backups, err := filepath.Glob(configPath + ".bak.*")
	if err != nil || len(backups) != 0 {
		t.Fatalf("restore retained invalid active as revision history: backups=%v error=%v", backups, err)
	}
	if err := os.WriteFile(configPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondDryRun, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, true)
	if err != nil || secondDryRun.AfterRevision != 10 {
		t.Fatalf("durable reservation did not advance repeated restore: result=%+v error=%v", secondDryRun, err)
	}
}

func TestRestorePinnedLastKnownGoodIgnoresForeignBackupNames(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 4
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath+".bak.old", []byte("not a managed backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := []byte(`{"schema_version":2,"revision":5,"node":{},"system":{},"node_inbound":[{"kind":"socks"},{"kind":"socks"}]}`)
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, 5, true)
	if err != nil || dryRun.BeforeRevision != 5 || dryRun.AfterRevision != 6 {
		t.Fatalf("foreign backup affected restore: result=%+v error=%v", dryRun, err)
	}
}

func TestRestorePinnedLastKnownGoodTreatsCredentialCollisionAsInvalidActive(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}

	collision := peerCredentialCollisionConfig()
	collision.Revision = 100
	payload, err := json.MarshalIndent(collision, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExisting(configPath); err == nil || !IsContentError(err) || !strings.Contains(err.Error(), "config.peer_credential_duplicate") {
		t.Fatalf("ordinary active read accepted repair-only collision: %v", err)
	}

	if _, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, true); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("checkpoint revision bypassed active CAS: %v", err)
	}
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, collision.Revision, true)
	if err != nil {
		t.Fatalf("dry-run rejected repairable active collision: %v", err)
	}
	if dryRun.BeforeRevision != 100 || dryRun.AfterRevision != 101 {
		t.Fatalf("dry-run result = %+v", dryRun)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, collision.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if result != dryRun {
		t.Fatalf("restore result=%+v dry-run=%+v", result, dryRun)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(restored); err != nil || restored.Revision != 101 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v error=%v", restored, err)
	}
}

func TestRestorePinnedLastKnownGoodRefusesNewerSchemaWithoutSideEffects(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	active := []byte(`{"schema_version":3,"revision":100,"node":{},"system":{},"future_extension":true}`)
	if err := os.WriteFile(configPath, active, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 100, false); err == nil ||
		!strings.Contains(err.Error(), "config.restore_schema_newer") {
		t.Fatalf("newer schema restore error=%v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(after, active) {
		t.Fatalf("newer schema active=%q error=%v", after, err)
	}
	if _, err := os.Stat(configPath + rejectedConfigSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refused restore created rejected archive: %v", err)
	}
	if _, exists, err := loadPathConfigRevisionHighWater(configPath); err != nil || exists {
		t.Fatalf("refused restore reserved revision: exists=%v error=%v", exists, err)
	}
}

func TestRestorePinnedLastKnownGoodPreservesStructurallyReadableActiveRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	active := []byte(`{"schema_version":2,"revision":100,"node":{},"system":{},"future_extension":true}`)
	if err := os.WriteFile(configPath, active, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 7, true); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("checkpoint revision bypassed readable active revision: %v", err)
	}
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, 100, true)
	if err != nil || dryRun.BeforeRevision != 100 || dryRun.AfterRevision != 101 {
		t.Fatalf("dry-run=%+v error=%v", dryRun, err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, 100, false)
	if err != nil || result != dryRun {
		t.Fatalf("restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 101 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
}

func TestRestorePinnedLastKnownGoodUsesBackupsToSkipUnreadableActiveRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := DefaultConfig()
	checkpoint.Revision = 7
	checkpoint.Node.DisplayName = "checkpoint"
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{40, 41} {
		active := checkpoint
		active.Revision = revision
		active.Node.DisplayName = "active"
		if err := Save(configPath, active); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(configPath, []byte(`{"revision":41`), 0o600); err != nil {
		t.Fatal(err)
	}

	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, true)
	if err != nil || dryRun.BeforeRevision != checkpoint.Revision || dryRun.AfterRevision != 42 {
		t.Fatalf("dry-run=%+v error=%v", dryRun, err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false)
	if err != nil || result != dryRun {
		t.Fatalf("restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 42 || restored.Node.DisplayName != "checkpoint" {
		t.Fatalf("restored config=%+v", restored)
	}
}

func TestRestorePinnedLastKnownGoodCarriesForwardDurableCredentialQuarantine(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := peerCredentialCollisionConfig()
	checkpoint.Revision = 7
	checkpoint.Peers = []PeerConfig{checkpoint.Peers[0], checkpoint.Peers[2]}
	delete(checkpoint.PeerTrust, "C")
	delete(checkpoint.NodeEgressGrants, "node-c")
	if err := Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := SaveLastKnownGood(configPath, checkpoint); err != nil {
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
	if err := Save(configPath, active); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"revision":8`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 7 || result.AfterRevision != 9 {
		t.Fatalf("restore result=%+v", result)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, found := FindPeer(restored.Peers, "B")
	if !found || peer.Enabled || !IsPeerCredentialQuarantined(peer) {
		t.Fatalf("restored peer escaped quarantine: %+v", restored.Peers)
	}
	assertCredentialQuarantineRecord(t, restored, "shared", []string{"node-b", "node-c"})
}

func TestRecoverableDocumentRevisionRejectsAmbiguousOrIncompleteJSON(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    int64
		known   bool
	}{
		{"valid", `{"revision":100,"future":true}`, 100, true},
		{"missing", `{"future":true}`, 0, false},
		{"null", `{"revision":null}`, 0, false},
		{"duplicate", `{"revision":7,"revision":100}`, 0, false},
		{"fractional", `{"revision":1.5}`, 0, false},
		{"negative", `{"revision":-1}`, 0, false},
		{"truncated", `{"revision":100`, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, known := recoverableDocumentRevision([]byte(test.payload), nil)
			if got != test.want || known != test.known {
				t.Fatalf("revision=%d known=%v, want %d/%v", got, known, test.want, test.known)
			}
		})
	}
}

func TestLastKnownGoodRestoreRevisionEvidenceModel(t *testing.T) {
	tests := []struct {
		name        string
		active      int64
		activeKnown bool
		checkpoint  int64
		backup      int64
		reserved    int64
		want        int64
		wantError   bool
	}{
		{"known active ahead", 41, true, 40, 40, -1, 42, false},
		{"backup equals active", 40, true, 7, 40, -1, 42, false},
		{"regressed active", 8, true, 7, 9, -1, 11, false},
		{"unknown without backup", 0, false, 4, -1, -1, 5, false},
		{"unknown with backup", 0, false, 7, 40, -1, 42, false},
		{"unknown checkpoint ahead", 0, false, 41, 40, -1, 42, false},
		{"known checkpoint ahead", 8, true, 10, 9, -1, 11, false},
		{"durable reservation ahead", 8, true, 7, 9, 42, 43, false},
		{"last revision remains publishable", 0, false, math.MaxInt64 - 1, -1, -1, math.MaxInt64, false},
		{"backup reserves last revision", 0, false, 7, math.MaxInt64 - 1, -1, 0, true},
		{"reservation exhausted", 7, true, 7, -1, math.MaxInt64, 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := test.checkpoint
			wantBefore := test.checkpoint
			if test.activeKnown {
				expected = test.active
				wantBefore = test.active
			}
			before, after, err := lastKnownGoodRestoreRevisions(
				test.active,
				test.activeKnown,
				test.backup,
				test.reserved,
				Config{Revision: test.checkpoint},
				expected,
			)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "config.revision_exhausted") {
					t.Fatalf("result=%d/%d error=%v, want exhaustion", before, after, err)
				}
				return
			}
			if err != nil || before != wantBefore || after != test.want {
				t.Fatalf("result=%d/%d error=%v, want %d/%d", before, after, err, wantBefore, test.want)
			}
		})
	}
}

func TestLastKnownGoodCredentialCollisionIsQuarantinedForRecovery(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := Save(configPath, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	invalidActive := []byte("{\n")
	if err := os.WriteFile(configPath, invalidActive, 0o600); err != nil {
		t.Fatal(err)
	}

	collision := peerCredentialCollisionConfig()
	collision.Revision = 7
	payload, err := json.MarshalIndent(collision, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkpointPayload := append(payload, '\n')
	if err := writeFileAtomic(LastKnownGoodPath(configPath), checkpointPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := LoadLastKnownGood(configPath)
	if err != nil {
		t.Fatalf("repairable checkpoint load: %v", err)
	}
	if err := Validate(checkpoint); err != nil || checkpoint.Revision != 7 {
		t.Fatalf("quarantined checkpoint=%+v error=%v", checkpoint, err)
	}
	for _, name := range []string{"B", "C"} {
		peer, _, found := FindPeer(checkpoint.Peers, name)
		if !found || peer.Enabled || peer.XrayProfileID != "shared" || !IsPeerCredentialQuarantined(peer) {
			t.Fatalf("checkpoint peer %q was not quarantined: %+v", name, checkpoint.Peers)
		}
	}
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, 7, true)
	if err != nil || dryRun.BeforeRevision != 7 || dryRun.AfterRevision != 8 {
		t.Fatalf("checkpoint dry-run=%+v error=%v", dryRun, err)
	}
	if active, err := os.ReadFile(configPath); err != nil || !bytes.Equal(active, invalidActive) {
		t.Fatalf("dry-run changed active config: %q error=%v", active, err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, 7, false)
	if err != nil || result != dryRun {
		t.Fatalf("checkpoint restore=%+v dry-run=%+v error=%v", result, dryRun, err)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(restored); err != nil || restored.Revision != 8 {
		t.Fatalf("restored active config=%+v error=%v", restored, err)
	}
	afterCheckpoint, err := os.ReadFile(LastKnownGoodPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterCheckpoint, checkpointPayload) {
		t.Fatalf("in-memory checkpoint quarantine rewrote checkpoint")
	}
}

func TestSaveLastKnownGoodRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "xtier.json")
	if err := os.MkdirAll(filepath.Dir(LastKnownGoodPath(configPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, LastKnownGoodPath(configPath)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveLastKnownGood(configPath, DefaultConfig()); err == nil {
		t.Fatal("last-known-good writer accepted a symlink target")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("symlink target changed: %q", contents)
	}
}

func TestLoadLastKnownGoodRejectsNewerSchemaWithoutRewriting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	payload := []byte(`{"schema_version":2,"revision":9,"node":{},"system":{},"future":true}`)
	if err := os.MkdirAll(filepath.Dir(LastKnownGoodPath(configPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(LastKnownGoodPath(configPath), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLastKnownGood(configPath); err == nil || !strings.Contains(err.Error(), "config.schema_newer") {
		t.Fatalf("newer checkpoint error = %v", err)
	}
	after, err := os.ReadFile(LastKnownGoodPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, payload) {
		t.Fatalf("newer checkpoint was rewritten: before=%s after=%s", payload, after)
	}
}

func TestLoadLastKnownGoodRejectsUnknownUnversionedFieldWithoutRewriting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	payload := []byte(`{"revision":9,"node":{},"system":{},"future_extension":true}`)
	if err := os.MkdirAll(filepath.Dir(LastKnownGoodPath(configPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(LastKnownGoodPath(configPath), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLastKnownGood(configPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown checkpoint error = %v", err)
	}
	after, err := os.ReadFile(LastKnownGoodPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, payload) {
		t.Fatalf("unknown checkpoint was rewritten: before=%s after=%s", payload, after)
	}
}

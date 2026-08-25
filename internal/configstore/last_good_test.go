package configstore

import (
	"bytes"
	"errors"
	"io/fs"
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
	dryRun, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.BeforeRevision != 7 || dryRun.AfterRevision != 8 {
		t.Fatalf("dry-run result = %+v", dryRun)
	}
	if active, err := os.ReadFile(configPath); err != nil || !bytes.Equal(active, invalid) {
		t.Fatalf("dry run changed invalid active config: %q err=%v", active, err)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 6, false); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("stale checkpoint revision error = %v", err)
	}
	result, err := RestorePinnedLastKnownGood(configPath, configPath, checkpoint.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 7 || result.AfterRevision != 8 {
		t.Fatalf("restore result = %+v", result)
	}
	restored, err := LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 8 || restored.Node.DisplayName != checkpoint.Node.DisplayName {
		t.Fatalf("restored config = %+v", restored)
	}
	if _, err := RestorePinnedLastKnownGood(configPath, configPath, 7, false); err == nil || !strings.Contains(err.Error(), "config.restore_not_required") {
		t.Fatalf("valid active config was eligible for restore: %v", err)
	}
}

func TestSaveLastKnownGoodRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "xtier.json")
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

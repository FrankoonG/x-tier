package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/xtls/xray-core/common/uuid"
)

const testLegacyNodeID = "node-0123456789abcdef0123456789abcdef"

func TestLoadExistingRejectsMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, err := LoadExisting(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadExisting error = %v, want os.ErrNotExist", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 0 || cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("Load default = %+v", cfg)
	}
}

func TestLoadExistingClassifiesContentFailuresWithoutClassifyingIO(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := LoadExisting(missing); err == nil || IsContentError(err) {
		t.Fatalf("missing config error = %v, want non-content I/O error", err)
	}

	root := t.TempDir()
	malformed := filepath.Join(root, "malformed.json")
	if err := Save(malformed, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(malformed, []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExisting(malformed); err == nil || !IsContentError(err) {
		t.Fatalf("malformed config error = %v, want content classification", err)
	}

	invalid := DefaultConfig()
	invalid.NodeInbound = []InboundConfig{
		{Kind: "socks", Listen: "127.0.0.1:1080"},
		{Kind: "socks", Listen: "127.0.0.1:2080"},
	}
	payload, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(root, "invalid.json")
	if err := Save(invalidPath, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExisting(invalidPath); err == nil || !IsContentError(err) || !strings.Contains(err.Error(), "config.inbound_duplicate") {
		t.Fatalf("invalid config error = %v, want classified duplicate inbound", err)
	}
}

func TestSaveLoadRevisionAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Revision = 1
	cfg.Node.NodeID = testLegacyNodeID
	cfg.Node.DisplayName = "alpha"
	cfg.Node.Role = "thin"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg.Revision = 2
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save second: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Revision != 2 {
		t.Fatalf("revision = %d", got.Revision)
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected backup file")
	}
	if _, err := readSecureFile(matches[0]); err != nil {
		t.Fatalf("backup security: %v", err)
	}
}

func TestSaveRetainsBoundedBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	for revision := int64(0); revision < maxConfigBackups+4; revision++ {
		cfg.Revision = revision
		if err := Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != maxConfigBackups {
		t.Fatalf("backup count=%d, want %d", len(matches), maxConfigBackups)
	}
}

func TestWithLockIgnoresStaleLockFileAndRejectsLiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatalf("create stale lock fixture: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	if err := WithLock(path, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "config.locked") {
		t.Fatalf("concurrent WithLock error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
}

func TestWithLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	target := filepath.Join(dir, "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WithLock(path, func() error { return nil }); err == nil {
		t.Fatal("WithLock accepted a symlink")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "original" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
}

func TestUpdateCASOwnsRevisionAndRejectsStaleWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := UpdateCAS(path, 0, func(candidate *Config) error {
		candidate.Revision = 99
		candidate.Node.DisplayName = "first"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeRevision != 0 || result.AfterRevision != 1 {
		t.Fatalf("revision transition = %+v", result)
	}
	called := false
	if _, err := UpdateCAS(path, 0, func(*Config) error {
		called = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "config.revision_conflict") {
		t.Fatalf("stale update error = %v", err)
	}
	if called {
		t.Fatal("stale mutation callback was called")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.Node.DisplayName != "first" {
		t.Fatalf("committed config = %+v", loaded)
	}
}

func TestUpdateCASRejectsMissingRevisionAndMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := UpdateCAS(path, -1, func(*Config) error { return nil }); err == nil || !strings.Contains(err.Error(), "config.revision_required") {
		t.Fatalf("missing revision error = %v", err)
	}
	if _, err := UpdateCAS(path, 0, nil); err == nil || !strings.Contains(err.Error(), "config.mutation_required") {
		t.Fatalf("missing mutation error = %v", err)
	}
}

func TestUpdateCASRejectsMissingLiveConfigWithoutRecreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	called := false
	if _, err := UpdateCAS(path, 0, func(*Config) error {
		called = true
		return nil
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("UpdateCAS error = %v, want os.ErrNotExist", err)
	}
	if called {
		t.Fatal("mutation callback was called for a missing live config")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing live config was recreated: %v", err)
	}
}

func TestUpdateCASReportsUnknownEvenWhenVisibleCommitCanBeConfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := updateCASWith(path, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "committed"
		return nil
	}, Load, func(path string, candidate Config) error {
		if err := Save(path, candidate); err != nil {
			return err
		}
		return fmt.Errorf("%w: injected parent sync failure", ErrCommitOutcomeUnknown)
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) || !strings.Contains(err.Error(), "config.commit_visible_and_resynced") {
		t.Fatalf("error = %v, want visible but durability-unknown commit", err)
	}
	if result != (UpdateResult{}) {
		t.Fatalf("public result on unknown outcome = %+v", result)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.Node.DisplayName != "committed" {
		t.Fatalf("visible commit = %+v", loaded)
	}
}

func TestUpdateCASKeepsUnknownOutcomeWhenRevisionCannotBeConfirmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loadCalls := 0
	_, err := updateCASWith(path, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "uncertain"
		return nil
	}, func(path string) (Config, error) {
		loadCalls++
		if loadCalls == 1 {
			return Load(path)
		}
		return Config{}, errors.New("injected verification read failure")
	}, func(string, Config) error {
		return fmt.Errorf("%w: injected parent sync failure", ErrCommitOutcomeUnknown)
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) || !strings.Contains(err.Error(), "verification read failure") {
		t.Fatalf("error = %v, want unknown outcome and verification error", err)
	}
}

func TestUpdateCASDoesNotConfirmDifferentContentAtSameRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loadCalls := 0
	_, err := updateCASWith(path, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "expected"
		return nil
	}, func(path string) (Config, error) {
		loadCalls++
		if loadCalls == 1 {
			return Load(path)
		}
		other, err := Load(path)
		other.Revision = 1
		other.Node.DisplayName = "different"
		return other, err
	}, func(string, Config) error {
		return fmt.Errorf("%w: injected parent sync failure", ErrCommitOutcomeUnknown)
	})
	if !errors.Is(err, ErrCommitOutcomeUnknown) || !strings.Contains(err.Error(), "config.commit_confirmation_mismatch") {
		t.Fatalf("error = %v, want unknown outcome and content mismatch", err)
	}
}

func TestUpdateCASRejectsRevisionExhaustionBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Revision = math.MaxInt64
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := UpdateCAS(path, math.MaxInt64, func(*Config) error {
		called = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "config.revision_exhausted") {
		t.Fatalf("revision exhaustion error = %v", err)
	}
	if called {
		t.Fatal("mutation callback ran after revision exhaustion")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != math.MaxInt64 {
		t.Fatalf("revision changed to %d", loaded.Revision)
	}
}

func TestValidateRejectsUnknownProfileReference(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	cfg.Peers = []PeerConfig{{
		Name:          "B",
		NodeID:        "B",
		Direction:     "outbound",
		GatewayAddr:   "b:19080",
		XrayProfileID: "missing",
		Enabled:       true,
		RendrCapable:  true,
	}}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestAtomicWriteReplaceFailurePreservesTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaceErr := errors.New("simulated interruption before replace")
	err := writeFileAtomicWith(path, []byte("new"), 0o600, func(_, _ string) error {
		return replaceErr
	})
	if !errors.Is(err, replaceErr) {
		t.Fatalf("write error = %v, want %v", err, replaceErr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target = %q, want old content", got)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files left behind: %v", temps)
	}
}

func TestAtomicWriteRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := writeFileAtomic(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "backup-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writeFileAtomic(alias, []byte("replacement"), 0o600); err == nil {
		t.Fatal("atomic write accepted a symlink target")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "original" {
		t.Fatalf("symlink target changed: %q", contents)
	}
}

func TestLoadPreservesExplicitFalseAndDoesNotForgePeerIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := `{
  "revision": 1,
  "node": {"node_id": "node-0123456789abcdef0123456789abcdef", "rendr_capable": false},
  "system": {},
  "peers": [{
    "name": "peer-a",
    "node_id": "peer-id",
    "direction": "outbound",
    "addr": "127.0.0.1:19080",
    "nested_enabled": false,
    "enabled": false,
    "rendr_capable": false
  }]
}`
	if err := writeFileAtomic(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Node.RendrCapable {
		t.Fatal("explicit node rendr_capable=false changed to true")
	}
	peer := cfg.Peers[0]
	if peer.Enabled || peer.NestedEnabled || peer.RendrCapable {
		t.Fatalf("explicit peer false values changed: %+v", peer)
	}
	if peer.InstanceID != "" {
		t.Fatalf("peer instance ID was forged: %q", peer.InstanceID)
	}
}

func TestLoadRejectsStructurallyInvalidObservedChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := `{
  "revision": 1,
  "node": {},
  "system": {},
  "xray_profiles": {
    "vless": {
      "id": "vless",
      "kind": "vless",
      "vless": {
        "uuid": "66ad4540-b58c-4ad2-9926-ea63445a9b57",
        "transport": "tcp",
        "security": "none",
        "allow_insecure_plaintext": true
      }
    }
  },
  "peers": [{
    "name": "peer-a",
    "node_id": "peer-id",
    "direction": "outbound",
    "addr": "127.0.0.1:19080",
    "xray_profile_id": "vless",
    "enabled": true,
    "children": [{"name": "legacy-child"}]
  }]
}`
	if err := writeFileAtomic(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config.peer_node_id_required") {
		t.Fatalf("invalid observed child load error = %v", err)
	}
}

func TestValidateObservedChildrenUsesRemoteRatherThanLocalProfileScope(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.Peers[0].Children = []PeerConfig{{
		Name: "peer-c", NodeID: "peer-c-id", Direction: route.DirectionOutbound,
		GatewayAddr: "peer-c.remote.invalid:2443", Enabled: true, XrayProfileID: "profile-owned-by-peer-b",
	}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("remote observed child was validated as a locally managed peer: %v", err)
	}
	cfg.Peers[0].Children = append(cfg.Peers[0].Children, PeerConfig{
		Name: "peer-d", NodeID: "peer-c-id", Direction: route.DirectionInbound,
	})
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_node_id_duplicate") {
		t.Fatalf("duplicate observed identity error = %v", err)
	}
}

func TestLoadRejectsUnknownFieldAndTrailingJSON(t *testing.T) {
	tests := map[string]string{
		"unknown":  `{"revision":0,"node":{},"system":{},"unexpected":true}`,
		"trailing": `{"revision":0,"node":{},"system":{}} {}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := writeFileAtomic(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected strict load error")
			}
		})
	}
}

func TestLoadRejectsOversizedConfigBeforeJSONParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := writeFileAtomic(path, make([]byte, maxConfigFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config.file_too_large") {
		t.Fatalf("oversized config error = %v", err)
	}
}

func TestLoadOrMigrateRejectsUnknownUnversionedFieldWithoutRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := []byte(`{"revision":7,"node":{},"system":{},"future_extension":{"enabled":true}}`)
	if err := writeFileAtomic(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict legacy load error = %v", err)
	}
	if _, migrated, err := LoadOrMigrate(path); err == nil || migrated || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unsafe migration migrated=%v err=%v", migrated, err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, legacy) {
		t.Fatalf("rejected config was rewritten: before=%s after=%s", legacy, payload)
	}
	backups, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(backups) != 0 {
		t.Fatalf("rejected config created backups=%v err=%v", backups, err)
	}
}

func TestLoadOrMigrateVersionsUnversionedConfigWithoutUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	legacy := []byte(`{"revision":3,"node":{},"system":{}}`)
	if err := writeFileAtomic(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, migrated, err := LoadOrMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || cfg.SchemaVersion != CurrentSchemaVersion || cfg.Revision != 4 {
		t.Fatalf("migration result = %+v migrated=%v", cfg, migrated)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"schema_version": 1`)) {
		t.Fatalf("migrated file is not versioned: %s", payload)
	}
}

func TestLoadOrMigrateNeverRewritesCurrentUnknownOrNewerSchema(t *testing.T) {
	for name, payload := range map[string][]byte{
		"current unknown": []byte(`{"schema_version":1,"revision":7,"node":{},"system":{},"typo":true}`),
		"newer schema":    []byte(`{"schema_version":2,"revision":7,"node":{},"system":{},"future":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := writeFileAtomic(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, migrated, err := LoadOrMigrate(path); err == nil || migrated {
				t.Fatalf("unsafe migration migrated=%v err=%v", migrated, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, payload) {
				t.Fatalf("rejected config was rewritten: before=%s after=%s", payload, after)
			}
			backups, err := filepath.Glob(path + ".bak.*")
			if err != nil || len(backups) != 0 {
				t.Fatalf("rejected config created backups=%v err=%v", backups, err)
			}
		})
	}
}

func TestLoadOrMigrateDoesNotRelaxMalformedOrTrailingJSON(t *testing.T) {
	for name, payload := range map[string]string{
		"malformed": `{"revision":0,"node":{},"system":{},`,
		"trailing":  `{"revision":0,"node":{},"system":{},"removed":true} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := writeFileAtomic(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, migrated, err := LoadOrMigrate(path); err == nil || migrated {
				t.Fatalf("unsafe migration migrated=%v err=%v", migrated, err)
			}
		})
	}
}

func TestCanonicalPathResolvesExistingParentAliasAndAllowsMissingTarget(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}

	got, err := CanonicalPath(filepath.Join(alias, "missing", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalParent, "missing", "config.json")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
}

func TestCanonicalPathRejectsFileAsExistingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalPath(filepath.Join(parent, "config.json")); err == nil {
		t.Fatal("CanonicalPath accepted a regular file as a parent")
	}
}

func TestSaveReportsBackupReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	err := Save(path, DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "config.backup_read") {
		t.Fatalf("save error = %v, want backup read error", err)
	}
}

func TestValidateNodeIdentityClassification(t *testing.T) {
	raw := make([]byte, identity.SeedSize)
	for index := range raw {
		raw[index] = byte(index + 1)
	}
	seed, err := identity.NewNodeSeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := identity.FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	public := derived.Public()

	tests := []struct {
		name      string
		node      NodeConfig
		wantError string
	}{
		{name: "uninitialized"},
		{name: "v2", node: NodeConfig{NodeID: public.NodeID.String(), PublicKey: public.PublicKey}},
		{name: "legacy", node: NodeConfig{NodeID: testLegacyNodeID}},
		{name: "v2 mismatch", node: NodeConfig{NodeID: public.NodeID.String(), PublicKey: strings.Repeat("a", 52)}, wantError: "config.node_identity_invalid"},
		{name: "legacy public key", node: NodeConfig{NodeID: testLegacyNodeID, PublicKey: "legacy-public-key"}, wantError: "config.node_identity_invalid"},
		{name: "arbitrary legacy", node: NodeConfig{NodeID: "legacy-random-id"}, wantError: "config.node_identity_invalid"},
		{name: "public key only", node: NodeConfig{PublicKey: public.PublicKey}, wantError: "config.node_identity_invalid"},
		{name: "unknown suite", node: NodeConfig{NodeID: "xtier-v3-ed25519-unknown", PublicKey: public.PublicKey}, wantError: "config.node_identity_unsupported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Node = tc.node
			err := Validate(cfg)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("valid identity rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %s", err, tc.wantError)
			}
		})
	}
}

func TestValidateRejectsAmbiguousPeerIdentifiers(t *testing.T) {
	profile := XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	tests := map[string][]PeerConfig{
		"duplicate node id": {
			{Name: "A", NodeID: "node-a", Direction: route.DirectionInbound},
			{Name: "B", NodeID: "node-a", Direction: route.DirectionInbound},
		},
		"name collides with node id": {
			{Name: "A", NodeID: "node-a", Direction: route.DirectionInbound},
			{Name: "node-a", NodeID: "node-b", Direction: route.DirectionInbound},
		},
	}
	for name, peers := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.XrayProfiles[profile.ID] = profile
			cfg.Peers = peers
			if err := Validate(cfg); err == nil {
				t.Fatal("ambiguous peer identifiers were accepted")
			}
		})
	}
}

func TestValidateRequiresCompatibleOutboundProfile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{Username: "u", Password: "p"}}
	cfg.Peers = []PeerConfig{{
		Name: "B", NodeID: "node-b", Direction: route.DirectionOutbound, GatewayAddr: "127.0.0.1:2443",
		XrayProfileID: "socks", Enabled: true,
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_profile_incompatible") {
		t.Fatalf("error = %v, want incompatible outbound profile", err)
	}
}

func TestValidateConfinesPlaintextEndpointsToIsolatedPrivateAddresses(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "public peer", mutate: func(cfg *Config) { cfg.Peers[0].GatewayAddr = "198.51.100.7:2443" }, want: "config.plaintext_endpoint_private_ip_required"},
		{name: "domain peer", mutate: func(cfg *Config) { cfg.Peers[0].GatewayAddr = "peer.example:2443" }, want: "config.plaintext_endpoint_ip_literal_required"},
		{name: "wildcard node inbound", mutate: func(cfg *Config) {
			cfg.NodeInbound = append(cfg.NodeInbound, InboundConfig{Kind: "node-vless", Purpose: "node", Listen: "0.0.0.0:2443", Enabled: true})
		}, want: "config.plaintext_endpoint_wildcard_forbidden"},
		{name: "public node inbound", mutate: func(cfg *Config) {
			cfg.NodeInbound = append(cfg.NodeInbound, InboundConfig{Kind: "node-vless", Purpose: "node", Listen: "203.0.113.9:2443", Enabled: true})
		}, want: "config.plaintext_endpoint_private_ip_required"},
		{name: "wildcard user inbound", mutate: func(cfg *Config) {
			cfg.NodeInbound[0].Listen = "0.0.0.0:1080"
		}, want: "config.plaintext_endpoint_wildcard_forbidden"},
		{name: "public user inbound", mutate: func(cfg *Config) {
			cfg.NodeInbound[0].Listen = "203.0.113.10:1080"
		}, want: "config.plaintext_endpoint_private_ip_required"},
		{name: "domain user inbound", mutate: func(cfg *Config) {
			cfg.NodeInbound[0].Listen = "proxy.example:1080"
		}, want: "config.plaintext_endpoint_ip_literal_required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validRuntimeConfigForValidation()
			tc.mutate(&cfg)
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestValidateRequiresAuthenticationForLoopbackSOCKSInbound(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_auth_required") {
		t.Fatalf("error = %v, want authentication required", err)
	}
}

func TestValidateRejectsEnabledUserInboundWithUnavailableExitPeer(t *testing.T) {
	for name, direction := range map[string]route.Direction{
		"disabled outbound": route.DirectionOutbound,
		"inbound only":      route.DirectionInbound,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validRuntimeConfigForValidation()
			cfg.Peers[0].Direction = direction
			if name == "disabled outbound" {
				cfg.Peers[0].Enabled = false
			}
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_exit_peer_unavailable") {
				t.Fatalf("error = %v, want unavailable exit peer rejection", err)
			}
		})
	}
}

func TestValidateRejectsPartialSOCKSCredentialsBeforeRuntimeCompile(t *testing.T) {
	for name, profile := range map[string]*SOCKSProfile{
		"missing password": {Username: "terminal"},
		"missing username": {Password: "entry-secret"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validRuntimeConfigForValidation()
			cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: profile}
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.profile_invalid") {
				t.Fatalf("error = %v, want profile validation failure", err)
			}
		})
	}
}

func TestValidateRejectsSOCKSAndVLESSCredentialReuse(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{
		Username: "terminal", Password: cfg.XrayProfiles["vless"].VLESS.UUID,
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.credential_reuse_forbidden") {
		t.Fatalf("error = %v, want credential reuse rejection", err)
	}
}

func TestValidateRejectsXrayEquivalentSOCKSAndVLESSCredentialReuse(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{
		Username: "terminal", Password: "66ad4540-b58c-4ead-9926-ea63445a9b57",
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.credential_reuse_forbidden") {
		t.Fatalf("error = %v, want Xray-equivalent credential reuse rejection", err)
	}
}

func TestValidateRejectsEveryXrayAcceptedSOCKSAliasOfVLESSCredential(t *testing.T) {
	tests := map[string]struct {
		vless string
		socks string
	}{
		"uppercase": {
			vless: "66ad4540-b58c-4ad2-9926-ea63445a9b57",
			socks: "66AD4540-B58C-4AD2-9926-EA63445A9B57",
		},
		"undashed": {
			vless: "66ad4540-b58c-4ad2-9926-ea63445a9b57",
			socks: "66ad4540b58c4ad29926ea63445a9b57",
		},
	}
	short := "peer-a-secret"
	parsed, err := uuid.ParseString(short)
	if err != nil {
		t.Fatal(err)
	}
	tests["short SHA-1 mapping"] = struct {
		vless string
		socks string
	}{vless: parsed.String(), socks: short}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validRuntimeConfigForValidation()
			cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
				UUID: test.vless, Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			}}
			cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{
				Username: "terminal", Password: test.socks,
			}}
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.credential_reuse_forbidden") {
				t.Fatalf("error = %v, want Xray-equivalent credential reuse rejection", err)
			}
		})
	}
}

func TestValidateRejectsVLESSCredentialReuseInSOCKSUsername(t *testing.T) {
	aliases := []string{
		"66ad4540-b58c-4ad2-9926-ea63445a9b57",
		"66AD4540-B58C-4AD2-9926-EA63445A9B57",
		"66ad4540b58c4ad29926ea63445a9b57",
	}
	for _, username := range aliases {
		t.Run(username, func(t *testing.T) {
			cfg := validRuntimeConfigForValidation()
			cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
				UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			}}
			cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{
				Username: username, Password: "socks-password-not-a-vless-credential-123456",
			}}
			if err := Validate(cfg); err == nil ||
				!strings.Contains(err.Error(), "config.credential_reuse_forbidden") ||
				!strings.Contains(err.Error(), "field=username") {
				t.Fatalf("error = %v, want SOCKS username credential reuse rejection", err)
			}
		})
	}

	short := "peer-a-secret"
	parsed, err := uuid.ParseString(short)
	if err != nil {
		t.Fatal(err)
	}
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: parsed.String(), Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{
		Username: short, Password: "socks-password-not-a-vless-credential-123456",
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "field=username") {
		t.Fatalf("short username mapping error = %v, want username credential reuse rejection", err)
	}
}

func TestValidateRejectsDuplicateInboundKinds(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	duplicate := cfg.NodeInbound[0]
	duplicate.Listen = "127.0.0.1:2080"
	duplicate.Enabled = false
	cfg.NodeInbound = append(cfg.NodeInbound, duplicate)
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_duplicate") {
		t.Fatalf("error = %v, want duplicate inbound rejection", err)
	}
}

func TestValidateRejectsEnabledInboundListenConflict(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["inbound-vless"] = XrayProfile{ID: "inbound-vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "e2f7cbec-a847-44ed-b3df-ceae1f9aa252", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.Peers = append(cfg.Peers, PeerConfig{
		Name: "A", NodeID: "node-a", Direction: route.DirectionInbound,
		XrayProfileID: "inbound-vless", Enabled: true,
	})
	cfg.NodeInbound = append(cfg.NodeInbound, InboundConfig{
		Kind: "node-vless", Purpose: "node", Listen: "127.0.0.1:1080", Enabled: true,
	})
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_listen_conflict") {
		t.Fatalf("error = %v, want inbound listener conflict", err)
	}

	cfg.NodeInbound[1].Enabled = false
	if err := Validate(cfg); err != nil {
		t.Fatalf("disabled inbound reserved a listener: %v", err)
	}
}

func TestSortStablePreservesEquivalentInboundOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NodeInbound = []InboundConfig{
		{Kind: "socks", Listen: "127.0.0.1:1080"},
		{Kind: "node-vless", Listen: "127.0.0.1:2443"},
		{Kind: "socks", Listen: "127.0.0.1:2080"},
	}
	SortStable(&cfg)
	if cfg.NodeInbound[1].Listen != "127.0.0.1:1080" || cfg.NodeInbound[2].Listen != "127.0.0.1:2080" {
		t.Fatalf("equivalent inbound order changed: %+v", cfg.NodeInbound)
	}
}

func TestValidateRequiresDistinctVLESSCredentialPerInboundPeer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.XrayProfiles["shared"] = XrayProfile{ID: "shared", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "e2f7cbec-a847-44ed-b3df-ceae1f9aa252", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.Peers = []PeerConfig{
		{Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, XrayProfileID: "shared", Enabled: true},
		{Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, XrayProfileID: "shared", Enabled: true},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_inbound_credential_duplicate") {
		t.Fatalf("error = %v, want inbound credential reuse rejection", err)
	}
}

func TestValidateRejectsXrayEquivalentInboundVLESSCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.XrayProfiles["first"] = XrayProfile{ID: "first", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.XrayProfiles["collision"] = XrayProfile{ID: "collision", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ead-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.Peers = []PeerConfig{
		{Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, XrayProfileID: "first", Enabled: true},
		{Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, XrayProfileID: "collision", Enabled: true},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_inbound_credential_duplicate") {
		t.Fatalf("error = %v, want Xray-equivalent credential rejection", err)
	}
}

func TestValidateRejectsNonCanonicalVLESSCredential(t *testing.T) {
	cfg := validRuntimeConfigForValidation()
	cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66AD4540-B58C-4AD2-9926-EA63445A9B57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.profile_invalid") {
		t.Fatalf("error = %v, want non-canonical credential rejection", err)
	}
}

func TestValidateRejectsEnabledInboundPeerWithoutRuntimeCredential(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Peers = []PeerConfig{{
		Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, Enabled: true,
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_inbound_profile_incompatible") {
		t.Fatalf("error = %v, want missing inbound credential rejection", err)
	}
	cfg.Peers[0].Enabled = false
	if err := Validate(cfg); err != nil {
		t.Fatalf("disabled address-book-only inbound peer was rejected: %v", err)
	}
}

func TestLocalProfileInUseIgnoresObservedChildren(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Peers = []PeerConfig{{
		Name: "managed", NodeID: "managed-id", XrayProfileID: "managed-profile",
		Children: []PeerConfig{{Name: "observed", NodeID: "observed-id", XrayProfileID: "local"}},
	}}
	if LocalProfileInUse(cfg, "local") {
		t.Fatal("remote observed child pinned a local profile")
	}
	cfg.Peers[0].XrayProfileID = "local"
	if !LocalProfileInUse(cfg, "local") {
		t.Fatal("directly managed peer did not pin its local profile")
	}
	cfg.Peers[0].XrayProfileID = "managed-profile"
	cfg.NodeInbound = []InboundConfig{{XrayProfileID: "local"}}
	if !LocalProfileInUse(cfg, "local") {
		t.Fatal("local inbound did not pin its profile")
	}
}

func TestValidateRejectsNodeInboundProfileEvenWhileDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.NodeInbound = []InboundConfig{{
		Kind: "node-vless", Purpose: "node", Listen: "127.0.0.1:2443", XrayProfileID: "vless", Enabled: false,
	}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_profile_forbidden") {
		t.Fatalf("error = %v, want node listener profile rejection", err)
	}
}

func validRuntimeConfigForValidation() Config {
	cfg := DefaultConfig()
	cfg.XrayProfiles["vless"] = XrayProfile{ID: "vless", Kind: "vless", VLESS: &VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.XrayProfiles["socks"] = XrayProfile{ID: "socks", Kind: "socks", SOCKS: &SOCKSProfile{Username: "terminal", Password: "entry-secret"}}
	cfg.Peers = []PeerConfig{{
		Name: "B", NodeID: "node-b", Direction: route.DirectionOutbound, GatewayAddr: "127.0.0.1:2443",
		XrayProfileID: "vless", Enabled: true,
	}}
	cfg.NodeInbound = []InboundConfig{{
		Kind: "socks", Purpose: "user", Listen: "127.0.0.1:1080", Enabled: true, XrayProfileID: "socks", ExitPeer: "B",
	}}
	return cfg
}

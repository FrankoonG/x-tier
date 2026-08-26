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

func TestDocumentCodecIsStrictAndCanonical(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Revision = 7
	cfg.Node.DisplayName = "codec"
	payload, err := EncodeDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("encoded document is not newline terminated: %q", payload)
	}
	decoded, err := DecodeDocument(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Revision != 7 || decoded.Node.DisplayName != "codec" {
		t.Fatalf("decoded document = %+v", decoded)
	}

	unknown := bytes.Replace(payload, []byte("\n}"), []byte(",\n  \"future\": true\n}"), 1)
	if bytes.Equal(unknown, payload) {
		t.Fatal("unknown-field fixture did not modify the document")
	}
	if _, err := DecodeDocument(unknown); err == nil || !IsContentError(err) {
		t.Fatalf("unknown field error=%v, want classified content error", err)
	}
	trailing := append(append([]byte(nil), payload...), []byte("{}")...)
	if _, err := DecodeDocument(trailing); err == nil || !IsContentError(err) {
		t.Fatalf("trailing JSON error=%v, want classified content error", err)
	}
}

func TestConfigJSONRejectsDuplicateObjectFieldsRecursively(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		sensitive []string
	}{
		{
			name:      "schema version",
			payload:   `{"schema_version":2,"schema_version":1,"revision":0,"node":{},"system":{}}`,
			sensitive: []string{"schema_version"},
		},
		{
			name: "top-level grants field",
			payload: `{"schema_version":2,"revision":0,"node":{},"system":{},` +
				`"node_egress_grants":{},"node_egress_grants":{"secret-peer":{}}}`,
			sensitive: []string{"node_egress_grants", "secret-peer"},
		},
		{
			name: "grant map key",
			payload: `{"schema_version":2,"revision":0,"node":{},"system":{},"node_egress_grants":{` +
				`"secret-peer":{"source_node_id":"secret-peer","network":"tcp","allow_ports":[]},` +
				`"secret-peer":{"source_node_id":"secret-peer","network":"tcp","allow_ports":[]}}}`,
			sensitive: []string{"secret-peer"},
		},
		{
			name: "nested grant field",
			payload: `{"schema_version":2,"revision":0,"node":{},"system":{},"node_egress_grants":{` +
				`"node-a":{"source_node_id":"node-a","network":"tcp","network":"secret-network","allow_ports":[]}}}`,
			sensitive: []string{"network", "secret-network"},
		},
		{
			name: "grant port field in array",
			payload: `{"schema_version":2,"revision":0,"node":{},"system":{},"node_egress_grants":{` +
				`"node-a":{"source_node_id":"node-a","network":"tcp","allow_ports":[{"from":443,"\u0066rom":8443,"to":443}]}}}`,
			sensitive: []string{"from", "8443"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeDocument([]byte(test.payload))
			assertDuplicateConfigFieldError(t, err, test.sensitive...)
		})
	}
}

func TestDuplicateFieldPreflightCoversSchemaAndVersionedDecoders(t *testing.T) {
	current := []byte(`{"schema_version":2,"revision":0,"node":{"display_name":"first","display_name":"secret-current"},"system":{}}`)
	legacy := []byte(`{"schema_version":1,"revision":0,"node":{"display_name":"first","display_name":"secret-legacy"},"system":{}}`)

	if _, _, err := configSchemaFromJSON(current); err == nil {
		t.Fatal("configSchemaFromJSON accepted duplicate nested field")
	} else {
		assertDuplicateConfigFieldError(t, err, "display_name", "secret-current")
	}
	if _, err := decodeConfig(current); err == nil {
		t.Fatal("decodeConfig accepted duplicate nested field")
	} else {
		assertDuplicateConfigFieldError(t, err, "display_name", "secret-current")
	}
	if _, err := decodeConfigV1(legacy); err == nil {
		t.Fatal("decodeConfigV1 accepted duplicate nested field")
	} else {
		assertDuplicateConfigFieldError(t, err, "display_name", "secret-legacy")
	}
}

func assertDuplicateConfigFieldError(t *testing.T, err error, sensitive ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("duplicate field was accepted")
	}
	if !IsContentError(err) {
		t.Fatalf("error %T = %v, want content error", err, err)
	}
	var coded interface{ PublicErrorCode() string }
	if !errors.As(err, &coded) {
		t.Fatalf("error %T = %v, want public error code", err, err)
	}
	if got := coded.PublicErrorCode(); got != "config.json_duplicate_field" {
		t.Fatalf("public error code = %q, want config.json_duplicate_field", got)
	}
	if got := err.Error(); got != "config.json_duplicate_field" {
		t.Fatalf("error text = %q, want stable duplicate-field error", got)
	}
	for _, value := range sensitive {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error text leaked sensitive JSON content %q: %v", value, err)
		}
	}
}

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
	if cfg.NodeEgressGrants == nil || len(cfg.NodeEgressGrants) != 0 {
		t.Fatalf("default node egress grants = %#v, want non-nil deny-all map", cfg.NodeEgressGrants)
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
	lockPath := path + ".lock"
	if err := os.Symlink(target, lockPath); err != nil {
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
	if !bytes.Contains(payload, []byte(`"schema_version": 2`)) {
		t.Fatalf("migrated file is not versioned: %s", payload)
	}
}

func TestLoadOrMigrateStrictlyMigratesExplicitV1ExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	v1 := []byte(`{"schema_version":1,"revision":7,"node":{},"system":{}}`)
	if err := writeFileAtomic(path, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, migrated, err := LoadOrMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || cfg.SchemaVersion != 2 || cfg.Revision != 8 {
		t.Fatalf("first migration = %+v migrated=%v", cfg, migrated)
	}
	if cfg.NodeEgressGrants == nil || len(cfg.NodeEgressGrants) != 0 {
		t.Fatalf("v1 migration forged grants: %#v", cfg.NodeEgressGrants)
	}

	second, migratedAgain, err := LoadOrMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	if migratedAgain || second.SchemaVersion != 2 || second.Revision != 8 || len(second.NodeEgressGrants) != 0 {
		t.Fatalf("second migration = %+v migrated=%v", second, migratedAgain)
	}
}

func TestLoadOrMigrateV1RejectsEveryUnknownExtensionWithoutRewrite(t *testing.T) {
	fixtures := map[string][]byte{
		"top level": []byte(`{"schema_version":1,"revision":7,"node":{},"system":{},"future":true}`),
		"nested":    []byte(`{"schema_version":1,"revision":7,"node":{"future":true},"system":{}}`),
		"v2 grant":  []byte(`{"schema_version":1,"revision":7,"node":{},"system":{},"node_egress_grants":{}}`),
	}
	for name, payload := range fixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := writeFileAtomic(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, migrated, err := LoadOrMigrate(path); err == nil || migrated || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("migration result migrated=%v err=%v", migrated, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, payload) {
				t.Fatalf("rejected v1 was rewritten: before=%s after=%s", payload, after)
			}
		})
	}
}

func TestLoadOrMigrateV1RevisionExhaustionDoesNotRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	payload := []byte(fmt.Sprintf(`{"schema_version":1,"revision":%d,"node":{},"system":{}}`, int64(math.MaxInt64)))
	if err := writeFileAtomic(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, migrated, err := LoadOrMigrate(path); err == nil || migrated || !strings.Contains(err.Error(), "config.revision_exhausted") {
		t.Fatalf("migration result migrated=%v err=%v", migrated, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, payload) {
		t.Fatalf("revision-exhausted v1 was rewritten: before=%s after=%s", payload, after)
	}
}

func TestLoadOrMigrateUnversionedStrictlyPreservesKnownV2Grant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	payload := []byte(`{
  "revision": 4,
  "node": {},
  "system": {},
  "peers": [{"name":"A","node_id":"node-a","direction":"inbound","nested_enabled":false,"enabled":false,"rendr_capable":false}],
  "node_egress_grants": {
    "node-a": {
      "source_node_id":"node-a",
      "network":"tcp",
      "allow_cidrs":["8.0.0.0/8"],
      "allow_ports":[{"from":443,"to":443}]
    }
  }
}`)
	if err := writeFileAtomic(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, migrated, err := LoadOrMigrate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || cfg.SchemaVersion != 2 || cfg.Revision != 5 || len(cfg.NodeEgressGrants) != 1 {
		t.Fatalf("migration = %+v migrated=%v", cfg, migrated)
	}
}

func TestLoadOrMigrateNeverRewritesCurrentUnknownOrNewerSchema(t *testing.T) {
	for name, payload := range map[string][]byte{
		"current unknown": []byte(`{"schema_version":2,"revision":7,"node":{},"system":{},"typo":true}`),
		"newer schema":    []byte(`{"schema_version":3,"revision":7,"node":{},"system":{},"future":true}`),
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

func TestValidateAcceptsExplicitNodeEgressGrantForDirectInboundPeer(t *testing.T) {
	cfg := validNodeEgressGrantConfig()
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Peers[0].Enabled {
		t.Fatal("fixture no longer proves a grant may survive a temporarily disabled inbound peer")
	}
}

func TestValidateRejectsInvalidNodeEgressGrant(t *testing.T) {
	tests := map[string]struct {
		want   string
		mutate func(*Config)
	}{
		"key source mismatch": {
			want: "config.node_egress_grant_source_mismatch",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.SourceNodeID = "node-b"
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"noncanonical source": {
			want: "config.node_egress_grant_source_mismatch",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				delete(cfg.NodeEgressGrants, "node-a")
				grant.SourceNodeID = " node-a"
				cfg.NodeEgressGrants[" node-a"] = grant
			},
		},
		"unknown peer": {
			want: "config.node_egress_grant_peer_unknown",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				delete(cfg.NodeEgressGrants, "node-a")
				grant.SourceNodeID = "node-missing"
				cfg.NodeEgressGrants["node-missing"] = grant
			},
		},
		"nested peer": {
			want: "config.node_egress_grant_peer_unknown",
			mutate: func(cfg *Config) {
				cfg.Peers[0].Children = []PeerConfig{{Name: "child", NodeID: "node-child", Direction: route.DirectionInbound}}
				grant := cfg.NodeEgressGrants["node-a"]
				delete(cfg.NodeEgressGrants, "node-a")
				grant.SourceNodeID = "node-child"
				cfg.NodeEgressGrants["node-child"] = grant
			},
		},
		"outbound only": {
			want: "config.node_egress_grant_peer_inbound_required",
			mutate: func(cfg *Config) {
				cfg.Peers[0].Direction = route.DirectionOutbound
				cfg.Peers[0].GatewayAddr = "127.0.0.1:2443"
			},
		},
		"network required": {
			want: "config.node_egress_grant_network_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.Network = "udp"
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"no allow": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = nil
				grant.AllowPrivateCIDRs = nil
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"no ports": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowPorts = nil
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"noncanonical CIDR": {
			want: "config.node_egress_grant_cidr_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = []string{"8.8.8.1/24"}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"mapped CIDR": {
			want: "config.node_egress_grant_cidr_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = []string{"::ffff:0:0/96"}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"duplicate CIDR": {
			want: "config.node_egress_grant_cidr_duplicate",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = []string{"8.0.0.0/8", "8.0.0.0/8"}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"private in public list": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = []string{"10.0.0.0/8"}
				grant.AllowPrivateCIDRs = nil
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"public in private list": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowCIDRs = nil
				grant.AllowPrivateCIDRs = []string{"8.0.0.0/8"}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"zero port": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowPorts = []EgressPortRange{{From: 0, To: 443}}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"reverse port": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowPorts = []EgressPortRange{{From: 444, To: 443}}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
		"overlapping ports": {
			want: "config.node_egress_grant_invalid",
			mutate: func(cfg *Config) {
				grant := cfg.NodeEgressGrants["node-a"]
				grant.AllowPorts = []EgressPortRange{{From: 400, To: 500}, {From: 443, To: 443}}
				cfg.NodeEgressGrants["node-a"] = grant
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validNodeEgressGrantConfig()
			test.mutate(&cfg)
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestNodeEgressGrantCanonicalEncodingSortsRuleLists(t *testing.T) {
	cfg := validNodeEgressGrantConfig()
	grant := cfg.NodeEgressGrants["node-a"]
	grant.AllowCIDRs = []string{"9.0.0.0/8", "8.0.0.0/8"}
	grant.AllowPrivateCIDRs = []string{"192.168.0.0/16", "10.0.0.0/8"}
	grant.DenyCIDRs = []string{"9.9.0.0/16", "8.8.0.0/16"}
	grant.AllowPorts = []EgressPortRange{{From: 8000, To: 8099}, {From: 443, To: 443}}
	cfg.NodeEgressGrants["node-a"] = grant

	payload, err := EncodeDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDocument(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded.NodeEgressGrants["node-a"]
	if fmt.Sprint(got.AllowCIDRs) != "[8.0.0.0/8 9.0.0.0/8]" ||
		fmt.Sprint(got.AllowPrivateCIDRs) != "[10.0.0.0/8 192.168.0.0/16]" ||
		fmt.Sprint(got.DenyCIDRs) != "[8.8.0.0/16 9.9.0.0/16]" ||
		len(got.AllowPorts) != 2 || got.AllowPorts[0].From != 443 || got.AllowPorts[1].From != 8000 {
		t.Fatalf("grant was not canonically sorted: %+v", got)
	}
	if cfg.NodeEgressGrants["node-a"].AllowCIDRs[0] != "9.0.0.0/8" {
		t.Fatal("EncodeDocument mutated caller-owned configuration")
	}
}

func TestNodeEgressGrantDecodeRejectsUnknownNestedField(t *testing.T) {
	payload := []byte(`{
  "schema_version":2,
  "revision":0,
  "node":{},
  "system":{},
  "peers":[{"name":"A","node_id":"node-a","direction":"inbound","nested_enabled":false,"enabled":false,"rendr_capable":false}],
  "node_egress_grants":{"node-a":{"source_node_id":"node-a","network":"tcp","allow_cidrs":["8.0.0.0/8"],"allow_ports":[{"from":443,"to":443}],"future":true}}
}`)
	if _, err := DecodeDocument(payload); err == nil || !IsContentError(err) || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown grant field error=%v", err)
	}
}

func validNodeEgressGrantConfig() Config {
	cfg := DefaultConfig()
	cfg.Peers = []PeerConfig{{
		Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, Enabled: false,
	}}
	cfg.NodeEgressGrants["node-a"] = NodeEgressGrant{
		SourceNodeID:      "node-a",
		Network:           "tcp",
		AllowCIDRs:        []string{"8.0.0.0/8"},
		AllowPrivateCIDRs: []string{"10.20.0.0/16"},
		DenyCIDRs:         []string{"8.8.8.0/24", "10.20.9.0/24"},
		AllowPorts:        []EgressPortRange{{From: 443, To: 443}, {From: 8000, To: 8099}},
	}
	return cfg
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

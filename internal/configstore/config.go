package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FrankoonG/x-tier/internal/egresspolicy"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/settings"
	"github.com/FrankoonG/x-tier/internal/stablelock"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

type Config struct {
	SchemaVersion    int                        `json:"schema_version"`
	Revision         int64                      `json:"revision"`
	Node             NodeConfig                 `json:"node"`
	System           settings.Config            `json:"system"`
	NodeInbound      []InboundConfig            `json:"node_inbound,omitempty"`
	Peers            []PeerConfig               `json:"peers,omitempty"`
	XrayProfiles     map[string]XrayProfile     `json:"xray_profiles,omitempty"`
	PeerTrust        map[string]PeerTrustGrant  `json:"peer_trust,omitempty"`
	NodeEgressGrants map[string]NodeEgressGrant `json:"node_egress_grants,omitempty"`
}

const (
	CurrentSchemaVersion = 2
	maxConfigBackups     = 5
)

type NodeConfig struct {
	NodeID            string `json:"node_id,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	Role              string `json:"role,omitempty"`
	PublicKey         string `json:"public_key,omitempty"`
	RendrPersistentID string `json:"rendr_persistent_id,omitempty"`
	RendrInstanceID   string `json:"rendr_instance_id,omitempty"`
	RendrCapable      bool   `json:"rendr_capable"`
	Disabled          bool   `json:"disabled,omitempty"`
	DisabledCause     string `json:"disabled_cause,omitempty"`
}

type InboundConfig struct {
	Kind          string `json:"kind"`
	Purpose       string `json:"purpose,omitempty"`
	Listen        string `json:"listen"`
	Enabled       bool   `json:"enabled"`
	XrayProfileID string `json:"xray_profile_id,omitempty"`
	ExitPeer      string `json:"exit_peer,omitempty"`
	DisabledCause string `json:"disabled_cause,omitempty"`
}

type PeerConfig struct {
	Name          string          `json:"name"`
	NodeID        string          `json:"node_id"`
	DisplayName   string          `json:"display_name,omitempty"`
	Addr          string          `json:"addr,omitempty"`
	Direction     route.Direction `json:"direction"`
	XrayProfileID string          `json:"xray_profile_id,omitempty"`
	GatewayAddr   string          `json:"gateway_addr,omitempty"`
	NestedEnabled bool            `json:"nested_enabled"`
	Enabled       bool            `json:"enabled"`
	DisabledCause string          `json:"disabled_cause,omitempty"`
	RendrCapable  bool            `json:"rendr_capable"`
	InstanceID    string          `json:"rendr_instance_id,omitempty"`
	Children      []PeerConfig    `json:"children,omitempty"`
}

type XrayProfile struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	VLESS   *VLESSProfile     `json:"vless,omitempty"`
	SOCKS   *SOCKSProfile     `json:"socks,omitempty"`
	Options map[string]string `json:"options,omitempty"` // Legacy reference; never compiled into a runtime.
}

type VLESSProfile struct {
	UUID                   string `json:"uuid"`
	Flow                   string `json:"flow,omitempty"`
	Transport              string `json:"transport"`
	Security               string `json:"security"`
	AllowInsecurePlaintext bool   `json:"allow_insecure_plaintext,omitempty"`
}

type SOCKSProfile struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type PeerTrustGrant struct {
	PeerNodeID string   `json:"peer_node_id"`
	Allow      []string `json:"allow"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Audit      bool     `json:"audit"`
}

type NodeEgressGrant struct {
	SourceNodeID      string            `json:"source_node_id"`
	Network           string            `json:"network"`
	AllowCIDRs        []string          `json:"allow_cidrs,omitempty"`
	AllowPrivateCIDRs []string          `json:"allow_private_cidrs,omitempty"`
	DenyCIDRs         []string          `json:"deny_cidrs,omitempty"`
	AllowPorts        []EgressPortRange `json:"allow_ports"`
}

type EgressPortRange struct {
	From uint16 `json:"from"`
	To   uint16 `json:"to"`
}

// configV1 is deliberately frozen. Keeping a separate wire shape makes an
// explicit v1 migration reject fields that did not exist in that schema.
type configV1 struct {
	SchemaVersion int                       `json:"schema_version"`
	Revision      int64                     `json:"revision"`
	Node          NodeConfig                `json:"node"`
	System        settings.Config           `json:"system"`
	NodeInbound   []InboundConfig           `json:"node_inbound,omitempty"`
	Peers         []PeerConfig              `json:"peers,omitempty"`
	XrayProfiles  map[string]XrayProfile    `json:"xray_profiles,omitempty"`
	PeerTrust     map[string]PeerTrustGrant `json:"peer_trust,omitempty"`
}

// UpdateResult identifies the committed revision transition of one CAS update.
type UpdateResult struct {
	BeforeRevision int64
	AfterRevision  int64
}

type codedConfigError struct {
	code string
	err  error
}

func (e codedConfigError) Error() string           { return e.err.Error() }
func (e codedConfigError) Unwrap() error           { return e.err }
func (e codedConfigError) PublicErrorCode() string { return e.code }

// configErrorf derives the public code from a package-owned format literal,
// never from the rendered error text or caller-controlled values.
func configErrorf(format string, args ...any) error {
	code, _, _ := strings.Cut(format, ":")
	if !strings.HasPrefix(code, "config.") {
		panic("configstore: invalid coded error format")
	}
	return codedConfigError{code: code, err: fmt.Errorf(format, args...)}
}

type configContentError struct {
	err error
}

func (e configContentError) Error() string { return e.err.Error() }
func (e configContentError) Unwrap() error { return e.err }

// IsContentError reports whether secure file access succeeded but the bytes
// could not be decoded or validated as a configuration. Callers may recover
// such failures from a trusted checkpoint without treating path, permission,
// ownership, or locking failures as recoverable content errors.
func IsContentError(err error) bool {
	var target configContentError
	return errors.As(err, &target)
}

func markContentError(err error) error {
	if err == nil || IsContentError(err) {
		return err
	}
	return configContentError{err: err}
}

type commitVisibleError struct {
	revision int64
	cause    error
}

func (e *commitVisibleError) Error() string {
	return fmt.Sprintf("config.commit_visible_and_resynced: revision=%d: %v", e.revision, e.cause)
}

func (e *commitVisibleError) Unwrap() error { return e.cause }
func (e *commitVisibleError) PublicErrorCode() string {
	return "config.commit_visible_and_resynced"
}

func CommitVisible(err error) bool {
	var visible *commitVisibleError
	return errors.As(err, &visible)
}

func DefaultConfig() Config {
	return Config{
		SchemaVersion:    CurrentSchemaVersion,
		System:           settings.Defaults(),
		XrayProfiles:     map[string]XrayProfile{},
		PeerTrust:        map[string]PeerTrustGrant{},
		NodeEgressGrants: map[string]NodeEgressGrant{},
	}
}

func Load(path string) (Config, error) {
	return load(path)
}

// LoadExisting reads a configuration that must already exist. Daemon runtime
// reconciliation uses this form so deleting a live config cannot be mistaken
// for an operator request to apply the revision-zero defaults.
func LoadExisting(path string) (Config, error) {
	return loadExisting(path)
}

// LoadOrMigrate versions an unversioned file only when every field is known to
// this binary. Versioned v1 documents use their frozen decoder. Unknown fields
// and newer schemas fail closed so migration cannot erase operator data.
func LoadOrMigrate(path string) (Config, bool, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return Config{}, false, err
	}
	return loadOrMigrateWithLock(canonical, false, func(fn func(string) error) error {
		return withLockedPath(canonical, fn)
	})
}

// LoadPinnedOrMigrate is the daemon-owned form. The daemon already holds the
// stable path identity and parent-directory pin, so this takes only the
// per-operation file lock while preserving that lifetime ownership.
func LoadPinnedOrMigrate(configPath, canonicalKey string) (Config, bool, error) {
	if canonicalKey == "" {
		return Config{}, false, configErrorf("config.ownership_key_required")
	}
	return loadOrMigrateWithLock(configPath, true, func(fn func(string) error) error {
		return withLockedPathKey(configPath, canonicalKey, false, false, fn)
	})
}

func loadOrMigrateWithLock(path string, persistMissing bool, withLock func(func(string) error) error) (Config, bool, error) {
	var cfg Config
	migrated := false
	err := withLock(func(lockedPath string) error {
		payload, readErr := readSecureFile(lockedPath)
		if os.IsNotExist(readErr) {
			cfg = DefaultConfig()
			if persistMissing {
				if saveErr := Save(lockedPath, cfg); saveErr != nil {
					return configErrorf("config.initial_write: %w", saveErr)
				}
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
		schemaVersion, versioned, schemaErr := configSchemaFromJSON(payload)
		if schemaErr != nil {
			return schemaErr
		}
		if versioned && schemaVersion > CurrentSchemaVersion {
			return configErrorf("config.schema_newer: have %d support %d", schemaVersion, CurrentSchemaVersion)
		}
		if versioned && schemaVersion == CurrentSchemaVersion {
			strict, strictErr := decodeConfig(payload)
			if strictErr != nil {
				return strictErr
			}
			cfg = strict
			return nil
		}
		legacy, legacyErr := decodeMigratableConfig(payload, schemaVersion, versioned)
		if legacyErr != nil {
			return configErrorf("config.legacy_decode: %w", legacyErr)
		}
		if legacy.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		legacy.SchemaVersion = CurrentSchemaVersion
		legacy.Revision++
		if saveErr := Save(lockedPath, legacy); saveErr != nil {
			return configErrorf("config.legacy_migration_write: %w", saveErr)
		}
		cfg = legacy
		migrated = true
		return nil
	})
	if err != nil {
		return Config{}, false, err
	}
	return cfg, migrated, nil
}

func decodeMigratableConfig(payload []byte, schemaVersion int, versioned bool) (Config, error) {
	if versioned {
		if schemaVersion == 1 {
			return decodeConfigV1(payload)
		}
		return Config{}, markContentError(configErrorf(
			"config.schema_migration_unavailable: have %d support %d",
			schemaVersion, CurrentSchemaVersion,
		))
	}
	// A strict current-shape decode proves an unversioned file contains no field
	// this binary would silently discard before the schema marker is added.
	return decodeConfig(payload)
}

func decodeConfigV1(payload []byte) (Config, error) {
	if err := preflightConfigJSON(payload); err != nil {
		return Config{}, err
	}
	var legacy configV1
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, markContentError(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Config{}, markContentError(err)
	}
	if legacy.SchemaVersion != 1 {
		return Config{}, markContentError(configErrorf(
			"config.schema_migration_unavailable: have %d support %d",
			legacy.SchemaVersion, CurrentSchemaVersion,
		))
	}
	cfg := Config{
		SchemaVersion:    CurrentSchemaVersion,
		Revision:         legacy.Revision,
		Node:             legacy.Node,
		System:           legacy.System,
		NodeInbound:      legacy.NodeInbound,
		Peers:            legacy.Peers,
		XrayProfiles:     legacy.XrayProfiles,
		PeerTrust:        legacy.PeerTrust,
		NodeEgressGrants: map[string]NodeEgressGrant{},
	}
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return Config{}, markContentError(err)
	}
	return cfg, nil
}

func configSchemaFromJSON(payload []byte) (int, bool, error) {
	if err := preflightConfigJSON(payload); err != nil {
		return 0, false, err
	}
	var envelope struct {
		SchemaVersion json.RawMessage `json:"schema_version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil {
		return 0, false, markContentError(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return 0, false, markContentError(err)
	}
	if len(envelope.SchemaVersion) == 0 {
		return 0, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(envelope.SchemaVersion), []byte("null")) {
		return 0, false, markContentError(configErrorf("config.schema_invalid"))
	}
	var version int
	if err := json.Unmarshal(envelope.SchemaVersion, &version); err != nil || version < 1 {
		return 0, false, markContentError(configErrorf("config.schema_invalid"))
	}
	return version, true, nil
}

func load(path string) (Config, error) {
	cfg, err := loadExisting(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}
	return cfg, nil
}

func loadExisting(path string) (Config, error) {
	b, err := readSecureFile(path)
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(b)
}

func decodeConfig(b []byte) (Config, error) {
	if err := preflightConfigJSON(b); err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			err = configErrorf("config.schema_newer_or_unknown: %w", err)
		}
		return Config{}, markContentError(err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Config{}, markContentError(err)
	}
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return Config{}, markContentError(err)
	}
	return cfg, nil
}

// DecodeDocument strictly decodes one complete configuration document. It is
// the shared boundary for path-backed, object-backed, and migration reads.
func DecodeDocument(payload []byte) (Config, error) {
	return decodeConfig(payload)
}

// EncodeDocument normalizes and validates a configuration before producing
// its canonical persisted representation.
func EncodeDocument(cfg Config) ([]byte, error) {
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return configErrorf("config.trailing_json")
		}
		return err
	}
	return nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := EncodeDocument(cfg)
	if err != nil {
		return err
	}

	if previous, err := readSecureFile(path); err == nil {
		if pruneErr := pruneConfigBackups(path, maxConfigBackups-1); pruneErr != nil {
			return configErrorf("config.backup_prune: %w", pruneErr)
		}
		backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405.000000000"))
		if writeErr := writeFileAtomic(backup, previous, 0o600); writeErr != nil {
			return configErrorf("config.backup_write: %w", writeErr)
		}
	} else if !os.IsNotExist(err) {
		return configErrorf("config.backup_read: %w", err)
	}
	if err := writeFileAtomic(path, b, 0o600); err != nil {
		return configErrorf("config.write: %w", err)
	}
	return nil
}

func pruneConfigBackups(path string, keep int) error {
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		return err
	}
	sort.Strings(matches)
	remove := len(matches) - keep
	if remove <= 0 {
		return nil
	}
	for _, backup := range matches[:remove] {
		info, err := os.Lstat(backup)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to prune non-regular backup %s", filepath.Base(backup))
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
	}
	return nil
}

func WithLock(path string, fn func() error) error {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return err
	}
	return withLockedPath(canonical, func(string) error { return fn() })
}

func withLockedPath(path string, fn func(string) error) error {
	return withLockedPathKey(path, path, true, true, fn)
}

func withLockedPathKey(path, ownershipKey string, acquireStable, pinPath bool, fn func(string) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	pinnedPath := path
	closePinned := func() error { return nil }
	var err error
	if pinPath {
		pinnedPath, closePinned, err = pinConfigPath(path)
		if err != nil {
			return configErrorf("config.pin_path: %w", err)
		}
		defer closePinned()
	}
	var stable io.Closer
	if acquireStable {
		stable, err = stablelock.AcquirePathIdentity("config", ownershipKey, pinnedPath)
		if err != nil {
			return configErrorf("config.locked: %w", err)
		}
		defer stable.Close()
	}
	lockPath := pinnedPath + ".lock"
	f, err := openLockFile(lockPath)
	if err != nil {
		return configErrorf("config.lock_open: %w", err)
	}
	defer f.Close()
	if err := lockFileExclusive(f); err != nil {
		return configErrorf("config.locked: %w", err)
	}
	defer unlockFile(f)
	if err := f.Truncate(0); err != nil {
		return configErrorf("config.lock_metadata: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return configErrorf("config.lock_metadata: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		return configErrorf("config.lock_metadata: %w", err)
	}
	if err := f.Sync(); err != nil {
		return configErrorf("config.lock_metadata: %w", err)
	}
	return fn(pinnedPath)
}

// UpdateCAS is the sole read-modify-write transaction for a live config. It
// holds the process-shared file lock across load, revision validation, mutation,
// validation, backup, and atomic publication.
func UpdateCAS(path string, expectedRevision int64, mutate func(*Config) error) (UpdateResult, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return UpdateResult{}, err
	}
	return updateCASWith(canonical, expectedRevision, mutate, LoadExisting, Save)
}

// UpdatePinnedCAS is for a daemon that already holds the lifetime ownership
// lease for canonicalKey and whose configPath is pinned to a parent directory
// handle. It still takes the per-operation file lock and all CAS checks.
func UpdatePinnedCAS(configPath, canonicalKey string, expectedRevision int64, mutate func(*Config) error) (UpdateResult, error) {
	if canonicalKey == "" {
		return UpdateResult{}, configErrorf("config.ownership_key_required")
	}
	return updateCASWithLock(configPath, expectedRevision, mutate, LoadExisting, Save, func(fn func(string) error) error {
		return withLockedPathKey(configPath, canonicalKey, false, false, fn)
	})
}

func updateCASWith(
	path string,
	expectedRevision int64,
	mutate func(*Config) error,
	loadConfig func(string) (Config, error),
	saveConfig func(string, Config) error,
) (UpdateResult, error) {
	return updateCASWithLock(path, expectedRevision, mutate, loadConfig, saveConfig, func(fn func(string) error) error {
		return withLockedPath(path, fn)
	})
}

func updateCASWithLock(
	path string,
	expectedRevision int64,
	mutate func(*Config) error,
	loadConfig func(string) (Config, error),
	saveConfig func(string, Config) error,
	withLock func(func(string) error) error,
) (UpdateResult, error) {
	if expectedRevision < 0 {
		return UpdateResult{}, configErrorf("config.revision_required")
	}
	if mutate == nil {
		return UpdateResult{}, configErrorf("config.mutation_required")
	}
	var result UpdateResult
	err := withLock(func(lockedPath string) error {
		cfg, err := loadConfig(lockedPath)
		if err != nil {
			return err
		}
		result.BeforeRevision = cfg.Revision
		if err := ValidateRevision(cfg, expectedRevision); err != nil {
			return err
		}
		if cfg.Revision == math.MaxInt64 {
			return configErrorf("config.revision_exhausted")
		}
		if err := mutate(&cfg); err != nil {
			return err
		}
		cfg.Revision = result.BeforeRevision + 1
		if err := Validate(cfg); err != nil {
			return err
		}
		if err := saveConfig(lockedPath, cfg); err != nil {
			if errors.Is(err, ErrCommitOutcomeUnknown) {
				observed, loadErr := loadConfig(lockedPath)
				expected := cfg
				normalize(&expected)
				if loadErr == nil && reflect.DeepEqual(observed, expected) {
					if syncErr := syncParentDirectory(filepath.Dir(lockedPath)); syncErr != nil {
						return errors.Join(err, configErrorf("config.commit_confirmation_sync: %w", syncErr))
					}
					result.AfterRevision = cfg.Revision
					return &commitVisibleError{revision: cfg.Revision, cause: err}
				}
				if loadErr == nil {
					loadErr = configErrorf("config.commit_confirmation_mismatch")
				}
				return errors.Join(err, loadErr)
			}
			return err
		}
		result.AfterRevision = cfg.Revision
		return nil
	})
	if err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func Validate(cfg Config) error {
	if cfg.SchemaVersion != CurrentSchemaVersion {
		return configErrorf("config.schema_unsupported: have %d support %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if cfg.Revision < 0 {
		return configErrorf("config.revision_negative")
	}
	if err := settings.Validate(cfg.System); err != nil {
		return err
	}
	if err := validateNodeIdentity(cfg.Node); err != nil {
		return err
	}
	seenProfiles := map[string]bool{}
	for id, p := range cfg.XrayProfiles {
		if id == "" || p.ID == "" || id != p.ID {
			return configErrorf("config.profile_id_mismatch: %s", id)
		}
		if p.Kind == "" {
			return configErrorf("config.profile_kind_required: %s", id)
		}
		if err := validateXrayProfile(p); err != nil {
			return configErrorf("config.profile_invalid: %s: %w", id, err)
		}
		seenProfiles[id] = true
	}
	if err := validateCredentialSeparation(cfg.XrayProfiles); err != nil {
		return err
	}
	if cfg.Node.Role != "" && cfg.Node.Role != "fat" && cfg.Node.Role != "thin" {
		return configErrorf("config.role_invalid: %s", cfg.Node.Role)
	}
	if err := validatePeers(cfg.Peers, cfg.XrayProfiles); err != nil {
		return err
	}
	if err := validateNodeEgressGrants(cfg.NodeEgressGrants, cfg.Peers); err != nil {
		return err
	}
	seenInboundKinds := make(map[string]struct{}, len(cfg.NodeInbound))
	seenInboundListeners := make(map[string]string, len(cfg.NodeInbound))
	for _, in := range cfg.NodeInbound {
		if in.Kind == "" {
			return configErrorf("config.inbound_kind_required")
		}
		if _, duplicate := seenInboundKinds[in.Kind]; duplicate {
			return configErrorf("config.inbound_duplicate: %s", in.Kind)
		}
		seenInboundKinds[in.Kind] = struct{}{}
		if in.Enabled && in.Listen == "" {
			return configErrorf("config.inbound_listen_required: %s", in.Kind)
		}
		if in.XrayProfileID != "" && !seenProfiles[in.XrayProfileID] {
			return configErrorf("config.profile_unknown: %s", in.XrayProfileID)
		}
		if err := validateInbound(in, cfg.XrayProfiles, cfg.Peers); err != nil {
			return err
		}
		if in.Enabled {
			listener, err := canonicalListenEndpoint(in.Listen)
			if err != nil {
				return configErrorf("config.inbound_listen_invalid: %s: %w", in.Kind, err)
			}
			if previous, conflict := seenInboundListeners[listener]; conflict {
				return configErrorf("config.inbound_listen_conflict: %s %s", previous, in.Kind)
			}
			seenInboundListeners[listener] = in.Kind
		}
	}
	return nil
}

func validateNodeIdentity(node NodeConfig) error {
	_, err := identity.ClassifyConfiguredIdentity(node.NodeID, node.PublicKey)
	if err == nil {
		return nil
	}
	if errors.Is(err, identity.ErrUnsupportedIdentitySuite) {
		return configErrorf("config.node_identity_unsupported: %w", err)
	}
	return configErrorf("config.node_identity_invalid: %w", err)
}

func ValidateRevision(cfg Config, want int64) error {
	if want >= 0 && cfg.Revision != want {
		return configErrorf("config.revision_conflict: have %d want %d", cfg.Revision, want)
	}
	return nil
}

func FindPeer(peers []PeerConfig, name string) (PeerConfig, int, bool) {
	for i, p := range peers {
		if p.Name == name || p.NodeID == name {
			return p, i, true
		}
	}
	return PeerConfig{}, -1, false
}

// LocalProfileInUse reports references owned by this node. Observed children
// describe remote topology and never pin a profile from the local store.
func LocalProfileInUse(cfg Config, id string) bool {
	for _, inbound := range cfg.NodeInbound {
		if inbound.XrayProfileID == id {
			return true
		}
	}
	for _, peer := range cfg.Peers {
		if peer.XrayProfileID == id {
			return true
		}
	}
	return false
}

func SortStable(cfg *Config) {
	sort.SliceStable(cfg.Peers, func(i, j int) bool { return cfg.Peers[i].Name < cfg.Peers[j].Name })
	sort.SliceStable(cfg.NodeInbound, func(i, j int) bool { return cfg.NodeInbound[i].Kind < cfg.NodeInbound[j].Kind })
}

func normalize(cfg *Config) {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = CurrentSchemaVersion
	}
	cfg.System = settings.ApplyDefaults(cfg.System)
	if cfg.XrayProfiles == nil {
		cfg.XrayProfiles = map[string]XrayProfile{}
	}
	if cfg.PeerTrust == nil {
		cfg.PeerTrust = map[string]PeerTrustGrant{}
	}
	if cfg.NodeEgressGrants == nil {
		cfg.NodeEgressGrants = map[string]NodeEgressGrant{}
	} else {
		cfg.NodeEgressGrants = cloneNodeEgressGrants(cfg.NodeEgressGrants)
	}
	normalizePeers(cfg.Peers)
	normalizeNodeEgressGrants(cfg.NodeEgressGrants)
	for index := range cfg.NodeInbound {
		if cfg.NodeInbound[index].Purpose == "" {
			switch cfg.NodeInbound[index].Kind {
			case "socks", "http", "mixed":
				cfg.NodeInbound[index].Purpose = "user"
			case "node-vless":
				cfg.NodeInbound[index].Purpose = "node"
			}
		}
	}
	SortStable(cfg)
}

func cloneNodeEgressGrants(grants map[string]NodeEgressGrant) map[string]NodeEgressGrant {
	cloned := make(map[string]NodeEgressGrant, len(grants))
	for key, grant := range grants {
		grant.AllowCIDRs = append([]string(nil), grant.AllowCIDRs...)
		grant.AllowPrivateCIDRs = append([]string(nil), grant.AllowPrivateCIDRs...)
		grant.DenyCIDRs = append([]string(nil), grant.DenyCIDRs...)
		grant.AllowPorts = append([]EgressPortRange(nil), grant.AllowPorts...)
		cloned[key] = grant
	}
	return cloned
}

func normalizeNodeEgressGrants(grants map[string]NodeEgressGrant) {
	for key, grant := range grants {
		sort.Strings(grant.AllowCIDRs)
		sort.Strings(grant.AllowPrivateCIDRs)
		sort.Strings(grant.DenyCIDRs)
		sort.SliceStable(grant.AllowPorts, func(i, j int) bool {
			if grant.AllowPorts[i].From != grant.AllowPorts[j].From {
				return grant.AllowPorts[i].From < grant.AllowPorts[j].From
			}
			return grant.AllowPorts[i].To < grant.AllowPorts[j].To
		})
		grants[key] = grant
	}
}

func validateNodeEgressGrants(grants map[string]NodeEgressGrant, peers []PeerConfig) error {
	directPeers := make(map[string]PeerConfig, len(peers))
	for _, peer := range peers {
		directPeers[peer.NodeID] = peer
	}
	keys := make([]string, 0, len(grants))
	for key := range grants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		grant := grants[key]
		if key == "" || strings.TrimSpace(key) != key || grant.SourceNodeID != key {
			return configErrorf("config.node_egress_grant_source_mismatch: %s", key)
		}
		peer, exists := directPeers[key]
		if !exists {
			return configErrorf("config.node_egress_grant_peer_unknown: %s", key)
		}
		if !peer.Direction.CanAcceptInbound() {
			return configErrorf("config.node_egress_grant_peer_inbound_required: %s", key)
		}
		if grant.Network != "tcp" {
			return configErrorf("config.node_egress_grant_network_invalid: %s", key)
		}
		publicAllowed, err := parseCanonicalGrantPrefixes(key, "allow_cidrs", grant.AllowCIDRs)
		if err != nil {
			return err
		}
		privateAllowed, err := parseCanonicalGrantPrefixes(key, "allow_private_cidrs", grant.AllowPrivateCIDRs)
		if err != nil {
			return err
		}
		denied, err := parseCanonicalGrantPrefixes(key, "deny_cidrs", grant.DenyCIDRs)
		if err != nil {
			return err
		}
		ports := make([]egresspolicy.PortRange, len(grant.AllowPorts))
		for index, portRange := range grant.AllowPorts {
			ports[index] = egresspolicy.PortRange{From: portRange.From, To: portRange.To}
		}
		if _, err := egresspolicy.NewPolicy(publicAllowed, privateAllowed, denied, ports); err != nil {
			return configErrorf("config.node_egress_grant_invalid: %s: %w", key, err)
		}
	}
	return nil
}

func parseCanonicalGrantPrefixes(nodeID, field string, values []string) ([]netip.Prefix, error) {
	prefixes, err := egresspolicy.ParseCanonicalPrefixes(values)
	if errors.Is(err, egresspolicy.ErrDuplicatePrefix) {
		return nil, configErrorf("config.node_egress_grant_cidr_duplicate: %s %s", nodeID, field)
	}
	if err != nil {
		return nil, configErrorf("config.node_egress_grant_cidr_invalid: %s %s", nodeID, field)
	}
	return prefixes, nil
}

func validateXrayProfile(profile XrayProfile) error {
	switch profile.Kind {
	case "vless":
		if profile.VLESS == nil || profile.SOCKS != nil {
			return fmt.Errorf("vless profile shape")
		}
		if _, err := xraycredential.VLESSKey(profile.VLESS.UUID); err != nil {
			return fmt.Errorf("vless credential format: %w", err)
		}
		if profile.VLESS.Transport != "tcp" {
			return fmt.Errorf("vless transport %q unsupported", profile.VLESS.Transport)
		}
		if profile.VLESS.Security != "none" {
			return fmt.Errorf("vless security %q unsupported", profile.VLESS.Security)
		}
		if !profile.VLESS.AllowInsecurePlaintext {
			return fmt.Errorf("plaintext vless requires explicit opt-in")
		}
		if len(profile.Options) != 0 {
			return fmt.Errorf("legacy options cannot accompany a typed vless profile")
		}
	case "socks":
		if profile.SOCKS == nil || profile.VLESS != nil {
			return fmt.Errorf("socks profile shape")
		}
		if (profile.SOCKS.Username == "") != (profile.SOCKS.Password == "") {
			return fmt.Errorf("socks username and password must be set together")
		}
		if len(profile.SOCKS.Username) > 255 || len(profile.SOCKS.Password) > 255 {
			return fmt.Errorf("socks credential exceeds protocol limit")
		}
		if len(profile.Options) != 0 {
			return fmt.Errorf("legacy options cannot accompany a typed socks profile")
		}
	default:
		// Historical profiles remain loadable as reference data, but the runtime
		// compiler rejects every unsupported kind.
	}
	return nil
}

func validateInbound(in InboundConfig, profiles map[string]XrayProfile, peers []PeerConfig) error {
	if in.Purpose != "" && in.Purpose != "user" && in.Purpose != "node" {
		return configErrorf("config.inbound_purpose_invalid: %s", in.Purpose)
	}
	if in.Purpose == "node" && in.XrayProfileID != "" {
		return configErrorf("config.inbound_profile_forbidden: %s", in.Kind)
	}
	if !in.Enabled {
		return nil
	}
	switch in.Purpose {
	case "user":
		profile, hasProfile := profiles[in.XrayProfileID]
		if in.Kind != "socks" {
			return configErrorf("config.inbound_kind_unsupported: %s", in.Kind)
		}
		if err := ValidateIsolatedPlaintextEndpoint(in.Listen); err != nil {
			return configErrorf("config.inbound_plaintext_scope_invalid: %s: %w", in.Kind, err)
		}
		if !hasProfile || profile.Kind != "socks" || profile.SOCKS == nil {
			return configErrorf("config.inbound_profile_incompatible: %s", in.Kind)
		}
		if in.ExitPeer == "" {
			return configErrorf("config.inbound_exit_peer_required: %s", in.Kind)
		}
		exitPeer, _, found := FindPeer(peers, in.ExitPeer)
		if !found || !exitPeer.Enabled || !exitPeer.Direction.CanDialOutbound() {
			return configErrorf("config.inbound_exit_peer_unavailable: %s", in.ExitPeer)
		}
		if profile.SOCKS.Username == "" || profile.SOCKS.Password == "" {
			return configErrorf("config.inbound_auth_required: %s", in.Listen)
		}
	case "node":
		if in.Kind != "node-vless" {
			return configErrorf("config.inbound_kind_unsupported: %s", in.Kind)
		}
		if in.ExitPeer != "" {
			return configErrorf("config.inbound_exit_peer_forbidden: %s", in.Kind)
		}
		if err := ValidateIsolatedPlaintextEndpoint(in.Listen); err != nil {
			return configErrorf("config.inbound_plaintext_scope_invalid: %s: %w", in.Kind, err)
		}
	default:
		return configErrorf("config.inbound_purpose_required: %s", in.Kind)
	}
	return nil
}

func normalizePeers(peers []PeerConfig) {
	for i := range peers {
		if peers[i].DisplayName == "" {
			peers[i].DisplayName = peers[i].Name
		}
	}
}

func validatePeers(peers []PeerConfig, profiles map[string]XrayProfile) error {
	seenNames := map[string]bool{}
	seenNodeIDs := map[string]bool{}
	inboundCredentials := map[string]string{}
	var walk func([]PeerConfig, bool) error
	walk = func(level []PeerConfig, directlyManaged bool) error {
		for _, p := range level {
			if p.Name == "" {
				return configErrorf("config.peer_name_required")
			}
			if p.NodeID == "" {
				return configErrorf("config.peer_node_id_required: %s", p.Name)
			}
			if seenNames[p.Name] {
				return configErrorf("config.peer_duplicate: %s", p.Name)
			}
			if seenNodeIDs[p.NodeID] {
				return configErrorf("config.peer_node_id_duplicate: %s", p.NodeID)
			}
			if seenNodeIDs[p.Name] || seenNames[p.NodeID] || p.Name == p.NodeID {
				return configErrorf("config.peer_identifier_collision: %s", p.Name)
			}
			seenNames[p.Name] = true
			seenNodeIDs[p.NodeID] = true

			if p.Direction != route.DirectionInbound && p.Direction != route.DirectionOutbound && p.Direction != route.DirectionBidirectional {
				return configErrorf("config.peer_direction_invalid: %s", p.Direction)
			}
			if p.Direction.CanDialOutbound() && p.GatewayAddr == "" && p.Addr == "" {
				return configErrorf("config.peer_gateway_required: %s", p.Name)
			}
			if directlyManaged {
				profile, hasProfile := profiles[p.XrayProfileID]
				if p.XrayProfileID != "" && !hasProfile {
					return configErrorf("config.profile_unknown: %s", p.XrayProfileID)
				}
				if p.Enabled && p.Direction.CanDialOutbound() && (!hasProfile || profile.Kind != "vless" || profile.VLESS == nil) {
					return configErrorf("config.peer_profile_incompatible: %s", p.Name)
				}
				if p.Enabled && p.Direction.CanAcceptInbound() && (!hasProfile || profile.Kind != "vless" || profile.VLESS == nil) {
					return configErrorf("config.peer_inbound_profile_incompatible: %s", p.Name)
				}
				if p.Enabled && p.Direction.CanAcceptInbound() && hasProfile && profile.VLESS != nil {
					credentialKey, err := xraycredential.VLESSKey(profile.VLESS.UUID)
					if err != nil {
						return configErrorf("config.peer_inbound_credential_invalid: %s", p.Name)
					}
					if previous := inboundCredentials[credentialKey]; previous != "" && previous != p.NodeID {
						return configErrorf("config.peer_inbound_credential_duplicate: %s %s", previous, p.NodeID)
					}
					inboundCredentials[credentialKey] = p.NodeID
				}
				if p.Enabled && p.Direction.CanDialOutbound() && hasProfile && profile.Kind == "vless" && profile.VLESS != nil {
					address := p.GatewayAddr
					if address == "" {
						address = p.Addr
					}
					if err := ValidateIsolatedPlaintextEndpoint(address); err != nil {
						return configErrorf("config.peer_plaintext_scope_invalid: %s: %w", p.Name, err)
					}
				}
			}
			if err := walk(p.Children, false); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(peers, true)
}

// ValidateIsolatedPlaintextEndpoint confines the initial VLESS/TCP plaintext
// carrier to explicitly isolated networks. Public deployment requires a
// cryptographically protected Xray transport, which is not implemented yet.
func ValidateIsolatedPlaintextEndpoint(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return configErrorf("config.plaintext_endpoint_invalid: host:port required")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return configErrorf("config.plaintext_endpoint_invalid: port is out of range")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return configErrorf("config.plaintext_endpoint_ip_literal_required")
	}
	if ip.IsUnspecified() {
		return configErrorf("config.plaintext_endpoint_wildcard_forbidden")
	}
	if !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		return configErrorf("config.plaintext_endpoint_private_ip_required")
	}
	return nil
}

func canonicalListenEndpoint(address string) (string, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return "", fmt.Errorf("host:port required")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("IP literal required")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return "", fmt.Errorf("port is out of range")
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)), nil
}

func validateCredentialSeparation(profiles map[string]XrayProfile) error {
	vlessCredentials := make(map[string]string)
	for id, profile := range profiles {
		if profile.Kind == "vless" && profile.VLESS != nil {
			vlessCredentials[credentialSeparationKey(profile.VLESS.UUID)] = id
		}
	}
	for id, profile := range profiles {
		if profile.Kind != "socks" || profile.SOCKS == nil {
			continue
		}
		for _, candidate := range []struct {
			field string
			value string
		}{
			{field: "username", value: profile.SOCKS.Username},
			{field: "password", value: profile.SOCKS.Password},
		} {
			if candidate.value == "" {
				continue
			}
			if vlessID, reused := vlessCredentials[credentialSeparationKey(candidate.value)]; reused {
				return configErrorf(
					"config.credential_reuse_forbidden: socks=%s field=%s vless=%s",
					id,
					candidate.field,
					vlessID,
				)
			}
		}
	}
	return nil
}

func credentialSeparationKey(value string) string {
	if key, err := xraycredential.VLESSLookupKey(value); err == nil {
		return "vless:" + key
	}
	return "raw:" + value
}

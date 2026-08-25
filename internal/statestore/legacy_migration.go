package statestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	legacyMigrationVersion      = 1
	legacyMigrationIntentKind   = "xtier-legacy-state-migration-intent"
	legacyMigrationCompleteKind = "xtier-legacy-state-migration-complete"
	legacyMigrationIntentName   = "legacy-migration.v1.intent.json"
	legacyMigrationCompleteName = "legacy-migration.v1.complete.json"

	maxLegacyTokenSize  = 64 << 10
	maxLegacyConfigSize = 16 << 20
	maxLegacySeedSize   = 4096
	maxLegacyIntentSize = 1 << 20
)

var (
	ErrLegacyMigrationConflict = errors.New("legacy state migration conflict")
	ErrInvalidLegacyState      = errors.New("invalid legacy state")
)

// LegacyMigrationOptions supplies the application-level checks that the state
// store cannot perform itself. Validators receive a copy of the exact bytes
// that will be published.
type LegacyMigrationOptions struct {
	// Identity is the identity asserted by the already-loaded active config or
	// v2 checkpoint. A shared legacy seed is ignored unless this assertion is
	// present and its validator proves ownership.
	Identity string

	ValidateIdentitySeed func(identity string, payload []byte) error
	IsConfigDocument     func([]byte) bool
}

// LegacyMigrationResult reports durable migration progress for this call.
type LegacyMigrationResult struct {
	Sources         int
	Published       int
	AlreadyComplete bool
}

type legacyMigrationIntent struct {
	Version   int                    `json:"version"`
	Kind      string                 `json:"kind"`
	ConfigKey string                 `json:"config_key"`
	Identity  string                 `json:"identity,omitempty"`
	Entries   []legacyMigrationEntry `json:"entries"`
}

type legacyMigrationEntry struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Target string `json:"target"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type legacyMigrationComplete struct {
	Version      int    `json:"version"`
	Kind         string `json:"kind"`
	ConfigKey    string `json:"config_key"`
	IntentSHA256 string `json:"intent_sha256"`
}

type legacyMigrationPlan struct {
	intent   legacyMigrationIntent
	payloads map[string][]byte
}

// MigrateLegacy copies only legacy token and identity objects whose ownership
// can be proven. Ambiguous checkpoints and backups remain untouched. Legacy
// files are never changed or removed. Callers must serialize this operation
// with ordinary config ownership and file locks.
func (s *Store) MigrateLegacy(options LegacyMigrationOptions) (LegacyMigrationResult, error) {
	if s == nil || s.parent == nil || s.state == nil {
		return LegacyMigrationResult{}, fmt.Errorf("statestore.closed")
	}

	existingIntent, intentErr := readFromRoot(s.state, legacyMigrationIntentName, maxLegacyIntentSize)
	existingComplete, completeErr := readFromRoot(s.state, legacyMigrationCompleteName, maxManifestSize)
	if completeErr != nil && !errors.Is(completeErr, fs.ErrNotExist) {
		return LegacyMigrationResult{}, fmt.Errorf("statestore.legacy_complete_read: %w", completeErr)
	}
	if intentErr != nil && !errors.Is(intentErr, fs.ErrNotExist) {
		return LegacyMigrationResult{}, fmt.Errorf("statestore.legacy_intent_read: %w", intentErr)
	}
	if completeErr == nil {
		if intentErr != nil {
			return LegacyMigrationResult{}, fmt.Errorf("%w: completion receipt exists without an intent", ErrLegacyMigrationConflict)
		}
		intent, _, err := decodeLegacyMigrationReceipts(existingIntent, existingComplete, s.configKey, s.configLeaf)
		if err != nil {
			return LegacyMigrationResult{}, err
		}
		current, err := s.buildLegacyMigrationPlan(options)
		if err != nil {
			return LegacyMigrationResult{}, fmt.Errorf(
				"%w: legacy state changed after v2 migration: %w",
				ErrLegacyMigrationConflict,
				err,
			)
		}
		if err := validateCompletedLegacySources(intent, current.intent); err != nil {
			return LegacyMigrationResult{}, err
		}
		published, err := s.repairCompletedLegacySeed(intent, current)
		if err != nil {
			return LegacyMigrationResult{}, err
		}
		return LegacyMigrationResult{
			Sources: len(intent.Entries), Published: published, AlreadyComplete: true,
		}, nil
	}

	plan, err := s.buildLegacyMigrationPlan(options)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	result := LegacyMigrationResult{Sources: len(plan.intent.Entries)}

	intentPayload, err := json.Marshal(plan.intent)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("statestore.legacy_intent_encode: %w", err)
	}
	intentPayload = append(intentPayload, '\n')
	if len(intentPayload) > maxLegacyIntentSize {
		return result, fmt.Errorf("%w: migration intent is too large", ErrInvalidLegacyState)
	}

	if intentErr == nil {
		if !bytes.Equal(existingIntent, intentPayload) {
			return result, fmt.Errorf("%w: legacy sources do not match the published intent", ErrLegacyMigrationConflict)
		}
	} else {
		if completeErr == nil {
			return result, fmt.Errorf("%w: completion receipt exists without an intent", ErrLegacyMigrationConflict)
		}
		if err := createExclusiveInRoot(s.state, s.stateDir, legacyMigrationIntentName, intentPayload); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return result, fmt.Errorf("statestore.legacy_intent_create: %w", err)
			}
			published, readErr := readFromRoot(s.state, legacyMigrationIntentName, maxLegacyIntentSize)
			if readErr != nil || !bytes.Equal(published, intentPayload) {
				return result, fmt.Errorf("%w: concurrently published intent differs", ErrLegacyMigrationConflict)
			}
		}
	}

	intentDigest := sha256.Sum256(intentPayload)
	complete := legacyMigrationComplete{
		Version:      legacyMigrationVersion,
		Kind:         legacyMigrationCompleteKind,
		ConfigKey:    s.configKey,
		IntentSHA256: hex.EncodeToString(intentDigest[:]),
	}
	completePayload, err := json.Marshal(complete)
	if err != nil {
		return result, fmt.Errorf("statestore.legacy_complete_encode: %w", err)
	}
	completePayload = append(completePayload, '\n')

	for _, entry := range plan.intent.Entries {
		payload := plan.payloads[entry.Source]
		published, publishErr := s.publishLegacyEntry(entry, payload)
		if publishErr != nil {
			return result, publishErr
		}
		if published {
			result.Published++
		}
	}

	if err := createExclusiveInRoot(s.state, s.stateDir, legacyMigrationCompleteName, completePayload); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return result, fmt.Errorf("statestore.legacy_complete_create: %w", err)
		}
		published, readErr := readFromRoot(s.state, legacyMigrationCompleteName, maxManifestSize)
		if readErr != nil || !bytes.Equal(published, completePayload) {
			return result, fmt.Errorf("%w: concurrently published completion differs", ErrLegacyMigrationConflict)
		}
	}
	return result, nil
}

// LegacyMigrationReceiptPaths exposes diagnostic paths without adding
// migration-only values to Object. Runtime I/O must continue through Store.
func (s *Store) LegacyMigrationReceiptPaths() (intent, complete string, err error) {
	if s == nil || s.state == nil {
		return "", "", fmt.Errorf("statestore.closed")
	}
	return filepath.Join(s.statePath, legacyMigrationIntentName),
		filepath.Join(s.statePath, legacyMigrationCompleteName), nil
}

func (s *Store) buildLegacyMigrationPlan(options LegacyMigrationOptions) (legacyMigrationPlan, error) {
	plan := legacyMigrationPlan{
		intent: legacyMigrationIntent{
			Version:   legacyMigrationVersion,
			Kind:      legacyMigrationIntentKind,
			ConfigKey: s.configKey,
			Entries:   make([]legacyMigrationEntry, 0, 3),
		},
		payloads: make(map[string][]byte),
	}
	if options.Identity != "" {
		if options.Identity != strings.TrimSpace(options.Identity) {
			return legacyMigrationPlan{}, fmt.Errorf("%w: active identity is not canonical", ErrInvalidLegacyState)
		}
	}
	add := func(kind, source, target string, payload []byte) {
		digest := sha256.Sum256(payload)
		plan.intent.Entries = append(plan.intent.Entries, legacyMigrationEntry{
			Kind: kind, Source: source, Target: target, Size: int64(len(payload)),
			SHA256: hex.EncodeToString(digest[:]),
		})
		plan.payloads[source] = payload
	}

	for _, candidate := range []struct {
		kind   string
		suffix string
		object Object
		limit  int64
	}{
		{kind: "control-token", suffix: ".control-token", object: ControlToken, limit: maxLegacyTokenSize},
		{kind: "web-token", suffix: ".web-token", object: WebToken, limit: maxLegacyTokenSize},
	} {
		source := s.configLeaf + candidate.suffix
		payload, readErr := readFromRoot(s.parent, source, candidate.limit)
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return legacyMigrationPlan{}, fmt.Errorf("statestore.legacy_source_read %q: %w", source, readErr)
		}
		if !validLegacyToken(payload) {
			if options.IsConfigDocument != nil && options.IsConfigDocument(bytes.Clone(payload)) {
				continue
			}
			return legacyMigrationPlan{}, fmt.Errorf("%w: token %q is malformed", ErrInvalidLegacyState, source)
		}
		target, _ := objectName(candidate.object)
		add(candidate.kind, source, target, payload)
	}

	seed, present, err := s.readLegacySeed()
	if err != nil {
		return legacyMigrationPlan{}, err
	}
	if present && options.Identity != "" {
		if options.ValidateIdentitySeed == nil {
			return legacyMigrationPlan{}, fmt.Errorf("%w: identity-seed validator is required", ErrInvalidLegacyState)
		}
		if err := options.ValidateIdentitySeed(options.Identity, bytes.Clone(seed)); err != nil {
			return legacyMigrationPlan{}, fmt.Errorf("%w: shared identity seed: %w", ErrInvalidLegacyState, err)
		}
		plan.intent.Identity = options.Identity
		target, _ := objectName(IdentitySeed)
		add("identity-seed", "keystore/node-seed.v1.json", target, seed)
	}

	sort.Slice(plan.intent.Entries, func(i, j int) bool {
		return plan.intent.Entries[i].Source < plan.intent.Entries[j].Source
	})
	return plan, nil
}

func validateCompletedLegacySources(intent, current legacyMigrationIntent) error {
	want := make(map[string]legacyMigrationEntry, len(intent.Entries))
	for _, entry := range intent.Entries {
		want[entry.Source] = entry
	}
	for _, entry := range current.Entries {
		previous, ok := want[entry.Source]
		if !ok || previous != entry {
			return fmt.Errorf(
				"%w: legacy source %q was created or changed after v2 migration",
				ErrLegacyMigrationConflict,
				entry.Source,
			)
		}
	}
	return nil
}

func (s *Store) repairCompletedLegacySeed(intent legacyMigrationIntent, current legacyMigrationPlan) (int, error) {
	var seedEntry *legacyMigrationEntry
	for index := range intent.Entries {
		if intent.Entries[index].Kind == "identity-seed" {
			seedEntry = &intent.Entries[index]
			break
		}
	}
	if seedEntry == nil {
		return 0, nil
	}
	if _, err := s.Read(IdentitySeed, maxLegacySeedSize); err == nil {
		return 0, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("statestore.legacy_seed_target_read: %w", err)
	}
	payload, ok := current.payloads[seedEntry.Source]
	if !ok {
		return 0, fmt.Errorf(
			"%w: migrated identity seed is missing and its verified legacy source is unavailable",
			ErrLegacyMigrationConflict,
		)
	}
	published, err := s.publishLegacyEntry(*seedEntry, payload)
	if err != nil {
		return 0, err
	}
	if published {
		return 1, nil
	}
	return 0, nil
}

func validLegacyToken(payload []byte) bool {
	token := strings.TrimSpace(string(payload))
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == 32
}

func (s *Store) readLegacySeed() ([]byte, bool, error) {
	info, err := s.parent.Lstat("keystore")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("statestore.legacy_seed_directory_inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("%w: legacy keystore is not a real directory", ErrInsecureState)
	}
	root, err := s.parent.OpenRoot("keystore")
	if err != nil {
		return nil, false, fmt.Errorf("statestore.legacy_seed_directory_open: %w", err)
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf("%w: legacy keystore changed while opening", ErrInsecureState)
	}
	if err := secureLegacyIdentityRootDirectory(root, filepath.Join(filepath.Dir(s.configPath), "keystore")); err != nil {
		return nil, false, fmt.Errorf("statestore.legacy_seed_directory_secure: %w", err)
	}
	current, err := s.parent.Lstat("keystore")
	if err != nil || !os.SameFile(current, opened) {
		return nil, false, fmt.Errorf("%w: legacy keystore was rebound", ErrInsecureState)
	}
	file, err := openLegacyIdentityRootFile(root, "node-seed.v1.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("statestore.legacy_seed_read: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxLegacySeedSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("statestore.legacy_seed_read: %w", err)
	}
	if len(payload) > maxLegacySeedSize {
		return nil, false, fmt.Errorf("%w: legacy identity seed is too large", ErrInvalidLegacyState)
	}
	return payload, true, nil
}

func (s *Store) publishLegacyEntry(entry legacyMigrationEntry, payload []byte) (bool, error) {
	if err := createExclusiveInRoot(s.state, s.stateDir, entry.Target, payload); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrExist) {
		return false, fmt.Errorf("statestore.legacy_target_create %q: %w", entry.Target, err)
	}
	existing, err := readFromRoot(s.state, entry.Target, maxLegacyConfigSize)
	if err != nil {
		return false, fmt.Errorf("statestore.legacy_target_read %q: %w", entry.Target, err)
	}
	if !bytes.Equal(existing, payload) {
		return false, fmt.Errorf("%w: legacy source %q differs from v2 target %q", ErrLegacyMigrationConflict, entry.Source, entry.Target)
	}
	return false, nil
}

func decodeLegacyMigrationReceipts(intentPayload, completePayload []byte, configKey, configLeaf string) (legacyMigrationIntent, legacyMigrationComplete, error) {
	var intent legacyMigrationIntent
	if err := decodeStrictLegacyReceipt(intentPayload, &intent); err != nil {
		return intent, legacyMigrationComplete{}, fmt.Errorf("%w: invalid migration intent: %v", ErrLegacyMigrationConflict, err)
	}
	var complete legacyMigrationComplete
	if err := decodeStrictLegacyReceipt(completePayload, &complete); err != nil {
		return intent, complete, fmt.Errorf("%w: invalid completion receipt: %v", ErrLegacyMigrationConflict, err)
	}
	if intent.Version != legacyMigrationVersion || intent.Kind != legacyMigrationIntentKind ||
		intent.ConfigKey != configKey || intent.Entries == nil {
		return intent, complete, fmt.Errorf("%w: migration intent identity is invalid", ErrLegacyMigrationConflict)
	}
	digest := sha256.Sum256(intentPayload)
	if complete.Version != legacyMigrationVersion || complete.Kind != legacyMigrationCompleteKind ||
		complete.ConfigKey != configKey || complete.IntentSHA256 != hex.EncodeToString(digest[:]) {
		return intent, complete, fmt.Errorf("%w: completion receipt does not match intent", ErrLegacyMigrationConflict)
	}
	seenSource := make(map[string]struct{}, len(intent.Entries))
	seenTarget := make(map[string]struct{}, len(intent.Entries))
	seedEntries := 0
	for _, entry := range intent.Entries {
		if entry.Source == "" || entry.Target == "" || entry.Size <= 0 || entry.Size > maxLegacyConfigSize ||
			!validLegacyReceiptEntry(entry, configLeaf) {
			return intent, complete, fmt.Errorf("%w: migration intent entry is invalid", ErrLegacyMigrationConflict)
		}
		decoded, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(decoded) != sha256.Size {
			return intent, complete, fmt.Errorf("%w: migration intent digest is invalid", ErrLegacyMigrationConflict)
		}
		if _, exists := seenSource[entry.Source]; exists {
			return intent, complete, fmt.Errorf("%w: migration intent source is duplicated", ErrLegacyMigrationConflict)
		}
		if _, exists := seenTarget[entry.Target]; exists {
			return intent, complete, fmt.Errorf("%w: migration intent target is duplicated", ErrLegacyMigrationConflict)
		}
		seenSource[entry.Source] = struct{}{}
		seenTarget[entry.Target] = struct{}{}
		if entry.Kind == "identity-seed" {
			seedEntries++
		}
	}
	if (seedEntries == 1) != (intent.Identity != "") || seedEntries > 1 ||
		intent.Identity != strings.TrimSpace(intent.Identity) {
		return intent, complete, fmt.Errorf("%w: migration identity assertion is invalid", ErrLegacyMigrationConflict)
	}
	return intent, complete, nil
}

func decodeStrictLegacyReceipt(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func validLegacyReceiptEntry(entry legacyMigrationEntry, configLeaf string) bool {
	var object Object
	var source string
	switch entry.Kind {
	case "control-token":
		object = ControlToken
		source = configLeaf + ".control-token"
	case "web-token":
		object = WebToken
		source = configLeaf + ".web-token"
	case "identity-seed":
		object = IdentitySeed
		source = "keystore/node-seed.v1.json"
	default:
		return false
	}
	want, _ := objectName(object)
	return entry.Target == want && entry.Source == source
}

package statestore

import (
	"bytes"
	"crypto/rand"
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
	"sync"
	"time"
)

const (
	DirectoryName = ".xtier-state"
	LayoutVersion = "v2"

	manifestVersion = 2
	manifestKind    = "xtier-private-state"
	maxManifestSize = 4096
	tempAttempts    = 10_000
)

type Object uint8

const (
	ConfigLock Object = iota + 1
	DaemonLock
	ControlToken
	WebToken
	IdentitySeed
	LastKnownGood
	PeerCredentialQuarantineLedger
	ConfigRevisionHighWater
	RejectedConfig
	PreMigrationConfig
)

var (
	ErrInsecureState        = errors.New("insecure private state")
	ErrCommitOutcomeUnknown = errors.New("private state commit outcome unknown")
)

type Store struct {
	configPath string
	configLeaf string
	configName string
	statePath  string
	configKey  string

	parent    *os.Root
	state     *os.Root
	parentDir *os.File
	stateDir  *os.File

	closeOnce sync.Once
	closeErr  error
}

type manifest struct {
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	ConfigKey string `json:"config_key"`
	StoreID   string `json:"store_id"`
}

// Open binds a private state store to an already-canonical absolute config
// path. The returned store keeps the config parent and private state directory
// objects open, so later path renames do not redirect its operations.
func Open(configPath string) (*Store, error) {
	if configPath == "" || !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return nil, fmt.Errorf("statestore.config_path_not_canonical")
	}
	if err := rejectReservedPath(configPath); err != nil {
		return nil, err
	}

	parentPath := filepath.Dir(configPath)
	parent, err := openPinnedRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("statestore.parent_open: %w", err)
	}
	opened := []*os.Root{parent}
	closeOpened := func() {
		for index := len(opened) - 1; index >= 0; index-- {
			_ = opened[index].Close()
		}
	}

	configName, err := normalizedConfigName(parent, filepath.Base(configPath))
	if err != nil {
		closeOpened()
		return nil, fmt.Errorf("statestore.config_name: %w", err)
	}
	digest := sha256.Sum256([]byte("xtier/statestore/v2\x00" + configName))
	configKey := hex.EncodeToString(digest[:])

	root := parent
	rootPath := parentPath
	for _, component := range []string{DirectoryName, LayoutVersion, configKey} {
		rootPath = filepath.Join(rootPath, component)
		child, childErr := ensureDirectory(root, component, rootPath)
		if childErr != nil {
			closeOpened()
			return nil, childErr
		}
		opened = append(opened, child)
		root = child
	}

	store := &Store{
		configPath: configPath,
		configLeaf: filepath.Base(configPath),
		configName: configName,
		statePath:  rootPath,
		configKey:  configKey,
		parent:     parent,
		state:      root,
	}
	parentDir, err := parent.Open(".")
	if err != nil {
		closeOpened()
		return nil, fmt.Errorf("statestore.parent_handle_open: %w", err)
	}
	store.parentDir = parentDir
	stateDir, err := root.Open(".")
	if err != nil {
		_ = parentDir.Close()
		closeOpened()
		return nil, fmt.Errorf("statestore.state_handle_open: %w", err)
	}
	store.stateDir = stateDir
	if err := store.ensureManifest(); err != nil {
		_ = stateDir.Close()
		_ = parentDir.Close()
		closeOpened()
		return nil, err
	}
	// Store owns parent and the final state root. Intermediate roots can close;
	// the final root remains bound to the same directory object.
	for index := 1; index < len(opened)-1; index++ {
		if err := opened[index].Close(); err != nil {
			_ = stateDir.Close()
			_ = parentDir.Close()
			closeOpened()
			return nil, fmt.Errorf("statestore.intermediate_close: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = errors.Join(s.stateDir.Close(), s.parentDir.Close(), s.state.Close(), s.parent.Close())
	})
	return s.closeErr
}

func (s *Store) ConfigPath() string { return s.configPath }
func (s *Store) ConfigName() string { return s.configName }
func (s *Store) ConfigKey() string  { return s.configKey }

// StableIdentityKey identifies the config's parent directory object and leaf
// using the same handle that backs all Store config operations.
func (s *Store) StableIdentityKey() (string, error) {
	if s == nil || s.parentDir == nil {
		return "", fmt.Errorf("statestore.closed")
	}
	return stableIdentityKey(s.parentDir, s.configLeaf)
}

// DiagnosticPath is for operator output and compatibility tests only. Runtime
// I/O must use Store methods because this path can be rebound after Open.
func (s *Store) DiagnosticPath(object Object) (string, error) {
	name, err := objectName(object)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.statePath, name), nil
}

func (s *Store) Read(object Object, limit int64) ([]byte, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("statestore.read_limit_invalid")
	}
	name, err := objectName(object)
	if err != nil {
		return nil, err
	}
	return readFromRoot(s.state, name, limit)
}

func (s *Store) ReadConfig(limit int64) ([]byte, error) {
	if s == nil || s.parent == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("statestore.read_limit_invalid")
	}
	return readFromRoot(s.parent, s.configLeaf, limit)
}

func (s *Store) CreateConfigExclusive(data []byte) error {
	if s == nil || s.parent == nil {
		return fmt.Errorf("statestore.closed")
	}
	return createExclusiveInRoot(s.parent, s.parentDir, s.configLeaf, data)
}

func (s *Store) ReplaceConfig(data []byte) error {
	if s == nil || s.parent == nil {
		return fmt.Errorf("statestore.closed")
	}
	return replaceInRoot(s.parent, s.parentDir, s.configLeaf, data)
}

func (s *Store) CreateExclusive(object Object, data []byte) error {
	if s == nil || s.state == nil {
		return fmt.Errorf("statestore.closed")
	}
	name, err := objectName(object)
	if err != nil {
		return err
	}
	return createExclusiveInRoot(s.state, s.stateDir, name, data)
}

func (s *Store) Replace(object Object, data []byte) error {
	if s == nil || s.state == nil {
		return fmt.Errorf("statestore.closed")
	}
	name, err := objectName(object)
	if err != nil {
		return err
	}
	return replaceInRoot(s.state, s.stateDir, name, data)
}

func (s *Store) OpenLock(object Object) (*os.File, error) {
	if object != ConfigLock && object != DaemonLock {
		return nil, fmt.Errorf("statestore.object_not_lock")
	}
	name, _ := objectName(object)
	file, err := openSecureRootFile(s.state, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		file, err = openSecureRootFile(s.state, name, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("statestore.lock_open: %w", err)
	}
	return file, nil
}

func (s *Store) SyncConfigParent() error {
	if s == nil || s.parentDir == nil {
		return fmt.Errorf("statestore.closed")
	}
	return syncDirectory(s.parentDir)
}

func (s *Store) CreateBackup(data []byte) (string, error) {
	if s == nil || s.state == nil {
		return "", fmt.Errorf("statestore.closed")
	}
	for range tempAttempts {
		name := "backup." + time.Now().UTC().Format("20060102T150405.000000000") + "." + rand.Text() + ".json"
		if err := createExclusiveInRoot(s.state, s.stateDir, name, data); err == nil {
			return name, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fs.ErrExist
}

func (s *Store) BackupNames() ([]string, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	entries, err := fs.ReadDir(s.state.FS(), ".")
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !validBackupName(name) {
			continue
		}
		file, err := openSecureRootFile(s.state, name, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

// ReadBackup reads one store-owned backup selected from BackupNames. Runtime
// callers must not turn the diagnostic state path back into an authority.
func (s *Store) ReadBackup(name string, limit int64) ([]byte, error) {
	if s == nil || s.state == nil {
		return nil, fmt.Errorf("statestore.closed")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("statestore.read_limit_invalid")
	}
	if !validBackupName(name) {
		return nil, fmt.Errorf("statestore.backup_name_invalid")
	}
	return readFromRoot(s.state, name, limit)
}

// PruneBackups must be called only after the corresponding config publication
// succeeds and while the caller holds the config lock.
func (s *Store) PruneBackups(keep int) error {
	if keep < 0 {
		return fmt.Errorf("statestore.backup_keep_invalid")
	}
	names, err := s.BackupNames()
	if err != nil {
		return err
	}
	remove := len(names) - keep
	if remove <= 0 {
		return nil
	}
	for _, name := range names[:remove] {
		if err := s.state.Remove(name); err != nil {
			return err
		}
	}
	return syncDirectory(s.stateDir)
}

// RecoverTemporaryFiles removes only statestore-owned unpublished files. The
// caller must hold the process-wide config ownership and config file lock.
func (s *Store) RecoverTemporaryFiles() error {
	if s == nil || s.state == nil {
		return fmt.Errorf("statestore.closed")
	}
	entries, err := fs.ReadDir(s.state.FS(), ".")
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".tmp-") {
			continue
		}
		file, err := openSecureRootFile(s.state, name, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := s.state.Remove(name); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(s.stateDir)
	}
	return nil
}

func readFromRoot(root *os.Root, name string, limit int64) ([]byte, error) {
	file, err := openSecureRootFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("statestore.object_too_large")
	}
	return payload, nil
}

func createExclusiveInRoot(root *os.Root, directory *os.File, name string, data []byte) (err error) {
	if info, statErr := root.Lstat(name); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: target %q is not a regular file", ErrInsecureState, name)
		}
		existing, openErr := openSecureRootFile(root, name, os.O_RDONLY, 0)
		if openErr != nil {
			return openErr
		}
		if closeErr := existing.Close(); closeErr != nil {
			return closeErr
		}
		return fs.ErrExist
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	temporary, temporaryName, err := writeTemporary(root, name, data)
	if err != nil {
		return err
	}
	defer func() {
		if temporary != nil {
			if closeErr := temporary.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if removeErr := root.Remove(temporaryName); err == nil && removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = removeErr
		}
	}()
	if err = temporary.Close(); err != nil {
		return err
	}
	temporary = nil
	if err = publishExclusive(root, directory, temporaryName, name); err != nil {
		return err
	}
	if err = syncDirectory(directory); err != nil {
		return fmt.Errorf("%w: statestore.publish_sync: %v", ErrCommitOutcomeUnknown, err)
	}
	return nil
}

func replaceInRoot(root *os.Root, directory *os.File, name string, data []byte) (err error) {
	return replaceInRootWithSync(root, directory, name, data, syncDirectory)
}

func replaceInRootWithSync(
	root *os.Root,
	directory *os.File,
	name string,
	data []byte,
	syncDirectoryFn func(*os.File) error,
) (err error) {
	if info, statErr := root.Lstat(name); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: target is not a regular file", ErrInsecureState)
		}
		existing, openErr := openSecureRootFile(root, name, os.O_RDONLY, 0)
		if openErr != nil {
			return openErr
		}
		if closeErr := existing.Close(); closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	temporary, temporaryName, err := writeTemporary(root, name, data)
	if err != nil {
		return err
	}
	defer func() {
		if temporary != nil {
			if closeErr := temporary.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		if removeErr := root.Remove(temporaryName); err == nil && removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = removeErr
		}
	}()
	if err = temporary.Close(); err != nil {
		return err
	}
	temporary = nil
	if err = replaceRootFile(root, directory, temporaryName, name); err != nil {
		return err
	}
	if err = syncDirectoryFn(directory); err != nil {
		return fmt.Errorf("%w: statestore.replace_sync: %v", ErrCommitOutcomeUnknown, err)
	}
	return nil
}

func writeTemporary(root *os.Root, target string, data []byte) (*os.File, string, error) {
	for range tempAttempts {
		name := ".tmp-" + target + "-" + rand.Text()
		file, err := openSecureRootFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return nil, "", err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fs.ErrExist
}

func (s *Store) ensureManifest() error {
	payload, err := readFromRoot(s.state, "manifest.v2.json", maxManifestSize)
	if errors.Is(err, fs.ErrNotExist) {
		var storeID [16]byte
		if _, randomErr := rand.Read(storeID[:]); randomErr != nil {
			return fmt.Errorf("statestore.manifest_random: %w", randomErr)
		}
		created := manifest{
			Version:   manifestVersion,
			Kind:      manifestKind,
			ConfigKey: s.configKey,
			StoreID:   hex.EncodeToString(storeID[:]),
		}
		encoded, encodeErr := json.Marshal(created)
		if encodeErr != nil {
			return encodeErr
		}
		encoded = append(encoded, '\n')
		if createErr := createExclusiveInRoot(s.state, s.stateDir, "manifest.v2.json", encoded); createErr != nil {
			if !errors.Is(createErr, fs.ErrExist) {
				return fmt.Errorf("statestore.manifest_create: %w", createErr)
			}
			payload, err = readFromRoot(s.state, "manifest.v2.json", maxManifestSize)
		} else {
			payload = encoded
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("statestore.manifest_read: %w", err)
	}
	observed, err := decodeManifest(payload)
	if err != nil {
		return fmt.Errorf("statestore.manifest_invalid: %w", err)
	}
	if observed.Version != manifestVersion || observed.Kind != manifestKind ||
		observed.ConfigKey != s.configKey || len(observed.StoreID) != 32 {
		return fmt.Errorf("statestore.manifest_mismatch")
	}
	if _, err := hex.DecodeString(observed.StoreID); err != nil {
		return fmt.Errorf("statestore.manifest_store_id: %w", err)
	}
	return nil
}

func decodeManifest(payload []byte) (manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var value manifest
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return manifest{}, fmt.Errorf("object required")
	}
	seen := make(map[string]struct{}, 4)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return manifest{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return manifest{}, fmt.Errorf("field name required")
		}
		if _, duplicate := seen[key]; duplicate {
			return manifest{}, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "version":
			err = decoder.Decode(&value.Version)
		case "kind":
			err = decoder.Decode(&value.Kind)
		case "config_key":
			err = decoder.Decode(&value.ConfigKey)
		case "store_id":
			err = decoder.Decode(&value.StoreID)
		default:
			return manifest{}, fmt.Errorf("unknown field %q", key)
		}
		if err != nil {
			return manifest{}, err
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return manifest{}, fmt.Errorf("unterminated object")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return manifest{}, fmt.Errorf("trailing JSON")
		}
		return manifest{}, err
	}
	if len(seen) != 4 {
		return manifest{}, fmt.Errorf("required field missing")
	}
	return value, nil
}

func ensureDirectory(parent *os.Root, name, diagnosticPath string) (*os.Root, error) {
	created := false
	info, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := createSecureRootDirectory(parent, name); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return nil, fmt.Errorf("statestore.directory_create: %w", err)
			}
		} else {
			created = true
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, fmt.Errorf("statestore.directory_inspect: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: state component %q is not a real directory", ErrInsecureState, name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("statestore.directory_open: %w", err)
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, fmt.Errorf("%w: state component %q changed while opening", ErrInsecureState, name)
	}
	if err := secureRootDirectory(child, diagnosticPath, created); err != nil {
		_ = child.Close()
		return nil, fmt.Errorf("statestore.directory_secure: %w", err)
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(current, opened) {
		_ = child.Close()
		return nil, fmt.Errorf("%w: state component %q was rebound", ErrInsecureState, name)
	}
	if created {
		if err := syncRootObject(parent); err != nil {
			_ = child.Close()
			return nil, fmt.Errorf("statestore.directory_sync: %w", err)
		}
	}
	return child, nil
}

func syncRootObject(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func rejectReservedPath(path string) error {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if reservedComponent(component) {
			return fmt.Errorf("statestore.config_path_reserved")
		}
	}
	return nil
}

func objectName(object Object) (string, error) {
	switch object {
	case ConfigLock:
		return "config.lock", nil
	case DaemonLock:
		return "daemon.lock", nil
	case ControlToken:
		return "control-token.v1", nil
	case WebToken:
		return "web-token.v1", nil
	case IdentitySeed:
		return "identity-seed.v1.json", nil
	case LastKnownGood:
		return "last-known-good.json", nil
	case PeerCredentialQuarantineLedger:
		return "peer-credential-quarantines.v1.json", nil
	case ConfigRevisionHighWater:
		return "config-revision-high-water.v1.json", nil
	case RejectedConfig:
		return "rejected-config.json", nil
	case PreMigrationConfig:
		return "pre-migration-config.json", nil
	default:
		return "", fmt.Errorf("statestore.object_invalid")
	}
}

func validBackupName(name string) bool {
	if !strings.HasPrefix(name, "backup.") || !strings.HasSuffix(name, ".json") {
		return false
	}
	parts := strings.Split(name, ".")
	return len(parts) == 5 && len(parts[1]) == len("20060102T150405") && len(parts[2]) == 9 && parts[3] != ""
}

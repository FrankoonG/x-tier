package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	SeedEnvelopeVersion   = 1
	SeedEnvelopeType      = "xtier-node-seed"
	SeedEnvelopeKDF       = "hkdf-sha256"
	SeedEnvelopeAlgorithm = IdentityAlgorithm
	maxSeedEnvelopeSize   = 4096
)

var (
	ErrInvalidSeedEnvelope = errors.New("invalid node seed envelope")
	ErrInsecureSeedFile    = errors.New("insecure node seed file")
	ErrAlreadyExists       = errors.New("identity seed already exists")
)

type seedEnvelope struct {
	Version   int
	Type      string
	KDF       string
	Algorithm string
	Seed      string
}

// Create generates and atomically persists a new identity. It never overwrites
// an existing path.
func Create(path string) (*Identity, error) {
	seed, err := CreateSeed(path)
	if err != nil {
		return nil, err
	}
	return FromSeed(seed)
}

// Load strictly loads a seed envelope and derives its identity.
func Load(path string) (*Identity, error) {
	seed, err := LoadSeed(path)
	if err != nil {
		return nil, err
	}
	return FromSeed(seed)
}

// CreateSeed generates and atomically persists a NodeSeed. It never overwrites
// an existing path.
func CreateSeed(path string) (NodeSeed, error) {
	if path == "" {
		return NodeSeed{}, fs.ErrInvalid
	}
	seed, err := GenerateNodeSeed()
	if err != nil {
		return NodeSeed{}, err
	}
	encoded, err := marshalSeedEnvelope(seed)
	if err != nil {
		return NodeSeed{}, err
	}
	if err := writeExclusiveAtomic(path, encoded); err != nil {
		return NodeSeed{}, fmt.Errorf("create node seed file: %w", err)
	}
	return seed, nil
}

// LoadSeed loads NodeSeed only from its dedicated, versioned envelope.
func LoadSeed(path string) (NodeSeed, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return NodeSeed{}, fmt.Errorf("inspect node seed file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return NodeSeed{}, fmt.Errorf("%w: path is not a regular file", ErrInvalidSeedEnvelope)
	}

	file, err := os.Open(path)
	if err != nil {
		return NodeSeed{}, fmt.Errorf("open node seed file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return NodeSeed{}, fmt.Errorf("inspect opened node seed file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return NodeSeed{}, fmt.Errorf("%w: seed file changed while opening", ErrInvalidSeedEnvelope)
	}
	if err := validateSecretPath(path, file, openedInfo); err != nil {
		return NodeSeed{}, errors.Join(ErrInvalidSeedEnvelope, ErrInsecureSeedFile, err)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxSeedEnvelopeSize+1))
	if err != nil {
		return NodeSeed{}, fmt.Errorf("read node seed file: %w", err)
	}
	if len(data) > maxSeedEnvelopeSize {
		return NodeSeed{}, fmt.Errorf("%w: file is too large", ErrInvalidSeedEnvelope)
	}
	return unmarshalSeedEnvelope(data)
}

func marshalSeedEnvelope(seed NodeSeed) ([]byte, error) {
	envelope := struct {
		Version   int    `json:"version"`
		Type      string `json:"type"`
		KDF       string `json:"kdf"`
		Algorithm string `json:"algorithm"`
		Seed      string `json:"seed"`
	}{
		Version:   SeedEnvelopeVersion,
		Type:      SeedEnvelopeType,
		KDF:       SeedEnvelopeKDF,
		Algorithm: SeedEnvelopeAlgorithm,
		Seed:      base64.RawURLEncoding.EncodeToString(seed.value[:]),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal node seed envelope: %w", err)
	}
	return append(data, '\n'), nil
}

func unmarshalSeedEnvelope(data []byte) (NodeSeed, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return NodeSeed{}, ErrInvalidSeedEnvelope
	}

	var envelope seedEnvelope
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return NodeSeed{}, ErrInvalidSeedEnvelope
		}
		key, ok := keyToken.(string)
		if !ok {
			return NodeSeed{}, ErrInvalidSeedEnvelope
		}
		if _, duplicate := seen[key]; duplicate {
			return NodeSeed{}, fmt.Errorf("%w: duplicate field", ErrInvalidSeedEnvelope)
		}
		seen[key] = struct{}{}

		switch key {
		case "version":
			err = decoder.Decode(&envelope.Version)
		case "type":
			err = decoder.Decode(&envelope.Type)
		case "kdf":
			err = decoder.Decode(&envelope.KDF)
		case "algorithm":
			err = decoder.Decode(&envelope.Algorithm)
		case "seed":
			err = decoder.Decode(&envelope.Seed)
		default:
			return NodeSeed{}, fmt.Errorf("%w: unknown field", ErrInvalidSeedEnvelope)
		}
		if err != nil {
			return NodeSeed{}, ErrInvalidSeedEnvelope
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return NodeSeed{}, ErrInvalidSeedEnvelope
	}
	if token, err = decoder.Token(); err != io.EOF {
		return NodeSeed{}, ErrInvalidSeedEnvelope
	}
	if len(seen) != 5 || envelope.Version != SeedEnvelopeVersion || envelope.Type != SeedEnvelopeType ||
		envelope.KDF != SeedEnvelopeKDF || envelope.Algorithm != SeedEnvelopeAlgorithm {
		return NodeSeed{}, ErrInvalidSeedEnvelope
	}

	raw, err := base64.RawURLEncoding.DecodeString(envelope.Seed)
	if err != nil || len(raw) != SeedSize || base64.RawURLEncoding.EncodeToString(raw) != envelope.Seed {
		return NodeSeed{}, ErrInvalidSeedEnvelope
	}
	return NewNodeSeed(raw)
}

func writeExclusiveAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return fs.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := secureSecretDirectory(dir); err != nil {
		return fmt.Errorf("secure node seed directory: %w", err)
	}

	temporary, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary seed file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
	}()
	if err := secureSecretFile(temporary); err != nil {
		return fmt.Errorf("secure temporary seed file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true

	// Publication is platform-specific but always atomic and non-overwriting.
	if err := publishSecretFile(temporaryPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errors.Join(ErrAlreadyExists, fs.ErrExist)
		}
		return fmt.Errorf("publish seed file: %w", err)
	}
	if err := validatePublishedSecret(path); err != nil {
		removeErr := os.Remove(path)
		syncErr := syncSecretDirectory(dir)
		return errors.Join(
			fmt.Errorf("validate published seed file: %w", err),
			wrapCleanupError("remove invalid published seed file", removeErr),
			wrapCleanupError("sync seed directory after cleanup", syncErr),
		)
	}
	return syncSecretDirectory(dir)
}

func wrapCleanupError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

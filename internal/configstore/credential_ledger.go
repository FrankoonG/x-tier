package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

const (
	peerCredentialLedgerVersion = 1
	peerCredentialLedgerSuffix  = ".peer-credential-quarantines.v1.json"
	maxPeerCredentialLedgerSize = 1 << 20
)

type peerCredentialQuarantineLedger struct {
	Version     int                        `json:"version"`
	Quarantines []PeerCredentialQuarantine `json:"quarantines"`
}

func peerCredentialLedgerPath(configPath string) string {
	return configPath + peerCredentialLedgerSuffix
}

func decodePeerCredentialLedger(payload []byte) ([]PeerCredentialQuarantine, error) {
	if err := preflightConfigJSON(payload); err != nil {
		return nil, configErrorf("config.peer_credential_ledger_decode: %w", err)
	}
	var ledger peerCredentialQuarantineLedger
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ledger); err != nil {
		return nil, markContentError(configErrorf("config.peer_credential_ledger_decode: %w", err))
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, markContentError(configErrorf("config.peer_credential_ledger_decode: %w", err))
	}
	if ledger.Version != peerCredentialLedgerVersion {
		return nil, markContentError(configErrorf(
			"config.peer_credential_ledger_version: have %d support %d",
			ledger.Version,
			peerCredentialLedgerVersion,
		))
	}
	canonical, err := mergePeerCredentialQuarantines(nil, ledger.Quarantines)
	if err != nil {
		return nil, markContentError(configErrorf("config.peer_credential_ledger_invalid: %w", err))
	}
	return canonical, nil
}

func encodePeerCredentialLedger(quarantines []PeerCredentialQuarantine) ([]byte, error) {
	canonical, err := mergePeerCredentialQuarantines(nil, quarantines)
	if err != nil {
		return nil, configErrorf("config.peer_credential_ledger_invalid: %w", err)
	}
	payload, err := json.MarshalIndent(peerCredentialQuarantineLedger{
		Version:     peerCredentialLedgerVersion,
		Quarantines: canonical,
	}, "", "  ")
	if err != nil {
		return nil, configErrorf("config.peer_credential_ledger_encode: %w", err)
	}
	return append(payload, '\n'), nil
}

func loadPathPeerCredentialLedger(configPath string) ([]PeerCredentialQuarantine, bool, error) {
	payload, err := readSecureFile(peerCredentialLedgerPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, configErrorf("config.peer_credential_ledger_read: %w", err)
	}
	quarantines, err := decodePeerCredentialLedger(payload)
	return quarantines, true, err
}

func loadStorePeerCredentialLedger(store *statestore.Store) ([]PeerCredentialQuarantine, bool, error) {
	payload, err := store.Read(statestore.PeerCredentialQuarantineLedger, maxPeerCredentialLedgerSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, configErrorf("config.peer_credential_ledger_read: %w", err)
	}
	quarantines, err := decodePeerCredentialLedger(payload)
	return quarantines, true, err
}

func persistPathPeerCredentialLedger(
	configPath string,
	additional []PeerCredentialQuarantine,
) ([]PeerCredentialQuarantine, error) {
	existing, exists, err := loadPathPeerCredentialLedger(configPath)
	if err != nil {
		return nil, err
	}
	merged, err := mergePeerCredentialQuarantines(existing, additional)
	if err != nil {
		return nil, configErrorf("config.peer_credential_ledger_merge: %w", err)
	}
	if exists && reflect.DeepEqual(existing, merged) {
		return merged, nil
	}
	payload, err := encodePeerCredentialLedger(merged)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(peerCredentialLedgerPath(configPath)), 0o700); err != nil {
		return nil, configErrorf("config.peer_credential_ledger_directory: %w", err)
	}
	if err := writeFileAtomic(peerCredentialLedgerPath(configPath), payload, 0o600); err != nil {
		return nil, configErrorf("config.peer_credential_ledger_write: %w", err)
	}
	return merged, nil
}

func persistStorePeerCredentialLedger(
	store *statestore.Store,
	additional []PeerCredentialQuarantine,
) ([]PeerCredentialQuarantine, error) {
	existing, exists, err := loadStorePeerCredentialLedger(store)
	if err != nil {
		return nil, err
	}
	merged, err := mergePeerCredentialQuarantines(existing, additional)
	if err != nil {
		return nil, configErrorf("config.peer_credential_ledger_merge: %w", err)
	}
	if exists && reflect.DeepEqual(existing, merged) {
		return merged, nil
	}
	payload, err := encodePeerCredentialLedger(merged)
	if err != nil {
		return nil, err
	}
	if err := store.Replace(statestore.PeerCredentialQuarantineLedger, payload); err != nil {
		return nil, configErrorf("config.peer_credential_ledger_write: %w", err)
	}
	return merged, nil
}

func mergeCredentialLedgerIntoConfig(
	cfg *Config,
	ledger []PeerCredentialQuarantine,
) (bool, error) {
	merged, err := mergePeerCredentialQuarantines(cfg.PeerCredentialQuarantines, ledger)
	if err != nil {
		return false, configErrorf("config.peer_credential_ledger_merge: %w", err)
	}
	changed := !reflect.DeepEqual(cfg.PeerCredentialQuarantines, merged)
	cfg.PeerCredentialQuarantines = merged
	quarantined, err := quarantinePeerCredentialCollisions(cfg)
	if err != nil {
		return false, err
	}
	return changed || quarantined, nil
}

func validateConfigMatchesCredentialLedger(
	cfg Config,
	ledger []PeerCredentialQuarantine,
	ledgerExists bool,
) error {
	if !ledgerExists {
		if len(cfg.PeerCredentialQuarantines) != 0 {
			return markContentError(configErrorf("config.peer_credential_ledger_missing"))
		}
		return nil
	}
	merged, err := mergePeerCredentialQuarantines(cfg.PeerCredentialQuarantines, ledger)
	if err != nil {
		return markContentError(configErrorf("config.peer_credential_ledger_merge: %w", err))
	}
	if !reflect.DeepEqual(cfg.PeerCredentialQuarantines, ledger) || !reflect.DeepEqual(merged, ledger) {
		return markContentError(configErrorf("config.peer_credential_ledger_mismatch"))
	}
	return nil
}

func requireConfigCoversCredentialLedger(
	cfg Config,
	ledger []PeerCredentialQuarantine,
	ledgerExists bool,
) error {
	if !ledgerExists {
		return nil
	}
	merged, err := mergePeerCredentialQuarantines(cfg.PeerCredentialQuarantines, ledger)
	if err != nil {
		return configErrorf("config.peer_credential_ledger_merge: %w", err)
	}
	if !reflect.DeepEqual(cfg.PeerCredentialQuarantines, merged) {
		return configErrorf("config.peer_credential_ledger_stale")
	}
	return nil
}

func preparePathConfigPublication(configPath string, cfg Config) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return nil, err
	}
	normalize(&cfg)
	ledger, ledgerExists, err := loadPathPeerCredentialLedger(configPath)
	if err != nil {
		return nil, err
	}
	if err := requireConfigCoversCredentialLedger(cfg, ledger, ledgerExists); err != nil {
		return nil, err
	}
	payload, err := EncodeDocument(cfg)
	if err != nil {
		return nil, err
	}
	persistedLedger, err := persistPathPeerCredentialLedger(configPath, cfg.PeerCredentialQuarantines)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(persistedLedger, cfg.PeerCredentialQuarantines) {
		return nil, configErrorf("config.peer_credential_ledger_stale")
	}
	return payload, nil
}

func prepareStoreConfigPublication(store *statestore.Store, cfg Config) ([]byte, error) {
	normalize(&cfg)
	ledger, ledgerExists, err := loadStorePeerCredentialLedger(store)
	if err != nil {
		return nil, err
	}
	if err := requireConfigCoversCredentialLedger(cfg, ledger, ledgerExists); err != nil {
		return nil, err
	}
	payload, err := EncodeDocument(cfg)
	if err != nil {
		return nil, err
	}
	persistedLedger, err := persistStorePeerCredentialLedger(store, cfg.PeerCredentialQuarantines)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(persistedLedger, cfg.PeerCredentialQuarantines) {
		return nil, configErrorf("config.peer_credential_ledger_stale")
	}
	return payload, nil
}

package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

const (
	configRevisionHighWaterVersion = 1
	configRevisionHighWaterSuffix  = ".revision-high-water.v1.json"
	maxConfigRevisionHighWaterSize = 4096
)

type configRevisionHighWaterDocument struct {
	Version  int   `json:"version"`
	Revision int64 `json:"revision"`
}

func configRevisionHighWaterPath(configPath string) string {
	return configPath + configRevisionHighWaterSuffix
}

func decodeConfigRevisionHighWater(payload []byte) (int64, error) {
	if err := preflightConfigJSON(payload); err != nil {
		return 0, configErrorf("config.revision_high_water_decode: %w", err)
	}
	var document configRevisionHighWaterDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return 0, markContentError(configErrorf("config.revision_high_water_decode: %w", err))
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return 0, markContentError(configErrorf("config.revision_high_water_decode: %w", err))
	}
	if document.Version != configRevisionHighWaterVersion {
		return 0, markContentError(configErrorf(
			"config.revision_high_water_version: have %d support %d",
			document.Version,
			configRevisionHighWaterVersion,
		))
	}
	if document.Revision < 0 {
		return 0, markContentError(configErrorf("config.revision_high_water_negative"))
	}
	return document.Revision, nil
}

func encodeConfigRevisionHighWater(revision int64) ([]byte, error) {
	if revision < 0 {
		return nil, configErrorf("config.revision_high_water_negative")
	}
	payload, err := json.MarshalIndent(configRevisionHighWaterDocument{
		Version:  configRevisionHighWaterVersion,
		Revision: revision,
	}, "", "  ")
	if err != nil {
		return nil, configErrorf("config.revision_high_water_encode: %w", err)
	}
	return append(payload, '\n'), nil
}

func loadPathConfigRevisionHighWater(configPath string) (int64, bool, error) {
	payload, err := readSecureFile(configRevisionHighWaterPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, configErrorf("config.revision_high_water_read: %w", err)
	}
	revision, err := decodeConfigRevisionHighWater(payload)
	return revision, true, err
}

func loadStoreConfigRevisionHighWater(store *statestore.Store) (int64, bool, error) {
	payload, err := store.Read(statestore.ConfigRevisionHighWater, maxConfigRevisionHighWaterSize)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, configErrorf("config.revision_high_water_read: %w", err)
	}
	revision, err := decodeConfigRevisionHighWater(payload)
	return revision, true, err
}

func persistPathConfigRevisionHighWater(configPath string, revision int64) error {
	existing, exists, err := loadPathConfigRevisionHighWater(configPath)
	if err != nil {
		return err
	}
	if exists && existing >= revision {
		return nil
	}
	payload, err := encodeConfigRevisionHighWater(revision)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(configRevisionHighWaterPath(configPath), payload, 0o600); err != nil {
		return configErrorf("config.revision_high_water_write: %w", err)
	}
	return nil
}

func persistStoreConfigRevisionHighWater(store *statestore.Store, revision int64) error {
	return persistStoreConfigRevisionHighWaterWithReplace(store, revision, store.Replace)
}

func persistStoreConfigRevisionHighWaterWithReplace(
	store *statestore.Store,
	revision int64,
	replace func(statestore.Object, []byte) error,
) error {
	existing, exists, err := loadStoreConfigRevisionHighWater(store)
	if err != nil {
		return err
	}
	if exists && existing >= revision {
		return nil
	}
	payload, err := encodeConfigRevisionHighWater(revision)
	if err != nil {
		return err
	}
	if err := replace(statestore.ConfigRevisionHighWater, payload); err != nil {
		if errors.Is(err, statestore.ErrCommitOutcomeUnknown) {
			return configErrorf(
				"config.revision_high_water_outcome_indeterminate: %w",
				errors.Join(ErrCommitOutcomeUnknown, err),
			)
		}
		return configErrorf("config.revision_high_water_write: %w", err)
	}
	return nil
}

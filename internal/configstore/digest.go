package configstore

import (
	"crypto/sha256"
	"encoding/json"
)

// ContentDigest identifies the complete normalized configuration content.
// Callers keep the digest internal; it may reflect changes to secret fields.
func ContentDigest(cfg Config) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var normalized Config
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return [sha256.Size]byte{}, err
	}
	normalize(&normalized)
	encoded, err = json.Marshal(normalized)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

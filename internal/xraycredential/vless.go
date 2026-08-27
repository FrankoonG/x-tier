package xraycredential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/proxy/vless"
)

var ErrVLESSCredentialFormat = errors.New("VLESS credential must be a canonical lowercase UUID")

const vlessFingerprintDomain = "xtier:vless-credential-fingerprint:v1\x00"

// VLESSLookupKey returns the lookup key Xray uses for any credential spelling
// accepted by its UUID parser. This is intentionally more permissive than
// VLESSKey so cross-protocol credential comparisons cannot be bypassed with an
// uppercase, undashed, or short SHA-1-mapped Xray alias.
func VLESSLookupKey(raw string) (string, error) {
	parsed, err := uuid.ParseString(raw)
	if err != nil {
		return "", ErrVLESSCredentialFormat
	}
	processed := vless.ProcessUUID(parsed)
	return hex.EncodeToString(processed[:]), nil
}

// VLESSKey returns the exact lookup key used by Xray's VLESS validator. Xray
// deliberately ignores UUID bytes 6 and 7, so comparing UUID text directly is
// not sufficient to prove that two credentials are distinct. Stored VLESS
// profiles remain restricted to canonical lowercase UUID text.
func VLESSKey(raw string) (string, error) {
	parsed, err := uuid.ParseString(raw)
	if err != nil || parsed.String() != raw {
		return "", ErrVLESSCredentialFormat
	}
	return VLESSLookupKey(raw)
}

// VLESSFingerprint returns a one-way, domain-separated identifier for the
// exact credential equivalence class enforced by Xray. It is suitable for a
// durable deny-list without persisting another directly usable credential.
func VLESSFingerprint(raw string) (string, error) {
	key, err := VLESSKey(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(vlessFingerprintDomain + key))
	return hex.EncodeToString(digest[:]), nil
}

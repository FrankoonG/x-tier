// Package identity owns the persistent node identity root and its public identity.
package identity

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	// SeedSize is the byte length of a NodeSeed.
	SeedSize = 32
	// NodeSeedSize is the explicit alias used by storage callers.
	NodeSeedSize = SeedSize

	// IdentityVersion and IdentityAlgorithm identify the first public identity suite.
	IdentityVersion   = 2
	IdentityAlgorithm = "ed25519"

	// IdentityDomainLabel separates the Ed25519 identity root from all other
	// current and future NodeSeed-derived keys.
	IdentityDomainLabel = "xtier/v2/identity-signing/ed25519-seed"
	// NodeIDDomainLabel separates X-Tier node identifiers from fingerprints of
	// the same public key used by another protocol or application.
	NodeIDDomainLabel    = "xtier/v2/node-id/ed25519-sha256"
	SignatureDomainLabel = "xtier/v2/identity-signature"

	// NodeIDPrefix makes both the identity format version and key algorithm explicit.
	NodeIDPrefix = "xtier-v2-ed25519-"
	// LegacyNodeIDPrefix identifies the exact random identifier format emitted by
	// pre-v2 X-Tier configurations. Legacy IDs never have an attached public key.
	LegacyNodeIDPrefix = "node-"
)

var (
	ErrInvalidNodeSeed           = errors.New("invalid node seed")
	ErrInvalidNodeID             = errors.New("invalid node ID")
	ErrInvalidPublicIdentity     = errors.New("invalid public identity")
	ErrInvalidConfiguredIdentity = errors.New("invalid configured identity")
	ErrUnsupportedIdentitySuite  = errors.New("unsupported identity suite")
)

var nodeIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NodeSeed is the local high-value root secret. Its bytes are intentionally
// unexported so ordinary JSON encoding and formatted logging cannot disclose it.
type NodeSeed struct {
	value       [SeedSize]byte
	initialized bool
}

// GenerateNodeSeed obtains a new NodeSeed directly from crypto/rand.
func GenerateNodeSeed() (NodeSeed, error) {
	var seed NodeSeed
	if _, err := rand.Read(seed.value[:]); err != nil {
		return NodeSeed{}, fmt.Errorf("generate node seed: %w", err)
	}
	seed.initialized = true
	return seed, nil
}

// NewNodeSeed validates and copies raw seed material. It is intended for
// explicit restore/import paths, not ordinary configuration loading.
func NewNodeSeed(raw []byte) (NodeSeed, error) {
	if len(raw) != SeedSize {
		return NodeSeed{}, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidNodeSeed, len(raw), SeedSize)
	}
	var nonzero byte
	for _, value := range raw {
		nonzero |= value
	}
	if nonzero == 0 {
		return NodeSeed{}, fmt.Errorf("%w: all-zero seed", ErrInvalidNodeSeed)
	}
	var seed NodeSeed
	copy(seed.value[:], raw)
	seed.initialized = true
	return seed, nil
}

// Bytes returns an explicit copy for dedicated backup/export code.
func (s NodeSeed) Bytes() []byte {
	if !s.initialized {
		return nil
	}
	return append([]byte(nil), s.value[:]...)
}

// Equal compares two seeds without early exit.
func (s NodeSeed) Equal(other NodeSeed) bool {
	return s.initialized == other.initialized && subtle.ConstantTimeCompare(s.value[:], other.value[:]) == 1
}

// String prevents accidental disclosure through fmt and logging.
func (NodeSeed) String() string { return "[REDACTED NodeSeed]" }

// GoString prevents accidental disclosure through %#v formatting.
func (NodeSeed) GoString() string { return "identity.NodeSeed{[REDACTED]}" }

// MarshalJSON rejects attempts to place a NodeSeed in ordinary JSON/configuration.
func (NodeSeed) MarshalJSON() ([]byte, error) {
	return nil, errors.New("identity: NodeSeed cannot be marshaled as ordinary JSON")
}

// NodeID is the canonical, versioned fingerprint of an identity public key.
type NodeID string

func (id NodeID) String() string { return string(id) }

// NewNodeID computes the stable NodeID for an Ed25519 public key.
func NewNodeID(publicKey ed25519.PublicKey) (NodeID, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: invalid Ed25519 public key length", ErrInvalidNodeID)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(NodeIDDomainLabel))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(publicKey)
	fingerprint := hasher.Sum(nil)
	encoded := strings.ToLower(nodeIDEncoding.EncodeToString(fingerprint))
	return NodeID(NodeIDPrefix + encoded), nil
}

// ParseNodeID accepts only the canonical lower-case v2 Ed25519 encoding.
func ParseNodeID(value string) (NodeID, error) {
	encoded, ok := strings.CutPrefix(value, NodeIDPrefix)
	if !ok || len(encoded) != nodeIDEncodedLength || encoded != strings.ToLower(encoded) {
		return "", ErrInvalidNodeID
	}
	fingerprint, err := nodeIDEncoding.DecodeString(strings.ToUpper(encoded))
	if err != nil || len(fingerprint) != sha256.Size {
		return "", ErrInvalidNodeID
	}
	canonical := strings.ToLower(nodeIDEncoding.EncodeToString(fingerprint))
	if encoded != canonical {
		return "", ErrInvalidNodeID
	}
	return NodeID(value), nil
}

// IsLegacyNodeID reports whether value is the exact pre-v2 random identifier
// format: "node-" followed by 16 bytes encoded as lower-case hexadecimal.
func IsLegacyNodeID(value string) bool {
	if len(value) != len(LegacyNodeIDPrefix)+32 || !strings.HasPrefix(value, LegacyNodeIDPrefix) {
		return false
	}
	for index := len(LegacyNodeIDPrefix); index < len(value); index++ {
		character := value[index]
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

// ConfiguredIdentityClass is the authoritative classification used by
// configuration, CLI status, and future address-book admission paths.
type ConfiguredIdentityClass string

const (
	ConfiguredIdentityUninitialized ConfiguredIdentityClass = "uninitialized"
	ConfiguredIdentityV2            ConfiguredIdentityClass = "v2"
	ConfiguredIdentityLegacy        ConfiguredIdentityClass = "legacy"
)

// ClassifyConfiguredIdentity accepts only an empty identity, a complete valid
// v2 identity, or the exact pre-v2 random NodeID without a public key.
func ClassifyConfiguredIdentity(nodeID, publicKey string) (ConfiguredIdentityClass, error) {
	if nodeID == "" && publicKey == "" {
		return ConfiguredIdentityUninitialized, nil
	}
	if strings.HasPrefix(nodeID, NodeIDPrefix) {
		public := PublicIdentity{
			Version:   IdentityVersion,
			Algorithm: IdentityAlgorithm,
			NodeID:    NodeID(nodeID),
			PublicKey: publicKey,
		}
		if err := public.Validate(); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfiguredIdentity, err)
		}
		return ConfiguredIdentityV2, nil
	}
	if IsLegacyNodeID(nodeID) {
		if publicKey != "" {
			return "", fmt.Errorf("%w: legacy NodeID cannot have a public key", ErrInvalidConfiguredIdentity)
		}
		return ConfiguredIdentityLegacy, nil
	}
	if strings.HasPrefix(nodeID, "xtier-") {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedIdentitySuite, nodeID)
	}
	return "", fmt.Errorf("%w: unsupported legacy format", ErrInvalidConfiguredIdentity)
}

const nodeIDEncodedLength = (sha256.Size*8 + 4) / 5

// MatchesPublicKey verifies that id is canonical and fingerprints publicKey.
func (id NodeID) MatchesPublicKey(publicKey ed25519.PublicKey) bool {
	parsed, err := ParseNodeID(string(id))
	if err != nil {
		return false
	}
	want, err := NewNodeID(publicKey)
	return err == nil && subtle.ConstantTimeCompare([]byte(parsed), []byte(want)) == 1
}

// PublicIdentity is the complete publishable identity. It never contains NodeSeed
// or private-key material.
type PublicIdentity struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	NodeID    NodeID `json:"node_id"`
	PublicKey string `json:"public_key"`
}

type SignaturePurpose string

const SignaturePurposeIdentityProof SignaturePurpose = "identity-proof"

// Validate rejects unknown suites, malformed keys, and mismatched fingerprints.
func (p PublicIdentity) Validate() error {
	if p.Version != IdentityVersion {
		return fmt.Errorf("%w: unsupported version", ErrInvalidPublicIdentity)
	}
	if p.Algorithm != IdentityAlgorithm {
		return fmt.Errorf("%w: unsupported algorithm", ErrInvalidPublicIdentity)
	}
	publicKey, err := decodePublicKey(p.PublicKey)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPublicIdentity, err)
	}
	if !p.NodeID.MatchesPublicKey(publicKey) {
		return fmt.Errorf("%w: NodeID does not match public key", ErrInvalidPublicIdentity)
	}
	return nil
}

// Verify validates the public identity before checking an Ed25519 signature.
func (p PublicIdentity) Verify(purpose SignaturePurpose, message, signature []byte) bool {
	if p.Validate() != nil {
		return false
	}
	framed, err := signaturePayload(purpose, message)
	if err != nil {
		return false
	}
	publicKey, err := decodePublicKey(p.PublicKey)
	return err == nil && ed25519.Verify(publicKey, framed, signature)
}

// Identity holds the derived signing key in memory and exposes only public data.
type Identity struct {
	privateKey ed25519.PrivateKey
	public     PublicIdentity
}

// FromSeed deterministically derives the v2 Ed25519 identity from seed using HKDF-SHA256.
func FromSeed(seed NodeSeed) (*Identity, error) {
	if !seed.initialized {
		return nil, ErrInvalidNodeSeed
	}
	edSeed, err := hkdf.Key(sha256.New, seed.value[:], nil, IdentityDomainLabel, ed25519.SeedSize)
	if err != nil {
		return nil, fmt.Errorf("derive identity signing seed: %w", err)
	}
	privateKey := ed25519.NewKeyFromSeed(edSeed)
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	nodeID, err := NewNodeID(publicKey)
	if err != nil {
		return nil, err
	}
	return &Identity{
		privateKey: privateKey,
		public: PublicIdentity{
			Version:   IdentityVersion,
			Algorithm: IdentityAlgorithm,
			NodeID:    nodeID,
			PublicKey: encodePublicKey(publicKey),
		},
	}, nil
}

// NewIdentity derives an identity from a NodeSeed.
func NewIdentity(seed NodeSeed) (*Identity, error) { return FromSeed(seed) }

// Public returns a detached copy safe to publish or encode.
func (i *Identity) Public() PublicIdentity {
	return i.public
}

// NodeID returns the stable public identifier.
func (i *Identity) NodeID() NodeID { return i.public.NodeID }

// Sign signs message with the identity root key.
func (i *Identity) Sign(purpose SignaturePurpose, message []byte) ([]byte, error) {
	if len(i.privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("identity: signing key is unavailable")
	}
	framed, err := signaturePayload(purpose, message)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(i.privateKey, framed), nil
}

func signaturePayload(purpose SignaturePurpose, message []byte) ([]byte, error) {
	if len(purpose) == 0 || len(purpose) > 64 {
		return nil, errors.New("identity: signature purpose length is invalid")
	}
	for index := range purpose {
		character := purpose[index]
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '-' || character == '.' || character == '/' {
			continue
		}
		return nil, fmt.Errorf("identity: signature purpose contains invalid byte at %d", index)
	}
	payload := make([]byte, 0, len(SignatureDomainLabel)+1+2+len(purpose)+8+len(message))
	payload = append(payload, SignatureDomainLabel...)
	payload = append(payload, 0)
	var lengths [8]byte
	binary.BigEndian.PutUint16(lengths[:2], uint16(len(purpose)))
	payload = append(payload, lengths[:2]...)
	payload = append(payload, purpose...)
	binary.BigEndian.PutUint64(lengths[:], uint64(len(message)))
	payload = append(payload, lengths[:]...)
	payload = append(payload, message...)
	return payload, nil
}

func encodePublicKey(publicKey ed25519.PublicKey) string {
	return strings.ToLower(nodeIDEncoding.EncodeToString(publicKey))
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	if encoded == "" || encoded != strings.ToLower(encoded) {
		return nil, errors.New("invalid public key encoding")
	}
	publicKey, err := nodeIDEncoding.DecodeString(strings.ToUpper(encoded))
	if err != nil || len(publicKey) != ed25519.PublicKeySize || encodePublicKey(publicKey) != encoded {
		return nil, errors.New("invalid public key encoding")
	}
	return ed25519.PublicKey(publicKey), nil
}

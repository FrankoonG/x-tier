package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIdentityDerivationVectorAndSignatures(t *testing.T) {
	raw := make([]byte, SeedSize)
	for index := range raw {
		raw[index] = byte(index)
	}
	seed, err := NewNodeSeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	first, err := FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}

	public := first.Public()
	if public.NodeID != second.NodeID() {
		t.Fatal("the same NodeSeed derived different identities")
	}
	const wantNodeID = "xtier-v2-ed25519-yzq5ggwg7qwjzh33txpr2qyvrcz4e5j6qxx3wd6ovnsnov2za7oa"
	if public.NodeID.String() != wantNodeID {
		t.Fatalf("derived NodeID = %q, want %q; public key = %s", public.NodeID, wantNodeID, public.PublicKey)
	}
	const wantPublicKey = "qhhp42egxvuumwa3llygtpvnltvtgzfcrtwdeudplsq43guurrdq"
	if public.PublicKey != wantPublicKey {
		t.Fatalf("derived public key = %q, want %q", public.PublicKey, wantPublicKey)
	}
	if public.Version != IdentityVersion || public.Algorithm != IdentityAlgorithm {
		t.Fatalf("unexpected public suite: %+v", public)
	}
	if _, err := ParseNodeID(public.NodeID.String()); err != nil {
		t.Fatalf("generated NodeID is invalid: %v", err)
	}

	message := []byte("x-tier identity proof")
	signature, err := first.Sign(SignaturePurposeIdentityProof, message)
	if err != nil {
		t.Fatal(err)
	}
	if !public.Verify(SignaturePurposeIdentityProof, message, signature) {
		t.Fatal("valid signature was rejected")
	}
	if public.Verify(SignaturePurposeIdentityProof, []byte("modified"), signature) {
		t.Fatal("signature for a different message was accepted")
	}
	signature[0] ^= 0x80
	if public.Verify(SignaturePurposeIdentityProof, message, signature) {
		t.Fatal("modified signature was accepted")
	}
}

func TestNodeSeedSecretBoundaries(t *testing.T) {
	raw := make([]byte, SeedSize)
	for index := range raw {
		raw[index] = byte(index + 1)
	}
	seed, err := NewNodeSeed(raw)
	if err != nil {
		t.Fatal(err)
	}

	backup := seed.Bytes()
	backup[0] ^= 0xff
	if seed.Bytes()[0] != raw[0] {
		t.Fatal("Bytes returned storage aliased to NodeSeed")
	}
	if got := fmt.Sprint(seed); !strings.Contains(got, "REDACTED") {
		t.Fatalf("NodeSeed string representation is not redacted: %q", got)
	}
	if _, err := json.Marshal(seed); err == nil {
		t.Fatal("NodeSeed was accepted by ordinary JSON encoding")
	}

	identity, err := FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(identity.Public())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"seed", "private_key", "secret"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("public identity contains forbidden field %q", forbidden)
		}
	}
	if len(fields) != 4 {
		t.Fatalf("unexpected public identity fields: %s", encoded)
	}
}

func TestPublicIdentityIsDetachedAndStrictlyValidated(t *testing.T) {
	identity := testIdentity(t)
	public := identity.Public()
	tests := map[string]func(*PublicIdentity){
		"version":   func(value *PublicIdentity) { value.Version++ },
		"algorithm": func(value *PublicIdentity) { value.Algorithm = "rsa" },
		"node ID": func(value *PublicIdentity) {
			value.NodeID = NodeID(NodeIDPrefix + strings.Repeat("a", nodeIDEncodedLength))
		},
		"public key":    func(value *PublicIdentity) { value.PublicKey = value.PublicKey[:len(value.PublicKey)-1] },
		"uppercase key": func(value *PublicIdentity) { value.PublicKey = strings.ToUpper(value.PublicKey) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := public
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid public identity was accepted")
			}
			if candidate.Verify(SignaturePurposeIdentityProof, []byte("message"), make([]byte, ed25519.SignatureSize)) {
				t.Fatal("invalid public identity verified a signature")
			}
		})
	}
}

func TestParseNodeIDRejectsNonCanonicalValues(t *testing.T) {
	id := testIdentity(t).NodeID().String()
	for _, value := range []string{
		"",
		id[:len(id)-1],
		strings.ToUpper(id),
		"xtier-v1-ed25519-" + strings.TrimPrefix(id, NodeIDPrefix),
		id + "=",
	} {
		if _, err := ParseNodeID(value); err == nil {
			t.Fatalf("ParseNodeID accepted %q", value)
		}
	}
}

func TestLegacyNodeIDClassificationIsExact(t *testing.T) {
	valid := "node-0123456789abcdef0123456789abcdef"
	if !IsLegacyNodeID(valid) {
		t.Fatalf("valid legacy NodeID was rejected: %s", valid)
	}
	for _, value := range []string{
		"",
		"legacy-node",
		"node-0123456789abcdef0123456789abcde",
		"node-0123456789abcdef0123456789abcdef0",
		"node-0123456789ABCDEF0123456789ABCDEF",
		"node-0123456789abcdef0123456789abcdeg",
		"xtier-v3-ed25519-0123456789abcdef0123456789abcdef",
	} {
		if IsLegacyNodeID(value) {
			t.Fatalf("invalid legacy NodeID was accepted: %q", value)
		}
	}
}

func TestConfiguredIdentityClassification(t *testing.T) {
	public := testIdentity(t).Public()
	tests := []struct {
		name      string
		nodeID    string
		publicKey string
		want      ConfiguredIdentityClass
		wantError error
	}{
		{name: "empty", want: ConfiguredIdentityUninitialized},
		{name: "v2", nodeID: public.NodeID.String(), publicKey: public.PublicKey, want: ConfiguredIdentityV2},
		{name: "legacy", nodeID: "node-0123456789abcdef0123456789abcdef", want: ConfiguredIdentityLegacy},
		{name: "v2 missing key", nodeID: public.NodeID.String(), wantError: ErrInvalidConfiguredIdentity},
		{name: "legacy with key", nodeID: "node-0123456789abcdef0123456789abcdef", publicKey: public.PublicKey, wantError: ErrInvalidConfiguredIdentity},
		{name: "unknown suite", nodeID: "xtier-v3-ed25519-unknown", publicKey: public.PublicKey, wantError: ErrUnsupportedIdentitySuite},
		{name: "arbitrary", nodeID: "legacy-node", wantError: ErrInvalidConfiguredIdentity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ClassifyConfiguredIdentity(tc.nodeID, tc.publicKey)
			if tc.wantError != nil {
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("error = %v, want %v", err, tc.wantError)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("classification = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestNewNodeSeedAndNodeIDRejectWrongLengths(t *testing.T) {
	for _, size := range []int{0, SeedSize - 1, SeedSize + 1} {
		if _, err := NewNodeSeed(make([]byte, size)); err == nil {
			t.Fatalf("NewNodeSeed accepted %d bytes", size)
		}
	}
	if _, err := NewNodeSeed(make([]byte, SeedSize)); err == nil {
		t.Fatal("NewNodeSeed accepted an all-zero seed")
	}
	if _, err := FromSeed(NodeSeed{}); err == nil {
		t.Fatal("FromSeed accepted the zero value")
	}
	if _, err := NewNodeID(make(ed25519.PublicKey, ed25519.PublicKeySize-1)); err == nil {
		t.Fatal("NewNodeID accepted a short public key")
	}
}

func TestSignaturePurposeIsMandatoryAndDomainSeparated(t *testing.T) {
	identity := testIdentity(t)
	message := []byte("same bytes")
	signature, err := identity.Sign(SignaturePurposeIdentityProof, message)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Public().Verify(SignaturePurpose("address-record"), message, signature) {
		t.Fatal("signature verified under a different purpose")
	}
	if _, err := identity.Sign("", message); err == nil {
		t.Fatal("empty signature purpose was accepted")
	}
	if _, err := identity.Sign("INVALID PURPOSE", message); err == nil {
		t.Fatal("invalid signature purpose was accepted")
	}
}

func testIdentity(t *testing.T) *Identity {
	t.Helper()
	seed, err := GenerateNodeSeed()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

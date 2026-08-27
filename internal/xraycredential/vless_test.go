package xraycredential

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/xtls/xray-core/common/uuid"
)

func TestVLESSKeyMatchesXrayCredentialEquivalence(t *testing.T) {
	first, err := VLESSKey("66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if err != nil {
		t.Fatal(err)
	}
	second, err := VLESSKey("66ad4540-b58c-4ead-9926-ea63445a9b57")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("Xray-equivalent credentials produced distinct keys: %q != %q", first, second)
	}
}

func TestVLESSFingerprintMatchesOnlyXrayEquivalentCredentials(t *testing.T) {
	first, err := VLESSFingerprint("66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := VLESSFingerprint("66ad4540-b58c-4ead-9926-ea63445a9b57")
	if err != nil {
		t.Fatal(err)
	}
	distinct, err := VLESSFingerprint("f3c9805c-12ea-48f0-b762-5739f2365620")
	if err != nil {
		t.Fatal(err)
	}
	if first != equivalent {
		t.Fatalf("equivalent credentials produced distinct fingerprints: %q != %q", first, equivalent)
	}
	if first == distinct {
		t.Fatalf("distinct credentials produced the same fingerprint: %q", first)
	}
	if len(first) != sha256.Size*2 {
		t.Fatalf("fingerprint length = %d", len(first))
	}
}

func TestVLESSKeyRejectsNonCanonicalAliases(t *testing.T) {
	for _, value := range []string{
		"66AD4540-B58C-4AD2-9926-EA63445A9B57",
		"66ad4540b58c4ad29926ea63445a9b57",
		"peer-a-secret",
	} {
		if _, err := VLESSKey(value); !errors.Is(err, ErrVLESSCredentialFormat) {
			t.Fatalf("VLESSKey(%q) error = %v", value, err)
		}
	}
}

func TestVLESSLookupKeyMatchesEveryXrayAcceptedAlias(t *testing.T) {
	canonical := "66ad4540-b58c-4ad2-9926-ea63445a9b57"
	want, err := VLESSLookupKey(canonical)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{
		"66AD4540-B58C-4AD2-9926-EA63445A9B57",
		"66ad4540b58c4ad29926ea63445a9b57",
	} {
		got, err := VLESSLookupKey(alias)
		if err != nil {
			t.Fatalf("VLESSLookupKey(%q): %v", alias, err)
		}
		if got != want {
			t.Fatalf("VLESSLookupKey(%q) = %q, want %q", alias, got, want)
		}
	}

	short := "peer-a-secret"
	parsed, err := uuid.ParseString(short)
	if err != nil {
		t.Fatal(err)
	}
	shortKey, err := VLESSLookupKey(short)
	if err != nil {
		t.Fatal(err)
	}
	canonicalShortKey, err := VLESSLookupKey(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	if shortKey != canonicalShortKey {
		t.Fatalf("short Xray alias key = %q, canonical key = %q", shortKey, canonicalShortKey)
	}
}

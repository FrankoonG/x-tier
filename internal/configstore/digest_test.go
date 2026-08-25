package configstore

import "testing"

func TestContentDigestIsStableAcrossMapInsertionOrder(t *testing.T) {
	first := DefaultConfig()
	first.Revision = 7
	first.XrayProfiles["z"] = XrayProfile{ID: "z", Kind: "socks", SOCKS: &SOCKSProfile{Username: "z"}}
	first.XrayProfiles["a"] = XrayProfile{ID: "a", Kind: "socks", SOCKS: &SOCKSProfile{Username: "a"}}

	second := DefaultConfig()
	second.Revision = 7
	second.XrayProfiles["a"] = XrayProfile{ID: "a", Kind: "socks", SOCKS: &SOCKSProfile{Username: "a"}}
	second.XrayProfiles["z"] = XrayProfile{ID: "z", Kind: "socks", SOCKS: &SOCKSProfile{Username: "z"}}

	firstDigest, err := ContentDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := ContentDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("equivalent configurations produced different content digests")
	}
}

func TestContentDigestIncludesRevisionAndSecretFields(t *testing.T) {
	base := DefaultConfig()
	base.Revision = 7
	base.XrayProfiles["socks"] = XrayProfile{
		ID:    "socks",
		Kind:  "socks",
		SOCKS: &SOCKSProfile{Username: "user", Password: "first-secret"},
	}

	baseDigest, err := ContentDigest(base)
	if err != nil {
		t.Fatal(err)
	}

	changedRevision := base
	changedRevision.Revision++
	revisionDigest, err := ContentDigest(changedRevision)
	if err != nil {
		t.Fatal(err)
	}
	if revisionDigest == baseDigest {
		t.Fatal("revision change did not change content digest")
	}

	changedSecret := base
	changedSecret.XrayProfiles = map[string]XrayProfile{
		"socks": {
			ID:    "socks",
			Kind:  "socks",
			SOCKS: &SOCKSProfile{Username: "user", Password: "second-secret"},
		},
	}
	secretDigest, err := ContentDigest(changedSecret)
	if err != nil {
		t.Fatal(err)
	}
	if secretDigest == baseDigest {
		t.Fatal("secret change did not change content digest")
	}
}

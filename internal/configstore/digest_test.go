package configstore

import (
	"reflect"
	"testing"
)

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

func TestContentDigestNormalizesGrantRuleOrderWithoutMutatingCaller(t *testing.T) {
	first := validNodeEgressGrantConfig()
	firstGrant := first.NodeEgressGrants["node-a"]
	firstGrant.AllowCIDRs = []string{"9.0.0.0/8", "8.0.0.0/8"}
	firstGrant.AllowPrivateCIDRs = []string{"192.168.0.0/16", "10.0.0.0/8"}
	firstGrant.DenyCIDRs = []string{"9.9.0.0/16", "8.8.0.0/16"}
	firstGrant.AllowPorts = []EgressPortRange{{From: 8000, To: 8099}, {From: 443, To: 443}}
	first.NodeEgressGrants["node-a"] = firstGrant
	originalCIDROrder := append([]string(nil), firstGrant.AllowCIDRs...)
	originalPortOrder := append([]EgressPortRange(nil), firstGrant.AllowPorts...)

	second := validNodeEgressGrantConfig()
	secondGrant := second.NodeEgressGrants["node-a"]
	secondGrant.AllowCIDRs = []string{"8.0.0.0/8", "9.0.0.0/8"}
	secondGrant.AllowPrivateCIDRs = []string{"10.0.0.0/8", "192.168.0.0/16"}
	secondGrant.DenyCIDRs = []string{"8.8.0.0/16", "9.9.0.0/16"}
	secondGrant.AllowPorts = []EgressPortRange{{From: 443, To: 443}, {From: 8000, To: 8099}}
	second.NodeEgressGrants["node-a"] = secondGrant

	firstDigest, err := ContentDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := ContentDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("semantically equivalent grant order produced different content digests")
	}
	gotGrant := first.NodeEgressGrants["node-a"]
	if !reflect.DeepEqual(gotGrant.AllowCIDRs, originalCIDROrder) || !reflect.DeepEqual(gotGrant.AllowPorts, originalPortOrder) {
		t.Fatalf("ContentDigest mutated caller-owned grant: %+v", gotGrant)
	}

	changed := validNodeEgressGrantConfig()
	changedGrant := changed.NodeEgressGrants["node-a"]
	changedGrant.AllowPorts[1].To++
	changed.NodeEgressGrants["node-a"] = changedGrant
	changedDigest, err := ContentDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	baselineDigest, err := ContentDigest(validNodeEgressGrantConfig())
	if err != nil {
		t.Fatal(err)
	}
	if changedDigest == baselineDigest {
		t.Fatal("grant rule change did not change content digest")
	}
}

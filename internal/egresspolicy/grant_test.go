package egresspolicy

import (
	"errors"
	"net/netip"
	"testing"
)

func TestPolicySeparatesPublicAndPrivateAuthorization(t *testing.T) {
	policy, err := NewPolicy(
		[]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("2000::/3")},
		[]netip.Prefix{
			netip.MustParsePrefix("10.20.0.0/16"),
			netip.MustParsePrefix("100.64.0.0/10"),
			netip.MustParsePrefix("fd12:3456::/48"),
		},
		nil,
		[]PortRange{{From: 443, To: 443}, {From: 8000, To: 8099}},
	)
	if err != nil {
		t.Fatal(err)
	}

	allowed := []string{"8.8.8.8", "2606:4700:4700::1111", "10.20.9.1", "100.100.99.1", "fd12:3456::1"}
	for _, raw := range allowed {
		if !policy.Allows(netip.MustParseAddr(raw), 443) {
			t.Errorf("expected %s:443 to be allowed", raw)
		}
	}
	denied := []string{"10.21.0.1", "172.16.0.1", "192.168.1.1", "fd13::1"}
	for _, raw := range denied {
		if policy.Allows(netip.MustParseAddr(raw), 443) {
			t.Errorf("public /0 implicitly authorized private address %s", raw)
		}
	}
	if policy.Allows(netip.MustParseAddr("8.8.8.8"), 80) || !policy.Allows(netip.MustParseAddr("8.8.8.8"), 8050) {
		t.Fatal("port range authorization is incorrect")
	}
}

func TestPolicyDenyPrecedesEveryAllowClass(t *testing.T) {
	policy, err := NewPolicy(
		[]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		[]netip.Prefix{netip.MustParsePrefix("8.8.8.0/24"), netip.MustParsePrefix("10.9.0.0/16")},
		[]PortRange{{From: 1, To: 65535}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"8.8.8.8", "10.9.1.1"} {
		if policy.Allows(netip.MustParseAddr(raw), 443) {
			t.Errorf("explicit deny did not override allow for %s", raw)
		}
	}
	if !policy.Allows(netip.MustParseAddr("9.9.9.9"), 443) || !policy.Allows(netip.MustParseAddr("10.8.1.1"), 443) {
		t.Fatal("deny affected an unrelated destination")
	}
}

func TestPolicyHardDenialsCannotBeOverridden(t *testing.T) {
	policy, err := NewPolicy(
		[]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		[]netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("100.64.0.0/10"),
			netip.MustParsePrefix("172.16.0.0/12"),
			netip.MustParsePrefix("192.168.0.0/16"),
			netip.MustParsePrefix("fc00::/7"),
		},
		nil,
		[]PortRange{{From: 1, To: 65535}},
	)
	if err != nil {
		t.Fatal(err)
	}
	addresses := []string{
		"0.0.0.0", "0.0.0.1", "127.0.0.1", "169.254.169.254", "224.0.0.1", "255.255.255.255",
		"100.100.100.200", "168.63.129.16",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1",
		"::", "::1", "::8.8.8.8", "::ffff:8.8.8.8", "fe80::1", "ff02::1",
		"64:ff9b::a00:1", "64:ff9b:1::a00:1", "2001:0000:4136:e378:8000:63bf:3fff:fdd2",
		"2002:0a00:0001::", "100::1", "2001:2::1", "2001:20::1", "2001:db8::1",
		"3fff::1", "fd00:ec2::254", "fec0::1",
	}
	for _, raw := range addresses {
		t.Run(raw, func(t *testing.T) {
			if policy.Allows(netip.MustParseAddr(raw), 443) {
				t.Fatalf("hard-denied address %s was allowed", raw)
			}
		})
	}
	if policy.Allows(netip.MustParseAddr("fe80::1%eth0"), 443) {
		t.Fatal("zoned address was allowed")
	}
	for _, raw := range []string{"8.8.8.8", "10.8.0.1", "2606:4700:4700::1111", "fd12::1"} {
		if !policy.Allows(netip.MustParseAddr(raw), 443) {
			t.Fatalf("positive control %s was denied", raw)
		}
	}
}

func TestNewPolicyRejectsMalformedOrAmbiguousRules(t *testing.T) {
	public := []netip.Prefix{netip.MustParsePrefix("8.0.0.0/8")}
	private := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	ports := []PortRange{{From: 443, To: 443}}
	tests := map[string]func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange){
		"no allow": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return nil, nil, nil, ports
		},
		"no ports": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return public, nil, nil, nil
		},
		"noncanonical public": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return []netip.Prefix{netip.MustParsePrefix("8.8.8.1/24")}, nil, nil, ports
		},
		"mapped public": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return []netip.Prefix{netip.MustParsePrefix("::ffff:0:0/96")}, nil, nil, ports
		},
		"private in public": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return private, nil, nil, ports
		},
		"public in private": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return nil, public, nil, ports
		},
		"duplicate CIDR": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return append(public, public...), nil, nil, ports
		},
		"zero port": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return public, nil, nil, []PortRange{{From: 0, To: 1}}
		},
		"reverse port": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return public, nil, nil, []PortRange{{From: 444, To: 443}}
		},
		"overlapping ports": func() ([]netip.Prefix, []netip.Prefix, []netip.Prefix, []PortRange) {
			return public, nil, nil, []PortRange{{From: 400, To: 450}, {From: 443, To: 443}}
		},
	}
	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			publicAllowed, privateAllowed, denied, allowedPorts := fixture()
			if _, err := NewPolicy(publicAllowed, privateAllowed, denied, allowedPorts); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error = %v, want ErrInvalidPolicy", err)
			}
		})
	}
	if _, err := NewPolicy(public, nil, nil, []PortRange{{From: 1, To: 10}, {From: 11, To: 20}}); err != nil {
		t.Fatalf("adjacent non-overlapping port ranges rejected: %v", err)
	}
}

func TestPolicyOwnsNormalizedCopies(t *testing.T) {
	public := []netip.Prefix{netip.MustParsePrefix("9.0.0.0/8"), netip.MustParsePrefix("8.0.0.0/8")}
	ports := []PortRange{{From: 8000, To: 8099}, {From: 443, To: 443}}
	policy, err := NewPolicy(public, nil, nil, ports)
	if err != nil {
		t.Fatal(err)
	}
	public[0] = netip.MustParsePrefix("10.0.0.0/8")
	ports[1] = PortRange{}
	if !policy.Allows(netip.MustParseAddr("9.1.1.1"), 443) || !policy.Allows(netip.MustParseAddr("8.1.1.1"), 8050) {
		t.Fatal("caller mutation changed compiled policy")
	}
	if DefaultPolicy().Allows(netip.MustParseAddr("8.8.8.8"), 443) {
		t.Fatal("default policy was not deny-all")
	}
}

func TestParseCanonicalPrefixesRejectsAmbiguousWireValues(t *testing.T) {
	got, err := ParseCanonicalPrefixes([]string{"8.0.0.0/8", "2001:4860::/32"})
	if err != nil || len(got) != 2 || got[0] != netip.MustParsePrefix("8.0.0.0/8") {
		t.Fatalf("canonical prefixes = %v, %v", got, err)
	}
	for name, values := range map[string][]string{
		"host bits":  {"8.8.8.1/24"},
		"alternate":  {"2001:0db8::/32"},
		"mapped":     {"::ffff:0:0/96"},
		"whitespace": {" 8.0.0.0/8"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCanonicalPrefixes(values); !errors.Is(err, ErrInvalidPrefix) {
				t.Fatalf("error = %v, want ErrInvalidPrefix", err)
			}
		})
	}
	if _, err := ParseCanonicalPrefixes([]string{"8.0.0.0/8", "8.0.0.0/8"}); !errors.Is(err, ErrDuplicatePrefix) {
		t.Fatalf("duplicate error = %v, want ErrDuplicatePrefix", err)
	}
}

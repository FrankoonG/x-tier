package egresspolicy

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestResolveFreezesCanonicalAllowedCandidates(t *testing.T) {
	lookupCalls := 0
	lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
		lookupCalls++
		if network != "ip" || host != "origin.example" {
			t.Fatalf("lookup = %s %s", network, host)
		}
		return []netip.Addr{
			netip.MustParseAddr("2001:db8::20"),
			netip.MustParseAddr("10.20.0.9"),
			netip.MustParseAddr("10.20.0.9"),
		}, nil
	}
	got, err := Resolve(context.Background(), lookup, DefaultPolicy(), "ORIGIN.example.")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("10.20.0.9"), netip.MustParseAddr("2001:db8::20")}
	if !reflect.DeepEqual(got, want) || lookupCalls != 1 {
		t.Fatalf("candidates=%v calls=%d, want %v and one call", got, lookupCalls, want)
	}
}

func TestResolveLiteralNeverUsesDNS(t *testing.T) {
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("literal target used DNS")
		return nil, nil
	}, DefaultPolicy(), "10.20.0.9")
	if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr("10.20.0.9") {
		t.Fatalf("Resolve literal = %v, %v", got, err)
	}
}

func TestResolveRejectsMixedUnsafeAnswerWithoutReturningAllowedSubset(t *testing.T) {
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}, DefaultPolicy(), "rebind.example")
	if !errors.Is(err, ErrAddressDenied) || got != nil {
		t.Fatalf("Resolve mixed answer = %v, %v", got, err)
	}
}

func TestResolveRejectsSpecialAndMappedLiterals(t *testing.T) {
	for _, host := range []string{
		"0.0.0.0", "127.0.0.1", "169.254.169.254", "224.0.0.1",
		"::", "::1", "fe80::1", "ff02::1", "::ffff:10.20.0.9", "fe80::1%1",
	} {
		t.Run(host, func(t *testing.T) {
			if got, err := Resolve(context.Background(), nil, DefaultPolicy(), host); !errors.Is(err, ErrAddressDenied) || got != nil {
				t.Fatalf("Resolve(%q) = %v, %v", host, got, err)
			}
		})
	}
}

func TestCIDRPolicyRequiresAllowAndHonorsDeny(t *testing.T) {
	if _, err := NewPolicy(nil, nil); err == nil {
		t.Fatal("empty allow policy succeeded")
	}
	policy, err := NewPolicy(
		[]netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
		[]netip.Prefix{netip.MustParsePrefix("10.20.9.0/24")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Allows(netip.MustParseAddr("10.20.8.1")) || policy.Allows(netip.MustParseAddr("10.20.9.1")) || policy.Allows(netip.MustParseAddr("10.21.0.1")) {
		t.Fatal("CIDR allow/deny precedence is incorrect")
	}
}

func TestResolveRejectsInvalidDomainAndCandidateFlood(t *testing.T) {
	for _, host := range []string{"", " bad.example", "bad..example", "-bad.example", "bad_.example"} {
		if _, err := Resolve(context.Background(), nil, DefaultPolicy(), host); !errors.Is(err, ErrInvalidHost) {
			t.Fatalf("Resolve(%q) error=%v", host, err)
		}
	}
	many := make([]netip.Addr, MaxCandidates+1)
	for index := range many {
		many[index] = netip.AddrFrom4([4]byte{10, 20, byte(index / 255), byte(index%255 + 1)})
	}
	if _, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return many, nil
	}, DefaultPolicy(), "many.example"); !errors.Is(err, ErrTooManyCandidates) {
		t.Fatalf("candidate flood error=%v", err)
	}
}

func TestResolvePreservesCancellationWithoutReflectingResolverText(t *testing.T) {
	secret := "resolver-secret.example"
	resolverErr := errors.New(secret)
	_, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, resolverErr
	}, DefaultPolicy(), "safe.example")
	if !errors.Is(err, ErrResolutionFailed) || !errors.Is(err, resolverErr) {
		t.Fatalf("resolver error classification=%v", err)
	}
	if contains := strings.Contains(err.Error(), secret); contains {
		t.Fatalf("resolver text leaked: %v", err)
	}
}

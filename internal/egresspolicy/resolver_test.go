package egresspolicy

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func publicTestPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := NewPolicy(
		[]netip.Prefix{netip.MustParsePrefix("8.0.0.0/8"), netip.MustParsePrefix("2606:4700::/32")},
		nil,
		nil,
		[]PortRange{{From: 443, To: 443}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestResolveFreezesCanonicalAllowedCandidates(t *testing.T) {
	lookupCalls := 0
	lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
		lookupCalls++
		if network != "ip" || host != "origin.example" {
			t.Fatalf("lookup = %s %s", network, host)
		}
		return []netip.Addr{
			netip.MustParseAddr("2606:4700:4700::1111"),
			netip.MustParseAddr("8.8.4.4"),
			netip.MustParseAddr("8.8.4.4"),
		}, nil
	}
	got, err := Resolve(context.Background(), lookup, publicTestPolicy(t), "ORIGIN.example.", 443)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("8.8.4.4"), netip.MustParseAddr("2606:4700:4700::1111")}
	if !reflect.DeepEqual(got, want) || lookupCalls != 1 {
		t.Fatalf("candidates=%v calls=%d, want %v and one call", got, lookupCalls, want)
	}
}

func TestResolveCanonicalizesIDNAExactlyOnce(t *testing.T) {
	calls := 0
	_, err := Resolve(context.Background(), func(_ context.Context, network, host string) ([]netip.Addr, error) {
		calls++
		if network != "ip" || host != "xn--bcher-kva.example" {
			t.Fatalf("lookup = %s %s", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, publicTestPolicy(t), "B\u00dcCHER.example.", 443)
	if err != nil || calls != 1 {
		t.Fatalf("IDNA Resolve error=%v calls=%d", err, calls)
	}
}

func TestResolveLiteralNeverUsesDNS(t *testing.T) {
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("literal target used DNS")
		return nil, nil
	}, publicTestPolicy(t), "8.8.8.8", 443)
	if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr("8.8.8.8") {
		t.Fatalf("Resolve literal = %v, %v", got, err)
	}
}

func TestResolveRejectsPortBeforeDNS(t *testing.T) {
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("unauthorized port reached DNS")
		return nil, nil
	}, publicTestPolicy(t), "origin.example", 80)
	if got != nil || !errors.Is(err, ErrAddressDenied) {
		t.Fatalf("Resolve unauthorized port = %v, %v", got, err)
	}
}

func TestResolveRejectsMixedUnsafeAnswerWithoutReturningAllowedSubset(t *testing.T) {
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}, publicTestPolicy(t), "rebind.example", 443)
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
			if got, err := Resolve(context.Background(), nil, publicTestPolicy(t), host, 443); !errors.Is(err, ErrAddressDenied) || got != nil {
				t.Fatalf("Resolve(%q) = %v, %v", host, got, err)
			}
		})
	}
}

func TestResolveRejectsInvalidDomainAndCandidateFlood(t *testing.T) {
	policy := publicTestPolicy(t)
	for _, host := range []string{"", " bad.example", "bad..example", "-bad.example", "bad_.example"} {
		if _, err := Resolve(context.Background(), nil, policy, host, 443); !errors.Is(err, ErrInvalidHost) {
			t.Fatalf("Resolve(%q) error=%v", host, err)
		}
	}
	many := make([]netip.Addr, MaxCandidates+1)
	for index := range many {
		many[index] = netip.AddrFrom4([4]byte{8, 8, 0, byte(index + 1)})
	}
	if _, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return many, nil
	}, policy, "many.example", 443); !errors.Is(err, ErrTooManyCandidates) {
		t.Fatalf("candidate flood error=%v", err)
	}
}

func TestResolveAppliesLimitAfterDeduplicatingPlatformAnswers(t *testing.T) {
	policy := publicTestPolicy(t)
	duplicateHeavy := make([]netip.Addr, MaxCandidates*3)
	for index := range duplicateHeavy {
		duplicateHeavy[index] = netip.MustParseAddr("8.8.8.8")
	}
	got, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return duplicateHeavy, nil
	}, policy, "duplicates.example", 443)
	if err != nil || !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("8.8.8.8")}) {
		t.Fatalf("duplicate-heavy platform answer = %v, %v", got, err)
	}

	rawFlood := make([]netip.Addr, MaxResolverAnswers+1)
	for index := range rawFlood {
		rawFlood[index] = netip.MustParseAddr("8.8.8.8")
	}
	if _, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return rawFlood, nil
	}, policy, "raw-flood.example", 443); !errors.Is(err, ErrTooManyCandidates) {
		t.Fatalf("raw resolver flood error = %v", err)
	}
}

func TestResolvePreservesCancellationWithoutReflectingResolverText(t *testing.T) {
	secret := "resolver-secret.example"
	resolverErr := errors.New(secret)
	_, err := Resolve(context.Background(), func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, resolverErr
	}, publicTestPolicy(t), "safe.example", 443)
	if !errors.Is(err, ErrResolutionFailed) || !errors.Is(err, resolverErr) {
		t.Fatalf("resolver error classification=%v", err)
	}
	if contains := strings.Contains(err.Error(), secret); contains {
		t.Fatalf("resolver text leaked: %v", err)
	}
}

package egresspolicy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"

	"golang.org/x/net/idna"
)

const MaxCandidates = 16

var (
	ErrInvalidHost       = errors.New("egresspolicy: invalid target host")
	ErrResolutionFailed  = errors.New("egresspolicy: target resolution failed")
	ErrNoCandidates      = errors.New("egresspolicy: target has no address candidates")
	ErrTooManyCandidates = errors.New("egresspolicy: target has too many address candidates")
	ErrAddressDenied     = errors.New("egresspolicy: target address is not permitted")
)

type LookupNetIP func(context.Context, string, string) ([]netip.Addr, error)

type Policy struct {
	allowed []netip.Prefix
	denied  []netip.Prefix
}

func NewPolicy(allowed, denied []netip.Prefix) (Policy, error) {
	if len(allowed) == 0 {
		return Policy{}, errors.New("egresspolicy: at least one allowed CIDR is required")
	}
	policy := Policy{
		allowed: make([]netip.Prefix, 0, len(allowed)),
		denied:  make([]netip.Prefix, 0, len(denied)),
	}
	for _, prefix := range allowed {
		if !prefix.IsValid() || prefix.Addr().Is4In6() {
			return Policy{}, errors.New("egresspolicy: invalid allowed CIDR")
		}
		policy.allowed = append(policy.allowed, prefix.Masked())
	}
	for _, prefix := range denied {
		if !prefix.IsValid() || prefix.Addr().Is4In6() {
			return Policy{}, errors.New("egresspolicy: invalid denied CIDR")
		}
		policy.denied = append(policy.denied, prefix.Masked())
	}
	sortPrefixes(policy.allowed)
	sortPrefixes(policy.denied)
	return policy, nil
}

func DefaultPolicy() Policy {
	policy, err := NewPolicy(
		[]netip.Prefix{
			netip.MustParsePrefix("0.0.0.0/0"),
			netip.MustParsePrefix("::/0"),
		},
		nil,
	)
	if err != nil {
		panic(err)
	}
	return policy
}

func (p Policy) Allows(address netip.Addr) bool {
	if !safeUnicast(address) {
		return false
	}
	for _, prefix := range p.denied {
		if prefix.Contains(address) {
			return false
		}
	}
	for _, prefix := range p.allowed {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func Resolve(ctx context.Context, lookup LookupNetIP, policy Policy, host string) ([]netip.Addr, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !policy.Allows(address) {
			return nil, ErrAddressDenied
		}
		return []netip.Addr{address}, nil
	}
	domain, err := canonicalDomain(host)
	if err != nil {
		return nil, err
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	addresses, err := lookup(ctx, "ip", domain)
	if err != nil {
		return nil, errors.Join(ErrResolutionFailed, opaqueCause{cause: err})
	}
	if len(addresses) == 0 {
		return nil, ErrNoCandidates
	}
	if len(addresses) > MaxCandidates {
		return nil, ErrTooManyCandidates
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !policy.Allows(address) {
			return nil, ErrAddressDenied
		}
		unique[address] = struct{}{}
	}
	result := make([]netip.Addr, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Compare(result[j]) < 0 })
	return result, nil
}

func canonicalDomain(host string) (string, error) {
	if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "\x00\r\n") {
		return "", ErrInvalidHost
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", ErrInvalidHost
	}
	ascii = strings.TrimSuffix(strings.ToLower(ascii), ".")
	if ascii == "" || len(ascii) > 253 {
		return "", ErrInvalidHost
	}
	for _, label := range strings.Split(ascii, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidHost
		}
		for index := range label {
			character := label[index]
			if (character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", ErrInvalidHost
		}
	}
	return ascii, nil
}

func safeUnicast(address netip.Addr) bool {
	return address.IsValid() && address.Zone() == "" && !address.Is4In6() &&
		address.IsGlobalUnicast() && !address.IsUnspecified() && !address.IsLoopback() &&
		!address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast()
}

func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool {
		if compared := prefixes[i].Addr().Compare(prefixes[j].Addr()); compared != 0 {
			return compared < 0
		}
		return prefixes[i].Bits() < prefixes[j].Bits()
	})
}

// opaqueCause preserves cancellation and resolver classification for errors.Is
// without reflecting resolver-controlled names or server text through Error.
type opaqueCause struct{ cause error }

func (e opaqueCause) Error() string { return "resolver cause unavailable" }
func (e opaqueCause) Unwrap() error { return e.cause }

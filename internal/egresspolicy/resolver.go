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

const (
	MaxCandidates      = 16
	MaxResolverAnswers = 64
)

var (
	ErrInvalidHost       = errors.New("egresspolicy: invalid target host")
	ErrResolutionFailed  = errors.New("egresspolicy: target resolution failed")
	ErrNoCandidates      = errors.New("egresspolicy: target has no address candidates")
	ErrTooManyCandidates = errors.New("egresspolicy: target has too many address candidates")
	ErrAddressDenied     = errors.New("egresspolicy: target destination is not permitted")
)

type LookupNetIP func(context.Context, string, string) ([]netip.Addr, error)

// Resolve canonicalizes a terminal-supplied host and freezes its complete DNS
// answer set. Every candidate must be authorized or the whole result is denied.
func Resolve(ctx context.Context, lookup LookupNetIP, policy Policy, host string, port uint16) ([]netip.Addr, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !policy.allowsPort(port) {
		return nil, ErrAddressDenied
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !policy.Allows(address, port) {
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
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		return nil, errors.Join(ErrResolutionFailed, opaqueCause{cause: err})
	}
	if len(addresses) == 0 {
		return nil, ErrNoCandidates
	}
	if len(addresses) > MaxResolverAnswers {
		return nil, ErrTooManyCandidates
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !policy.Allows(address, port) {
			return nil, ErrAddressDenied
		}
		unique[address] = struct{}{}
	}
	if len(unique) > MaxCandidates {
		return nil, ErrTooManyCandidates
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

// opaqueCause preserves cancellation and resolver classification for errors.Is
// without reflecting resolver-controlled names or server text through Error.
type opaqueCause struct{ cause error }

func (e opaqueCause) Error() string { return "resolver cause unavailable" }
func (e opaqueCause) Unwrap() error { return e.cause }

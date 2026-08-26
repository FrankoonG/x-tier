package egresspolicy

import (
	"errors"
	"net/netip"
	"sort"
)

var ErrInvalidPolicy = errors.New("egresspolicy: invalid policy")

var (
	ErrInvalidPrefix   = errors.New("egresspolicy: invalid canonical CIDR")
	ErrDuplicatePrefix = errors.New("egresspolicy: duplicate CIDR")
)

// PortRange is an inclusive TCP destination-port range.
type PortRange struct {
	From uint16
	To   uint16
}

type Policy struct {
	publicAllowed  []netip.Prefix
	privateAllowed []netip.Prefix
	denied         []netip.Prefix
	ports          []PortRange
}

type prefixClass uint8

const (
	prefixClassPublic prefixClass = iota
	prefixClassPrivate
	prefixClassDeny
)

var privatePrefixes = mustPrefixes(
	"10.0.0.0/8",
	"100.64.0.0/10",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
)

// These ranges are never valid egress destinations, even when a broader
// operator CIDR contains them. Private, CGNAT, and ULA ranges are handled
// separately because they may be granted through privateAllowed.
var hardDeniedPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"100.100.100.200/32",
	"127.0.0.0/8",
	"168.63.129.16/32",
	"169.254.0.0/16",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::/96",
	"::1/128",
	"::ffff:0:0/96",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"100:0:0:1::/64",
	"2001:2::/48",
	"2001:10::/28",
	"2001:20::/28",
	"2001::/32",
	"2001:db8::/32",
	"2002::/16",
	"3fff::/20",
	"5f00::/16",
	"fd00:ec2::254/128",
	"fe80::/10",
	"fec0::/10",
	"ff00::/8",
)

// NewPolicy compiles one explicit node egress grant. Public CIDRs cannot
// authorize private space; RFC1918, CGNAT, and ULA require privateAllowed.
func NewPolicy(publicAllowed, privateAllowed, denied []netip.Prefix, ports []PortRange) (Policy, error) {
	if len(publicAllowed) == 0 && len(privateAllowed) == 0 {
		return Policy{}, errors.Join(ErrInvalidPolicy, errors.New("at least one allowed CIDR is required"))
	}
	if len(ports) == 0 {
		return Policy{}, errors.Join(ErrInvalidPolicy, errors.New("at least one allowed port range is required"))
	}

	publicCopy, err := validatePrefixSet(publicAllowed, prefixClassPublic)
	if err != nil {
		return Policy{}, err
	}
	privateCopy, err := validatePrefixSet(privateAllowed, prefixClassPrivate)
	if err != nil {
		return Policy{}, err
	}
	deniedCopy, err := validatePrefixSet(denied, prefixClassDeny)
	if err != nil {
		return Policy{}, err
	}
	portCopy := append([]PortRange(nil), ports...)
	sort.Slice(portCopy, func(i, j int) bool {
		if portCopy[i].From != portCopy[j].From {
			return portCopy[i].From < portCopy[j].From
		}
		return portCopy[i].To < portCopy[j].To
	})
	for index, current := range portCopy {
		if current.From == 0 || current.To == 0 || current.From > current.To {
			return Policy{}, errors.Join(ErrInvalidPolicy, errors.New("invalid allowed port range"))
		}
		if index > 0 && current.From <= portCopy[index-1].To {
			return Policy{}, errors.Join(ErrInvalidPolicy, errors.New("overlapping allowed port ranges"))
		}
	}

	return Policy{
		publicAllowed:  publicCopy,
		privateAllowed: privateCopy,
		denied:         deniedCopy,
		ports:          portCopy,
	}, nil
}

// DefaultPolicy deliberately grants nothing. A caller must compile an
// explicit node egress grant before a destination can be used.
func DefaultPolicy() Policy { return Policy{} }

// ParseCanonicalPrefixes converts a wire-format CIDR list using the same
// canonicality rules consumed by the runtime policy compiler.
func ParseCanonicalPrefixes(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, raw := range values {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() ||
			prefix != prefix.Masked() || prefix.String() != raw {
			return nil, ErrInvalidPrefix
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, ErrDuplicatePrefix
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// Allows reports whether both the literal address and destination port are
// permitted. Explicit denies and non-overridable special ranges take priority.
func (p Policy) Allows(address netip.Addr, port uint16) bool {
	if !p.allowsPort(port) || hardDenied(address) || matchesPrefix(p.denied, address) {
		return false
	}
	if isPrivate(address) {
		return matchesPrefix(p.privateAllowed, address)
	}
	return matchesPrefix(p.publicAllowed, address)
}

func (p Policy) allowsPort(port uint16) bool {
	if port == 0 {
		return false
	}
	index := sort.Search(len(p.ports), func(index int) bool { return p.ports[index].To >= port })
	return index < len(p.ports) && p.ports[index].From <= port
}

func validatePrefixSet(prefixes []netip.Prefix, class prefixClass) ([]netip.Prefix, error) {
	result := append([]netip.Prefix(nil), prefixes...)
	seen := make(map[netip.Prefix]struct{}, len(result))
	for _, prefix := range result {
		if !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			return nil, errors.Join(ErrInvalidPolicy, errors.New("invalid or non-canonical CIDR"))
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, errors.Join(ErrInvalidPolicy, errors.New("duplicate CIDR"))
		}
		seen[prefix] = struct{}{}
		containedByPrivate := prefixContainedByAny(prefix, privatePrefixes)
		if class == prefixClassPrivate && !containedByPrivate {
			return nil, errors.Join(ErrInvalidPolicy, errors.New("private allow CIDR is not RFC1918, CGNAT, or ULA"))
		}
		if class == prefixClassPublic && containedByPrivate {
			return nil, errors.Join(ErrInvalidPolicy, errors.New("private CIDR must use private allow list"))
		}
	}
	sortPrefixes(result)
	return result, nil
}

func hardDenied(address netip.Addr) bool {
	return !address.IsValid() || address.Zone() != "" || address.Is4In6() ||
		!address.IsGlobalUnicast() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() ||
		matchesPrefix(hardDeniedPrefixes, address)
}

func isPrivate(address netip.Addr) bool {
	return matchesPrefix(privatePrefixes, address)
}

func matchesPrefix(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func prefixContainedByAny(prefix netip.Prefix, containers []netip.Prefix) bool {
	for _, container := range containers {
		if prefix.Addr().BitLen() == container.Addr().BitLen() &&
			prefix.Bits() >= container.Bits() && container.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func sortPrefixes(prefixes []netip.Prefix) {
	sort.Slice(prefixes, func(i, j int) bool {
		if compared := prefixes[i].Addr().Compare(prefixes[j].Addr()); compared != 0 {
			return compared < 0
		}
		return prefixes[i].Bits() < prefixes[j].Bits()
	})
}

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for index, value := range values {
		prefixes[index] = netip.MustParsePrefix(value)
	}
	sortPrefixes(prefixes)
	return prefixes
}

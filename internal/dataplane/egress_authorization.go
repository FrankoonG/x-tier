package dataplane

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/egresspolicy"
)

var ErrEgressAuthorizationInvalid = errors.New("dataplane: egress authorization is invalid")

type egressAuthorizationSnapshot struct {
	sourceRevision int64
	digest         [sha256.Size]byte
	policies       map[string]egresspolicy.Policy
}

type canonicalEgressGrant struct {
	SourceNodeID      string                        `json:"source_node_id"`
	Network           string                        `json:"network"`
	AllowCIDRs        []string                      `json:"allow_cidrs,omitempty"`
	AllowPrivateCIDRs []string                      `json:"allow_private_cidrs,omitempty"`
	DenyCIDRs         []string                      `json:"deny_cidrs,omitempty"`
	AllowPorts        []configstore.EgressPortRange `json:"allow_ports"`
}

func compileEgressAuthorization(cfg configstore.Config) (*egressAuthorizationSnapshot, error) {
	keys := make([]string, 0, len(cfg.NodeEgressGrants))
	for sourceNodeID := range cfg.NodeEgressGrants {
		keys = append(keys, sourceNodeID)
	}
	sort.Strings(keys)

	canonical := make([]canonicalEgressGrant, 0, len(keys))
	policies := make(map[string]egresspolicy.Policy, len(keys))
	for _, sourceNodeID := range keys {
		grant := cfg.NodeEgressGrants[sourceNodeID]
		if sourceNodeID == "" || grant.SourceNodeID != sourceNodeID || grant.Network != "tcp" {
			return nil, ErrEgressAuthorizationInvalid
		}
		publicAllowed, err := egresspolicy.ParseCanonicalPrefixes(grant.AllowCIDRs)
		if err != nil {
			return nil, errors.Join(ErrEgressAuthorizationInvalid, err)
		}
		privateAllowed, err := egresspolicy.ParseCanonicalPrefixes(grant.AllowPrivateCIDRs)
		if err != nil {
			return nil, errors.Join(ErrEgressAuthorizationInvalid, err)
		}
		denied, err := egresspolicy.ParseCanonicalPrefixes(grant.DenyCIDRs)
		if err != nil {
			return nil, errors.Join(ErrEgressAuthorizationInvalid, err)
		}
		ports := make([]egresspolicy.PortRange, len(grant.AllowPorts))
		for index, portRange := range grant.AllowPorts {
			ports[index] = egresspolicy.PortRange{From: portRange.From, To: portRange.To}
		}
		policy, err := egresspolicy.NewPolicy(publicAllowed, privateAllowed, denied, ports)
		if err != nil {
			return nil, errors.Join(ErrEgressAuthorizationInvalid, err)
		}
		policies[sourceNodeID] = policy

		encoded := canonicalEgressGrant{
			SourceNodeID:      sourceNodeID,
			Network:           grant.Network,
			AllowCIDRs:        append([]string(nil), grant.AllowCIDRs...),
			AllowPrivateCIDRs: append([]string(nil), grant.AllowPrivateCIDRs...),
			DenyCIDRs:         append([]string(nil), grant.DenyCIDRs...),
			AllowPorts:        append([]configstore.EgressPortRange(nil), grant.AllowPorts...),
		}
		sort.Strings(encoded.AllowCIDRs)
		sort.Strings(encoded.AllowPrivateCIDRs)
		sort.Strings(encoded.DenyCIDRs)
		sort.Slice(encoded.AllowPorts, func(i, j int) bool {
			if encoded.AllowPorts[i].From != encoded.AllowPorts[j].From {
				return encoded.AllowPorts[i].From < encoded.AllowPorts[j].From
			}
			return encoded.AllowPorts[i].To < encoded.AllowPorts[j].To
		})
		canonical = append(canonical, encoded)
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return nil, errors.Join(ErrEgressAuthorizationInvalid, err)
	}
	return &egressAuthorizationSnapshot{
		sourceRevision: cfg.Revision,
		digest:         sha256.Sum256(payload),
		policies:       policies,
	}, nil
}

func newDenyEgressAuthorization(sourceRevision int64) *egressAuthorizationSnapshot {
	return &egressAuthorizationSnapshot{
		sourceRevision: sourceRevision,
		digest:         sha256.Sum256([]byte("xtier:egress-authorization:fail-stop:v1")),
		policies:       map[string]egresspolicy.Policy{},
	}
}

func sameEgressAuthorization(left, right *egressAuthorizationSnapshot) bool {
	return left != nil && right != nil && left.digest == right.digest
}

package route

import (
	"fmt"
	"time"
)

type Reachability string

const (
	ReachabilityAuto           Reachability = "auto"
	ReachabilityDirect         Reachability = "direct"
	ReachabilityPunched        Reachability = "punched"
	ReachabilityMapped         Reachability = "mapped"
	ReachabilityReverseSession Reachability = "reverse_session"
)

type LogicalPath struct {
	ID                 string   `json:"path_id"`
	NodeIDs            []NodeID `json:"node_ids"`
	TerminalNodeID     NodeID   `json:"terminal_node_id"`
	Explicit           bool     `json:"explicit"`
	PathPolicyRevision uint64   `json:"path_policy_revision"`
}

type EdgeConstraint struct {
	ID                string        `json:"id"`
	EdgeIndex         int           `json:"edge_index"`
	FromNodeID        NodeID        `json:"from_node_id"`
	ToNodeID          NodeID        `json:"to_node_id"`
	SourceOfferID     string        `json:"source_offer_id,omitempty"`
	LocalDialSourceID string        `json:"local_dial_source_id,omitempty"`
	TargetInboundID   string        `json:"target_inbound_id,omitempty"`
	LocatorAllowlist  []string      `json:"locator_allowlist,omitempty"`
	Reachability      Reachability  `json:"reachability"`
	AddressFamilies   []string      `json:"address_families,omitempty"`
	Protocols         []string      `json:"protocols,omitempty"`
	MaxParallelChecks int           `json:"max_parallel_checks,omitempty"`
	Deadline          time.Duration `json:"deadline,omitempty"`
}

type TransitPolicy string

const (
	TransitPolicyAuto    TransitPolicy = "auto"
	TransitPolicyForward TransitPolicy = "forward"
	TransitPolicyRelay   TransitPolicy = "relay"
	TransitPolicyOrdered TransitPolicy = "ordered"
)

type TransitMode string

const (
	TransitModeForward TransitMode = "forward"
	TransitModeRelay   TransitMode = "relay"
)

type TransitBackend string

const (
	TransitBackendKernelNftables  TransitBackend = "kernel_nftables"
	TransitBackendUserspacePacket TransitBackend = "userspace_packet"
	TransitBackendUserspaceStream TransitBackend = "userspace_stream"
	TransitBackendRoutedUnderlay  TransitBackend = "routed_underlay"
	TransitBackendProtocol        TransitBackend = "protocol"
)

type TransitExecutionCandidate struct {
	Mode    TransitMode    `json:"mode"`
	Backend TransitBackend `json:"backend"`
}

func (c TransitExecutionCandidate) Validate() error {
	switch c.Mode {
	case TransitModeForward:
		if c.Backend == TransitBackendProtocol {
			return fmt.Errorf("route.transit_backend_mode_mismatch: %s requires relay mode", c.Backend)
		}
		switch c.Backend {
		case TransitBackendKernelNftables, TransitBackendUserspacePacket,
			TransitBackendUserspaceStream, TransitBackendRoutedUnderlay:
			return nil
		}
	case TransitModeRelay:
		if c.Backend == TransitBackendProtocol {
			return nil
		}
	}
	return fmt.Errorf("route.transit_backend_mode_mismatch: mode=%s backend=%s", c.Mode, c.Backend)
}

type TransitConstraint struct {
	ID                  string                      `json:"id"`
	NodeID              NodeID                      `json:"node_id"`
	RequestedPolicy     TransitPolicy               `json:"requested_policy"`
	ExecutionPreference []TransitExecutionCandidate `json:"execution_preference"`
	OfferID             string                      `json:"offer_id,omitempty"`
	RequireSourceFilter bool                        `json:"require_source_filter"`
	MaxLease            time.Duration               `json:"max_lease,omitempty"`
}

// TransitReleaseScope is generated from the signed release manifest. Order is
// part of the contract; it must never come from map iteration or live health.
type TransitReleaseScope struct {
	Stream []TransitExecutionCandidate `json:"stream"`
	Packet []TransitExecutionCandidate `json:"packet"`
}

func (s TransitReleaseScope) Candidates(kind SessionKind) ([]TransitExecutionCandidate, error) {
	var candidates []TransitExecutionCandidate
	switch kind {
	case SessionKindStream:
		candidates = s.Stream
	case SessionKindPacket:
		candidates = s.Packet
	default:
		return nil, fmt.Errorf("route.session_kind_unknown: %q", kind)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("route.transit_release_scope_empty: %s", kind)
	}
	seen := make(map[TransitExecutionCandidate]struct{}, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[candidate]; duplicate {
			return nil, fmt.Errorf("route.transit_release_scope_duplicate: mode=%s backend=%s", candidate.Mode, candidate.Backend)
		}
		seen[candidate] = struct{}{}
	}
	return append([]TransitExecutionCandidate(nil), candidates...), nil
}

func (c TransitConstraint) Normalize(kind SessionKind, scope TransitReleaseScope) (TransitConstraint, error) {
	allowed, err := scope.Candidates(kind)
	if err != nil {
		return TransitConstraint{}, err
	}
	policy := c.RequestedPolicy
	if policy == "" {
		policy = TransitPolicyAuto
	}

	var normalized []TransitExecutionCandidate
	switch policy {
	case TransitPolicyAuto:
		normalized = allowed
	case TransitPolicyForward:
		for _, candidate := range allowed {
			if candidate.Mode == TransitModeForward {
				normalized = append(normalized, candidate)
			}
		}
	case TransitPolicyRelay:
		for _, candidate := range allowed {
			if candidate.Mode == TransitModeRelay {
				normalized = append(normalized, candidate)
			}
		}
	case TransitPolicyOrdered:
		if len(c.ExecutionPreference) == 0 {
			return TransitConstraint{}, fmt.Errorf("route.transit_ordered_empty")
		}
		allowedSet := make(map[TransitExecutionCandidate]struct{}, len(allowed))
		for _, candidate := range allowed {
			allowedSet[candidate] = struct{}{}
		}
		seen := make(map[TransitExecutionCandidate]struct{}, len(c.ExecutionPreference))
		for _, candidate := range c.ExecutionPreference {
			if err := candidate.Validate(); err != nil {
				return TransitConstraint{}, err
			}
			if _, ok := allowedSet[candidate]; !ok {
				return TransitConstraint{}, fmt.Errorf("route.transit_not_in_release_scope: mode=%s backend=%s", candidate.Mode, candidate.Backend)
			}
			if _, duplicate := seen[candidate]; duplicate {
				return TransitConstraint{}, fmt.Errorf("route.transit_preference_duplicate: mode=%s backend=%s", candidate.Mode, candidate.Backend)
			}
			seen[candidate] = struct{}{}
			normalized = append(normalized, candidate)
		}
	default:
		return TransitConstraint{}, fmt.Errorf("route.transit_policy_unknown: %q", policy)
	}
	if len(normalized) == 0 {
		return TransitConstraint{}, fmt.Errorf("route.transit_policy_unavailable: policy=%s session=%s", policy, kind)
	}
	c.RequestedPolicy = policy
	c.ExecutionPreference = append([]TransitExecutionCandidate(nil), normalized...)
	return c, nil
}

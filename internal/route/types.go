package route

import "strings"

type NodeID string

func (id NodeID) String() string {
	return string(id)
}

type Direction string

const (
	DirectionInbound       Direction = "inbound"
	DirectionOutbound      Direction = "outbound"
	DirectionBidirectional Direction = "bidirectional"
)

func (d Direction) CanDialOutbound() bool {
	return d == DirectionOutbound || d == DirectionBidirectional
}

func (d Direction) CanAcceptInbound() bool {
	return d == DirectionInbound || d == DirectionBidirectional
}

type CarrierKind string

const (
	CarrierDirect        CarrierKind = "direct"
	CarrierRelayChain    CarrierKind = "relay_chain"
	CarrierPunchedDirect CarrierKind = "punched_direct"
)

// CarrierKind is retained for the May 2026 relay prototype. New route code
// must describe edge reachability and transit execution independently.

type EndpointKind string

const (
	EndpointRendrStream EndpointKind = "rendr_stream"
	EndpointRendrPacket EndpointKind = "rendr_packet"
	EndpointEgress      EndpointKind = "egress"
)

type SessionKind string

const (
	SessionKindStream SessionKind = "stream"
	SessionKindPacket SessionKind = "packet"
)

func (k EndpointKind) SessionKind() (SessionKind, bool) {
	switch k {
	case EndpointRendrStream, EndpointEgress:
		return SessionKindStream, true
	case EndpointRendrPacket:
		return SessionKindPacket, true
	default:
		return "", false
	}
}

type Strategy string

const (
	StrategySelector Strategy = "selector"
	StrategyRace     Strategy = "race"
	StrategyBond     Strategy = "bond"
	StrategyPeak     Strategy = "peak"
)

type Node struct {
	ID            NodeID `json:"id"`
	DisplayName   string `json:"display_name,omitempty"`
	RendrCapable  bool   `json:"rendr_capable"`
	InstanceID    string `json:"runtime_instance_id,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
	DisabledCause string `json:"disabled_cause,omitempty"`
}

type Edge struct {
	From          NodeID    `json:"from"`
	To            NodeID    `json:"to"`
	PeerName      string    `json:"peer_name,omitempty"`
	Direction     Direction `json:"direction"`
	XrayProfileID string    `json:"xray_profile_id,omitempty"`
	GatewayAddr   string    `json:"gateway_addr,omitempty"`
	NestedEnabled bool      `json:"nested_enabled"`
	Enabled       bool      `json:"enabled"`
	DisabledCause string    `json:"disabled_cause,omitempty"`
}

type Topology struct {
	Local NodeID          `json:"local"`
	Nodes map[NodeID]Node `json:"nodes"`
	Edges []Edge          `json:"edges"`
}

func (t *Topology) AddNode(n Node) {
	if t.Nodes == nil {
		t.Nodes = map[NodeID]Node{}
	}
	t.Nodes[n.ID] = n
}

func (t *Topology) AddEdge(e Edge) {
	t.Edges = append(t.Edges, e)
}

func (t Topology) Node(id NodeID) (Node, bool) {
	n, ok := t.Nodes[id]
	return n, ok
}

func (t Topology) Edge(from, to NodeID) (Edge, bool) {
	for _, e := range t.Edges {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return Edge{}, false
}

func (t Topology) DialEdge(from, to NodeID) (Edge, bool) {
	if e, ok := t.Edge(from, to); ok && e.Direction.CanDialOutbound() {
		return e, true
	}
	if e, ok := t.Edge(to, from); ok && e.Direction.CanAcceptInbound() {
		return reverseDialEdge(e), true
	}
	return Edge{}, false
}

func reverseDialEdge(e Edge) Edge {
	e.From, e.To = e.To, e.From
	e.PeerName = e.To.String()
	switch e.Direction {
	case DirectionInbound:
		e.Direction = DirectionOutbound
	case DirectionBidirectional:
		e.Direction = DirectionBidirectional
	}
	return e
}

type RouteIntent struct {
	Paths        []string     `json:"paths"`
	Strategy     Strategy     `json:"strategy"`
	EndpointKind EndpointKind `json:"endpoint_kind"`
	PrimaryPath  string       `json:"primary_path,omitempty"`
}

type ResolvedPath struct {
	ID                         string       `json:"id"`
	Expression                 string       `json:"expression"`
	Hops                       []NodeID     `json:"hops"`
	FinalPeer                  NodeID       `json:"final_peer"`
	RendrTerminal              NodeID       `json:"rendr_terminal"`
	ExpectedTerminalInstanceID string       `json:"expected_terminal_runtime_instance_id,omitempty"`
	CarrierKind                CarrierKind  `json:"legacy_carrier_kind,omitempty"`
	CarrierEntry               NodeID       `json:"legacy_carrier_entry,omitempty"`
	EndpointKind               EndpointKind `json:"endpoint_kind"`
	SessionKind                SessionKind  `json:"session_kind"`
	LeafTransport              string       `json:"leaf_transport"`
	Dialable                   bool         `json:"legacy_dialable"`
	DisabledReason             string       `json:"disabled_reason,omitempty"`
	Edges                      []Edge       `json:"edges"`
}

func (p ResolvedPath) Name() string {
	if p.ID != "" {
		return p.ID
	}
	return PathID(p.Expression)
}

func PathID(expr string) string {
	expr = strings.TrimSpace(expr)
	expr = strings.ReplaceAll(expr, "/", "-")
	expr = strings.ReplaceAll(expr, "\\", "-")
	expr = strings.ReplaceAll(expr, " ", "-")
	for strings.Contains(expr, "--") {
		expr = strings.ReplaceAll(expr, "--", "-")
	}
	return strings.Trim(expr, "-")
}

type TargetKind string

const (
	TargetPath     TargetKind = "path"
	TargetSelector TargetKind = "selector"
	TargetRace     TargetKind = "race"
	TargetBond     TargetKind = "bond"
	TargetPeak     TargetKind = "peak"
)

// RouteLeafDescriptor is the immutable end-to-end lane handed to a runtime
// adapter. LogicalPath retains the complete hop and edge realization.
type RouteLeafDescriptor struct {
	ID                        string       `json:"id"`
	Generation                uint64       `json:"generation"`
	LogicalPathID             string       `json:"logical_path_id"`
	LogicalPath               ResolvedPath `json:"logical_path"`
	TerminalNodeID            NodeID       `json:"terminal_node_id"`
	ExpectedRuntimeInstanceID string       `json:"expected_runtime_instance_id,omitempty"`
	SessionKind               SessionKind  `json:"session_kind"`
	EdgeConstraintRefs        []string     `json:"edge_constraint_refs,omitempty"`
	TransitConstraintRefs     []string     `json:"transit_constraint_refs,omitempty"`
	AuthPolicyRevision        uint64       `json:"auth_policy_revision,omitempty"`
}

func (d RouteLeafDescriptor) Name() string {
	return d.LogicalPath.Name()
}

type TargetSummary struct {
	Name       string               `json:"name"`
	Kind       TargetKind           `json:"kind"`
	Children   []TargetSummary      `json:"children,omitempty"`
	Descriptor *RouteLeafDescriptor `json:"descriptor,omitempty"`
}

type CompiledRoute struct {
	Intent        RouteIntent           `json:"intent"`
	ResolvedPaths []ResolvedPath        `json:"resolved_paths"`
	Leaves        []RouteLeafDescriptor `json:"leaves"`
	SessionKind   SessionKind           `json:"session_kind"`
	Target        TargetSummary         `json:"target"`
}

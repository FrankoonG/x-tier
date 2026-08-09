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

type EndpointKind string

const (
	EndpointRendrStream EndpointKind = "rendr_stream"
	EndpointRendrPacket EndpointKind = "rendr_packet"
	EndpointEgress      EndpointKind = "egress"
)

type Strategy string

const (
	StrategySelector Strategy = "selector"
	StrategyRace     Strategy = "race"
	StrategyBond     Strategy = "bond"
	StrategyPeak     Strategy = "peak"
)

type Node struct {
	ID            NodeID
	DisplayName   string
	RendrCapable  bool
	InstanceID    string
	Disabled      bool
	DisabledCause string
}

type Edge struct {
	From          NodeID
	To            NodeID
	PeerName      string
	Direction     Direction
	XrayProfileID string
	GatewayAddr   string
	NestedEnabled bool
	Enabled       bool
	DisabledCause string
}

type Topology struct {
	Local NodeID
	Nodes map[NodeID]Node
	Edges []Edge
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
	Paths        []string
	Strategy     Strategy
	EndpointKind EndpointKind
	PrimaryPath  string
}

type ResolvedPath struct {
	ID                         string
	Expression                 string
	Hops                       []NodeID
	FinalPeer                  NodeID
	RendrTerminal              NodeID
	ExpectedTerminalInstanceID string
	CarrierKind                CarrierKind
	CarrierEntry               NodeID
	EndpointKind               EndpointKind
	LeafTransport              string
	Dialable                   bool
	DisabledReason             string
	Edges                      []Edge
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
)

type TargetSummary struct {
	Name     string
	Kind     TargetKind
	Children []TargetSummary
	Path     *ResolvedPath
}

type CompiledRoute struct {
	Intent        RouteIntent
	ResolvedPaths []ResolvedPath
	Target        TargetSummary
}

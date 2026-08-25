package route

import (
	"strings"
)

const leafTransport = "xtier-chain"

func ResolveIntent(t Topology, intent RouteIntent) ([]ResolvedPath, error) {
	if t.Local == "" {
		return nil, errf("topology.local_missing", "", "local node id is required")
	}
	if len(intent.Paths) == 0 {
		return nil, errf("route.paths_empty", "", "at least one path is required")
	}
	endpoint := intent.EndpointKind
	if endpoint == "" {
		endpoint = EndpointRendrStream
	}
	out := make([]ResolvedPath, 0, len(intent.Paths))
	for _, expr := range intent.Paths {
		rp, err := ResolvePath(t, expr, endpoint)
		if err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, nil
}

func ResolvePath(t Topology, expr string, endpoint EndpointKind) (ResolvedPath, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ResolvedPath{}, errf("path.empty", expr, "path expression is empty")
	}
	parts := splitPath(expr)
	if len(parts) == 0 {
		return ResolvedPath{}, errf("path.empty", expr, "path expression is empty")
	}
	sessionKind, ok := endpoint.SessionKind()
	if !ok {
		return ResolvedPath{}, errf("route.endpoint_unknown", expr, "unknown endpoint kind %q", endpoint)
	}
	hops := make([]NodeID, 0, len(parts)+1)
	hops = append(hops, t.Local)
	for _, part := range parts {
		hops = append(hops, NodeID(part))
	}

	seen := map[NodeID]bool{}
	for _, hop := range hops {
		if seen[hop] {
			return ResolvedPath{}, errf("path.cycle", expr, "node %s appears more than once", hop)
		}
		seen[hop] = true
	}

	edges := make([]Edge, 0, len(hops)-1)
	for i := 0; i < len(hops)-1; i++ {
		from, to := hops[i], hops[i+1]
		n, ok := t.Node(to)
		if !ok {
			return ResolvedPath{}, errf("path.unknown_node", expr, "node %s is not known", to)
		}
		if n.Disabled {
			reason := n.DisabledCause
			if reason == "" {
				reason = "node is disabled"
			}
			return ResolvedPath{}, errf("path.node_disabled", expr, "node %s is disabled: %s", to, reason)
		}
		edge, ok := t.DialEdge(from, to)
		if !ok {
			if exact, exactOK := t.Edge(from, to); exactOK && !exact.Direction.CanDialOutbound() {
				return ResolvedPath{}, errf("path.edge_not_outbound", expr, "edge %s -> %s is %s, not outbound", from, to, exact.Direction)
			}
			return ResolvedPath{}, errf("path.edge_missing", expr, "edge %s -> %s is not known", from, to)
		}
		if !edge.Enabled {
			reason := edge.DisabledCause
			if reason == "" {
				reason = "edge is disabled"
			}
			return ResolvedPath{}, errf("path.edge_disabled", expr, "edge %s -> %s is disabled: %s", from, to, reason)
		}
		if !edge.Direction.CanDialOutbound() {
			return ResolvedPath{}, errf("path.edge_not_outbound", expr, "edge %s -> %s is %s, not outbound", from, to, edge.Direction)
		}
		if i < len(hops)-2 && !edge.NestedEnabled {
			return ResolvedPath{}, errf("path.nested_disabled", expr, "edge %s -> %s does not allow nested expansion", from, to)
		}
		edges = append(edges, edge)
	}

	final := hops[len(hops)-1]
	finalNode, ok := t.Node(final)
	if !ok {
		return ResolvedPath{}, errf("path.unknown_node", expr, "final node %s is not known", final)
	}
	if endpoint == EndpointRendrStream || endpoint == EndpointRendrPacket {
		if !finalNode.RendrCapable {
			return ResolvedPath{}, errf("path.terminal_not_rendr", expr, "final node %s does not advertise rendr capability", final)
		}
	}

	carrier := CarrierDirect
	if len(hops) > 2 {
		carrier = CarrierRelayChain
	}
	return ResolvedPath{
		ID:                         PathID(expr),
		Expression:                 expr,
		Hops:                       hops,
		FinalPeer:                  final,
		RendrTerminal:              final,
		ExpectedTerminalInstanceID: finalNode.InstanceID,
		CarrierKind:                carrier,
		CarrierEntry:               hops[1],
		EndpointKind:               endpoint,
		SessionKind:                sessionKind,
		LeafTransport:              leafTransport,
		Dialable:                   true,
		Edges:                      edges,
	}, nil
}

func splitPath(expr string) []string {
	raw := strings.FieldsFunc(expr, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

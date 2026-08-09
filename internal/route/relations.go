package route

import (
	"fmt"
	"sort"
	"strings"
)

type NodePeerRelations struct {
	Node          NodeID
	Inbound       []NodeID
	Outbound      []NodeID
	Bidirectional []NodeID
}

func (r NodePeerRelations) DirectionTo(peer NodeID) (Direction, bool) {
	if containsNodeID(r.Bidirectional, peer) {
		return DirectionBidirectional, true
	}
	if containsNodeID(r.Outbound, peer) {
		return DirectionOutbound, true
	}
	if containsNodeID(r.Inbound, peer) {
		return DirectionInbound, true
	}
	return "", false
}

func PeerRelations(t Topology) map[NodeID]NodePeerRelations {
	views := map[NodeID]*relationBuilder{}
	for id := range t.Nodes {
		views[id] = &relationBuilder{}
	}
	for _, e := range t.Edges {
		ensureRelationBuilder(views, e.From)
		ensureRelationBuilder(views, e.To)
		switch e.Direction {
		case DirectionOutbound:
			views[e.From].out[e.To] = true
			views[e.To].in[e.From] = true
		case DirectionInbound:
			views[e.From].in[e.To] = true
			views[e.To].out[e.From] = true
		case DirectionBidirectional:
			views[e.From].in[e.To] = true
			views[e.From].out[e.To] = true
			views[e.To].in[e.From] = true
			views[e.To].out[e.From] = true
		}
	}

	out := make(map[NodeID]NodePeerRelations, len(views))
	for id, b := range views {
		rel := NodePeerRelations{Node: id}
		for peer := range b.in {
			if b.out[peer] {
				rel.Bidirectional = append(rel.Bidirectional, peer)
				continue
			}
			rel.Inbound = append(rel.Inbound, peer)
		}
		for peer := range b.out {
			if b.in[peer] {
				continue
			}
			rel.Outbound = append(rel.Outbound, peer)
		}
		sortNodeIDs(rel.Inbound)
		sortNodeIDs(rel.Outbound)
		sortNodeIDs(rel.Bidirectional)
		out[id] = rel
	}
	return out
}

func DescribePeerRelations(t Topology) []string {
	relations := PeerRelations(t)
	nodes := make([]NodeID, 0, len(relations))
	for id := range relations {
		nodes = append(nodes, id)
	}
	sortNodeIDs(nodes)

	lines := make([]string, 0, len(nodes))
	for _, id := range nodes {
		r := relations[id]
		lines = append(lines, fmt.Sprintf("%s: in=[%s] out=[%s] bidir=[%s]",
			id, joinNodeIDs(r.Inbound), joinNodeIDs(r.Outbound), joinNodeIDs(r.Bidirectional)))
	}
	return lines
}

type relationBuilder struct {
	in  map[NodeID]bool
	out map[NodeID]bool
}

func ensureRelationBuilder(views map[NodeID]*relationBuilder, id NodeID) {
	if _, ok := views[id]; !ok {
		views[id] = &relationBuilder{}
	}
	if views[id].in == nil {
		views[id].in = map[NodeID]bool{}
	}
	if views[id].out == nil {
		views[id].out = map[NodeID]bool{}
	}
}

func sortNodeIDs(ids []NodeID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

func joinNodeIDs(ids []NodeID) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id.String())
	}
	return strings.Join(parts, ",")
}

func containsNodeID(ids []NodeID, target NodeID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

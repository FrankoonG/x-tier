package localview

import (
	"sort"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TopologyFromConfig(cfg configstore.Config) route.Topology {
	local := route.NodeID(cfg.Node.NodeID)
	t := route.Topology{Local: local}
	if local != "" {
		t.AddNode(route.Node{
			ID:            local,
			DisplayName:   cfg.Node.DisplayName,
			RendrCapable:  cfg.Node.RendrCapable,
			InstanceID:    cfg.Node.RendrInstanceID,
			Disabled:      cfg.Node.Disabled,
			DisabledCause: cfg.Node.DisabledCause,
		})
	}
	addPeers(&t, local, cfg.Peers)
	return t
}

func addPeers(t *route.Topology, parent route.NodeID, peers []configstore.PeerConfig) {
	for _, p := range peers {
		id := route.NodeID(p.NodeID)
		t.AddNode(route.Node{
			ID:           id,
			DisplayName:  p.DisplayName,
			RendrCapable: p.RendrCapable,
			InstanceID:   p.InstanceID,
		})
		if parent != "" {
			gateway := p.GatewayAddr
			if gateway == "" {
				gateway = p.Addr
			}
			t.AddEdge(route.Edge{
				From:          parent,
				To:            id,
				PeerName:      p.Name,
				Direction:     p.Direction,
				XrayProfileID: p.XrayProfileID,
				GatewayAddr:   gateway,
				NestedEnabled: p.NestedEnabled,
				Enabled:       p.Enabled,
				DisabledCause: p.DisabledCause,
			})
		}
		addPeers(t, id, p.Children)
	}
}

func TopologyLines(t route.Topology) []string {
	lines := route.DescribePeerRelations(t)
	sort.Strings(lines)
	return lines
}

package route

import "testing"

func baseTopology() Topology {
	topo := Topology{Local: "A"}
	for _, n := range []Node{
		{ID: "A", RendrCapable: true, InstanceID: "inst-A"},
		{ID: "B", RendrCapable: true, InstanceID: "inst-B"},
		{ID: "C", RendrCapable: true, InstanceID: "inst-C"},
		{ID: "D", RendrCapable: true, InstanceID: "inst-D"},
		{ID: "E", RendrCapable: true, InstanceID: "inst-E"},
	} {
		topo.AddNode(n)
	}
	return topo
}

func addOutbound(topo *Topology, from, to NodeID, nested bool) {
	topo.AddEdge(Edge{From: from, To: to, Direction: DirectionOutbound, Enabled: true, NestedEnabled: nested, XrayProfileID: "XR01"})
}

func TestResolveDirectAndRelayAreDistinctPaths(t *testing.T) {
	topo := baseTopology()
	addOutbound(&topo, "A", "B", true)
	addOutbound(&topo, "A", "C", true)
	addOutbound(&topo, "B", "C", true)

	ab, err := ResolvePath(topo, "B", EndpointRendrStream)
	if err != nil {
		t.Fatalf("resolve A->B: %v", err)
	}
	ac, err := ResolvePath(topo, "C", EndpointRendrStream)
	if err != nil {
		t.Fatalf("resolve A->C: %v", err)
	}
	abc, err := ResolvePath(topo, "B/C", EndpointRendrStream)
	if err != nil {
		t.Fatalf("resolve A->B->C: %v", err)
	}

	if ab.RendrTerminal != "B" || ab.CarrierEntry != "B" || ab.CarrierKind != CarrierDirect {
		t.Fatalf("A->B terminal/carrier mismatch: %+v", ab)
	}
	if ac.RendrTerminal != "C" || ac.CarrierEntry != "C" || ac.CarrierKind != CarrierDirect {
		t.Fatalf("A->C terminal/carrier mismatch: %+v", ac)
	}
	if abc.RendrTerminal != "C" || abc.CarrierEntry != "B" || abc.CarrierKind != CarrierRelayChain {
		t.Fatalf("A->B->C terminal/carrier mismatch: %+v", abc)
	}
}

func TestInboundOnlyEdgeIsNotDialable(t *testing.T) {
	topo := baseTopology()
	topo.AddEdge(Edge{From: "A", To: "B", Direction: DirectionInbound, Enabled: true, NestedEnabled: true})

	_, err := ResolvePath(topo, "B", EndpointRendrStream)
	if err == nil {
		t.Fatal("expected inbound-only edge to be rejected")
	}
	assertCompileCode(t, err, "path.edge_not_outbound")
}

func TestInboundEdgeIsOutboundFromRemoteNodeView(t *testing.T) {
	topo := baseTopology()
	topo.Local = "B"
	topo.AddEdge(Edge{From: "A", To: "B", Direction: DirectionInbound, Enabled: true, NestedEnabled: true, XrayProfileID: "XR01"})
	addOutbound(&topo, "B", "C", true)

	ba, err := ResolvePath(topo, "A", EndpointRendrStream)
	if err != nil {
		t.Fatalf("resolve B->A over A<-B relation: %v", err)
	}
	if ba.CarrierEntry != "A" || ba.CarrierKind != CarrierDirect {
		t.Fatalf("unexpected B->A resolved path: %+v", ba)
	}
	bc, err := ResolvePath(topo, "C", EndpointRendrStream)
	if err != nil {
		t.Fatalf("resolve B->C: %v", err)
	}
	if bc.CarrierEntry != "C" || bc.CarrierKind != CarrierDirect {
		t.Fatalf("unexpected B->C resolved path: %+v", bc)
	}
}

func TestPeerRelationsRenderInboundAndOutboundNodeView(t *testing.T) {
	topo := baseTopology()
	topo.AddEdge(Edge{From: "A", To: "B", Direction: DirectionInbound, Enabled: true, NestedEnabled: true})
	addOutbound(&topo, "B", "C", true)

	relations := PeerRelations(topo)
	b := relations["B"]
	if dir, ok := b.DirectionTo("A"); !ok || dir != DirectionOutbound {
		t.Fatalf("B->A direction = %s %t, want outbound", dir, ok)
	}
	if dir, ok := b.DirectionTo("C"); !ok || dir != DirectionOutbound {
		t.Fatalf("B->C direction = %s %t, want outbound", dir, ok)
	}
	a := relations["A"]
	if dir, ok := a.DirectionTo("B"); !ok || dir != DirectionInbound {
		t.Fatalf("A<-B direction = %s %t, want inbound", dir, ok)
	}
	c := relations["C"]
	if dir, ok := c.DirectionTo("B"); !ok || dir != DirectionInbound {
		t.Fatalf("C<-B direction = %s %t, want inbound", dir, ok)
	}
}

func TestNestedGateBlocksExpansionButNotDirectPath(t *testing.T) {
	topo := baseTopology()
	addOutbound(&topo, "A", "B", false)
	addOutbound(&topo, "B", "C", true)

	if _, err := ResolvePath(topo, "B", EndpointRendrStream); err != nil {
		t.Fatalf("direct A->B should not require nested expansion: %v", err)
	}
	_, err := ResolvePath(topo, "B/C", EndpointRendrStream)
	if err == nil {
		t.Fatal("expected nested=false to reject B/C")
	}
	assertCompileCode(t, err, "path.nested_disabled")
}

func TestCycleIsRejected(t *testing.T) {
	topo := baseTopology()
	addOutbound(&topo, "A", "B", true)
	addOutbound(&topo, "B", "C", true)
	addOutbound(&topo, "C", "A", true)

	_, err := ResolvePath(topo, "B/C/A", EndpointRendrStream)
	if err == nil {
		t.Fatal("expected cycle to be rejected")
	}
	assertCompileCode(t, err, "path.cycle")
}

func TestBondAllowsDirectAndRelaySameTerminal(t *testing.T) {
	topo := baseTopology()
	addOutbound(&topo, "A", "D", true)
	addOutbound(&topo, "A", "B", true)
	addOutbound(&topo, "B", "D", true)

	compiled, err := Compile(topo, RouteIntent{
		Paths:        []string{"D", "B/D"},
		Strategy:     StrategyBond,
		EndpointKind: EndpointRendrStream,
	})
	if err != nil {
		t.Fatalf("compile bond D + B/D: %v", err)
	}
	if compiled.Target.Kind != TargetBond {
		t.Fatalf("target kind = %s, want bond", compiled.Target.Kind)
	}
	if len(compiled.ResolvedPaths) != 2 {
		t.Fatalf("resolved paths = %d, want 2", len(compiled.ResolvedPaths))
	}
	if compiled.ResolvedPaths[0].RendrTerminal != "D" || compiled.ResolvedPaths[1].RendrTerminal != "D" {
		t.Fatalf("terminals differ: %+v", compiled.ResolvedPaths)
	}
	if compiled.ResolvedPaths[0].CarrierKind != CarrierDirect || compiled.ResolvedPaths[1].CarrierKind != CarrierRelayChain {
		t.Fatalf("carrier kinds mismatch: %+v", compiled.ResolvedPaths)
	}
}

func TestBondRejectsTerminalInstanceMismatch(t *testing.T) {
	topo := baseTopology()
	topo.AddNode(Node{ID: "D2", DisplayName: "D", RendrCapable: true, InstanceID: "inst-D2"})
	addOutbound(&topo, "A", "B", true)
	addOutbound(&topo, "A", "C", true)
	addOutbound(&topo, "B", "D", true)
	addOutbound(&topo, "C", "D2", true)

	_, err := Compile(topo, RouteIntent{
		Paths:        []string{"B/D", "C/D2"},
		Strategy:     StrategyBond,
		EndpointKind: EndpointRendrStream,
	})
	if err == nil {
		t.Fatal("expected bond mismatch to be rejected")
	}
	assertCompileCode(t, err, "route.bond_final_mismatch")
}

func TestC001LineTopologyCompiles(t *testing.T) {
	topo := baseTopology()
	addOutbound(&topo, "A", "B", true)
	addOutbound(&topo, "B", "C", true)
	addOutbound(&topo, "C", "D", true)
	addOutbound(&topo, "D", "E", true)

	compiled, err := Compile(topo, RouteIntent{
		Paths:        []string{"B/C/D/E"},
		Strategy:     StrategySelector,
		EndpointKind: EndpointRendrStream,
	})
	if err != nil {
		t.Fatalf("compile C001 line topology: %v", err)
	}
	got := compiled.ResolvedPaths[0]
	if got.RendrTerminal != "E" || got.CarrierEntry != "B" || got.CarrierKind != CarrierRelayChain {
		t.Fatalf("unexpected resolved C001 path: %+v", got)
	}
}

func assertCompileCode(t *testing.T, err error, code string) {
	t.Helper()
	ce, ok := err.(*CompileError)
	if !ok {
		t.Fatalf("error type = %T, want *CompileError (%v)", err, err)
	}
	if ce.Code != code {
		t.Fatalf("error code = %s, want %s (%v)", ce.Code, code, err)
	}
}

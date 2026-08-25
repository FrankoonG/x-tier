package rendradapter

import (
	"testing"

	"github.com/FrankoonG/rendr"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TestBuildTargetPreservesLeafDescriptorMetadata(t *testing.T) {
	path := route.ResolvedPath{
		ID:                         "B-D",
		Expression:                 "B/D",
		CarrierKind:                route.CarrierRelayChain,
		CarrierEntry:               "B",
		RendrTerminal:              "D",
		ExpectedTerminalInstanceID: "runtime-D",
		SessionKind:                route.SessionKindPacket,
	}
	compiled := route.CompiledRoute{
		Intent: route.RouteIntent{Strategy: route.StrategyRace},
		Target: route.TargetSummary{Kind: route.TargetRace},
		Leaves: []route.RouteLeafDescriptor{{
			ID:                        path.ID,
			LogicalPath:               path,
			TerminalNodeID:            "D",
			ExpectedRuntimeInstanceID: "runtime-D",
			SessionKind:               route.SessionKindPacket,
		}},
	}

	root, ok := buildTarget(compiled).(rendr.GroupTarget)
	if !ok {
		t.Fatalf("root type = %T, want rendr.GroupTarget", buildTarget(compiled))
	}
	if root.Kind != rendr.TargetKindRace || len(root.Children) != 1 {
		t.Fatalf("unexpected root: %+v", root)
	}
	leaf, ok := root.Children[0].(rendr.PathTarget)
	if !ok {
		t.Fatalf("leaf type = %T, want rendr.PathTarget", root.Children[0])
	}
	if leaf.Spec.Transport != chainTransport || leaf.Spec.Address != "B-D" {
		t.Fatalf("unexpected path spec: %+v", leaf.Spec)
	}
	if leaf.Spec.Opts["expression"] != "B/D" || leaf.Spec.Opts["terminal_node_id"] != "D" || leaf.Spec.Opts["runtime_instance_id"] != "runtime-D" || leaf.Spec.Opts["session_kind"] != "packet" {
		t.Fatalf("incomplete descriptor metadata: %+v", leaf.Spec.Opts)
	}
}

func TestBuildTargetMapsCompiledGroupKinds(t *testing.T) {
	leaf := route.RouteLeafDescriptor{
		ID:          "D",
		LogicalPath: route.ResolvedPath{ID: "D", Expression: "D"},
	}
	for _, tc := range []struct {
		name      string
		kind      route.TargetKind
		want      rendr.TargetKind
		wantPeak  bool
		leafCount int
	}{
		{name: "selector", kind: route.TargetSelector, want: rendr.TargetKindSelector, leafCount: 1},
		{name: "race", kind: route.TargetRace, want: rendr.TargetKindRace, leafCount: 1},
		{name: "bond", kind: route.TargetBond, want: rendr.TargetKindBond, leafCount: 1},
		{name: "peak", kind: route.TargetPeak, want: rendr.TargetKindSelector, wantPeak: true, leafCount: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaves := make([]route.RouteLeafDescriptor, tc.leafCount)
			for i := range leaves {
				leaves[i] = leaf
				leaves[i].ID += string(rune('1' + i))
				leaves[i].LogicalPath.ID = leaves[i].ID
			}
			root, ok := buildTarget(route.CompiledRoute{
				Target: route.TargetSummary{Kind: tc.kind},
				Leaves: leaves,
			}).(rendr.GroupTarget)
			if !ok {
				t.Fatalf("root type = %T, want rendr.GroupTarget", root)
			}
			if root.Kind != tc.want || (root.Peak != nil) != tc.wantPeak {
				t.Fatalf("root kind/peak = %s/%t, want %s/%t", root.Kind, root.Peak != nil, tc.want, tc.wantPeak)
			}
		})
	}
}

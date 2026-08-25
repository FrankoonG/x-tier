package route

import "fmt"

func Compile(t Topology, intent RouteIntent) (CompiledRoute, error) {
	paths, err := ResolveIntent(t, intent)
	if err != nil {
		return CompiledRoute{}, err
	}
	strategy := intent.Strategy
	if strategy == "" {
		strategy = StrategySelector
	}
	if err := validateStrategy(strategy, paths); err != nil {
		return CompiledRoute{}, err
	}
	leaves := buildLeafDescriptors(paths)
	return CompiledRoute{
		Intent:        intent,
		ResolvedPaths: paths,
		Leaves:        leaves,
		SessionKind:   leaves[0].SessionKind,
		Target:        buildSummary(strategy, leaves),
	}, nil
}

func validateStrategy(strategy Strategy, paths []ResolvedPath) error {
	if len(paths) == 0 {
		return errf("route.paths_empty", "", "at least one path is required")
	}
	switch strategy {
	case StrategySelector, StrategyRace, StrategyBond, StrategyPeak:
	default:
		return errf("route.strategy_unknown", "", "unknown strategy %q", strategy)
	}
	terminal := paths[0].RendrTerminal
	sessionKind := paths[0].SessionKind
	instance := firstKnownInstanceID(paths)
	for _, p := range paths[1:] {
		if p.RendrTerminal != terminal {
			return strategyMismatch(strategy, "terminal", p.Expression, "terminal %s does not match %s", p.RendrTerminal, terminal)
		}
		if p.SessionKind != sessionKind {
			return strategyMismatch(strategy, "session_kind", p.Expression, "session kind %s does not match %s", p.SessionKind, sessionKind)
		}
		if instance != "" && p.ExpectedTerminalInstanceID != "" && p.ExpectedTerminalInstanceID != instance {
			return strategyMismatch(strategy, "instance", p.Expression, "terminal runtime instance %s does not match %s", p.ExpectedTerminalInstanceID, instance)
		}
	}
	return nil
}

func firstKnownInstanceID(paths []ResolvedPath) string {
	for _, p := range paths {
		if p.ExpectedTerminalInstanceID != "" {
			return p.ExpectedTerminalInstanceID
		}
	}
	return ""
}

func strategyMismatch(strategy Strategy, field, path, format string, args ...any) error {
	return errf("route."+field+"_mismatch", path, "%s path "+format, append([]any{strategy}, args...)...)
}

func buildLeafDescriptors(paths []ResolvedPath) []RouteLeafDescriptor {
	leaves := make([]RouteLeafDescriptor, 0, len(paths))
	for _, p := range paths {
		p.Hops = append([]NodeID(nil), p.Hops...)
		p.Edges = append([]Edge(nil), p.Edges...)
		leaves = append(leaves, RouteLeafDescriptor{
			ID:                        p.ID,
			LogicalPathID:             p.ID,
			LogicalPath:               p,
			TerminalNodeID:            p.RendrTerminal,
			ExpectedRuntimeInstanceID: p.ExpectedTerminalInstanceID,
			SessionKind:               p.SessionKind,
		})
	}
	return leaves
}

func buildSummary(strategy Strategy, leaves []RouteLeafDescriptor) TargetSummary {
	children := make([]TargetSummary, 0, len(leaves))
	for _, leaf := range leaves {
		leaf := leaf
		children = append(children, TargetSummary{Name: leaf.Name(), Kind: TargetPath, Descriptor: &leaf})
	}
	kind := TargetSelector
	name := "root"
	switch strategy {
	case StrategyRace:
		kind = TargetRace
	case StrategyBond:
		kind = TargetBond
	case StrategyPeak:
		kind = TargetPeak
		name = "root-peak"
	}
	return TargetSummary{Name: name, Kind: kind, Children: children}
}

func DescribePath(p ResolvedPath) string {
	return fmt.Sprintf("%s terminal=%s carrier=%s entry=%s", p.Expression, p.RendrTerminal, p.CarrierKind, p.CarrierEntry)
}

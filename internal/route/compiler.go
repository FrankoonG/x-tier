package route

import (
	"fmt"

	"github.com/FrankoonG/rendr"
)

type RendrCompiledRoute struct {
	CompiledRoute
	Root rendr.Target
}

func Compile(t Topology, intent RouteIntent) (RendrCompiledRoute, error) {
	paths, err := ResolveIntent(t, intent)
	if err != nil {
		return RendrCompiledRoute{}, err
	}
	strategy := intent.Strategy
	if strategy == "" {
		strategy = StrategySelector
	}
	if err := validateStrategy(strategy, paths); err != nil {
		return RendrCompiledRoute{}, err
	}
	summary := buildSummary(strategy, paths)
	root := buildRendrTarget(strategy, paths)
	compiled := CompiledRoute{
		Intent:        intent,
		ResolvedPaths: paths,
		Target:        summary,
	}
	return RendrCompiledRoute{CompiledRoute: compiled, Root: root}, nil
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
	if strategy != StrategyBond {
		return nil
	}
	final := paths[0].FinalPeer
	terminal := paths[0].RendrTerminal
	instance := paths[0].ExpectedTerminalInstanceID
	endpoint := paths[0].EndpointKind
	for _, p := range paths[1:] {
		if p.FinalPeer != final {
			return errf("route.bond_final_mismatch", p.Expression, "bond path final %s does not match %s", p.FinalPeer, final)
		}
		if p.RendrTerminal != terminal {
			return errf("route.bond_terminal_mismatch", p.Expression, "bond path terminal %s does not match %s", p.RendrTerminal, terminal)
		}
		if p.EndpointKind != endpoint {
			return errf("route.bond_endpoint_mismatch", p.Expression, "bond endpoint %s does not match %s", p.EndpointKind, endpoint)
		}
		if instance != "" && p.ExpectedTerminalInstanceID != "" && p.ExpectedTerminalInstanceID != instance {
			return errf("route.bond_instance_mismatch", p.Expression, "bond terminal instance %s does not match %s", p.ExpectedTerminalInstanceID, instance)
		}
	}
	return nil
}

func buildSummary(strategy Strategy, paths []ResolvedPath) TargetSummary {
	children := make([]TargetSummary, 0, len(paths))
	for _, p := range paths {
		cp := p
		children = append(children, TargetSummary{Name: p.Name(), Kind: TargetPath, Path: &cp})
	}
	kind := TargetSelector
	name := "root"
	switch strategy {
	case StrategyRace:
		kind = TargetRace
	case StrategyBond:
		kind = TargetBond
	case StrategyPeak:
		kind = TargetSelector
		name = "root-peak"
	}
	return TargetSummary{Name: name, Kind: kind, Children: children}
}

func buildRendrTarget(strategy Strategy, paths []ResolvedPath) rendr.Target {
	children := make([]rendr.Target, 0, len(paths))
	for _, p := range paths {
		children = append(children, rendr.Path(p.Name(), rendr.PathSpec{
			Transport: leafTransport,
			Address:   p.Expression,
			Opts: map[string]string{
				"name":              p.Name(),
				"carrier_kind":      string(p.CarrierKind),
				"carrier_entry":     p.CarrierEntry.String(),
				"rendr_terminal":    p.RendrTerminal.String(),
				"terminal_instance": p.ExpectedTerminalInstanceID,
			},
		}))
	}
	switch strategy {
	case StrategyRace:
		return rendr.Race("root", children)
	case StrategyBond:
		return rendr.Bond("root", children)
	case StrategyPeak:
		if len(children) == 1 {
			return rendr.Selector("root-peak", children)
		}
		peak := children[len(children)-1].Name()
		return rendr.Selector("root-peak", children, rendr.PeakTransfer{Targets: []string{peak}})
	default:
		return rendr.Selector("root", children)
	}
}

func DescribePath(p ResolvedPath) string {
	return fmt.Sprintf("%s terminal=%s carrier=%s entry=%s", p.Expression, p.RendrTerminal, p.CarrierKind, p.CarrierEntry)
}

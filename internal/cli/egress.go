package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/FrankoonG/x-tier/internal/configstore"
)

func localPeerEgress(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("peer egress subcommand is required")}
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return commandError{"cli.argument_unexpected", fmt.Errorf("peer egress list does not accept arguments")}
		}
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{
			"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID,
			"node_egress_grants": cfg.NodeEgressGrants,
		})
	case "show":
		if len(args) != 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer reference is required")}
		}
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		peer, _, ok := configstore.FindPeer(cfg.Peers, args[1])
		if !ok {
			return commandError{"config.peer_unknown", fmt.Errorf("%s", args[1])}
		}
		grant, ok := cfg.NodeEgressGrants[peer.NodeID]
		if !ok {
			return commandError{"config.node_egress_grant_unknown", fmt.Errorf("%s", peer.NodeID)}
		}
		return writeOutput(g, out, map[string]any{
			"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID,
			"node_egress_grant": grant,
		})
	case "set":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer reference is required")}
		}
		peerRef := args[1]
		fs := flag.NewFlagSet("peer egress set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		network := fs.String("network", "tcp", "")
		allowCIDRs := fs.String("allow-cidrs", "", "")
		allowPrivateCIDRs := fs.String("allow-private-cidrs", "", "")
		denyCIDRs := fs.String("deny-cidrs", "", "")
		allowPorts := fs.String("allow-ports", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return commandError{"cli.argument_unexpected", fmt.Errorf("unexpected peer egress arguments")}
		}
		ports, err := parseEgressPortRanges(*allowPorts)
		if err != nil {
			return err
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			peer, _, ok := configstore.FindPeer(cfg.Peers, peerRef)
			if !ok {
				return nil, commandError{"config.peer_unknown", fmt.Errorf("%s", peerRef)}
			}
			grant := configstore.NodeEgressGrant{
				SourceNodeID:      peer.NodeID,
				Network:           *network,
				AllowCIDRs:        splitCSV(*allowCIDRs),
				AllowPrivateCIDRs: splitCSV(*allowPrivateCIDRs),
				DenyCIDRs:         splitCSV(*denyCIDRs),
				AllowPorts:        ports,
			}
			cfg.NodeEgressGrants[peer.NodeID] = grant
			return map[string]any{
				"target_local_node_id": cfg.Node.NodeID,
				"node_egress_grant":    grant,
			}, nil
		})
	case "revoke":
		if len(args) != 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer reference is required")}
		}
		peerRef := args[1]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			peer, _, ok := configstore.FindPeer(cfg.Peers, peerRef)
			if !ok {
				return nil, commandError{"config.peer_unknown", fmt.Errorf("%s", peerRef)}
			}
			if _, ok := cfg.NodeEgressGrants[peer.NodeID]; !ok {
				return nil, commandError{"config.node_egress_grant_unknown", fmt.Errorf("%s", peer.NodeID)}
			}
			delete(cfg.NodeEgressGrants, peer.NodeID)
			return map[string]any{
				"target_local_node_id":      cfg.Node.NodeID,
				"source_node_id":            peer.NodeID,
				"node_egress_grant_revoked": true,
			}, nil
		})
	default:
		return commandError{"cli.unknown_command", fmt.Errorf("peer egress %s", strings.Join(args, " "))}
	}
}

func parseEgressPortRanges(expression string) ([]configstore.EgressPortRange, error) {
	parts := splitCSV(expression)
	if len(parts) == 0 {
		return nil, commandError{"config.node_egress_grant_ports_required", fmt.Errorf("--allow-ports is required")}
	}
	ranges := make([]configstore.EgressPortRange, 0, len(parts))
	for _, part := range parts {
		fromRaw, toRaw, ranged := strings.Cut(part, "-")
		if strings.Contains(toRaw, "-") {
			return nil, commandError{"config.node_egress_grant_port_invalid", fmt.Errorf("invalid port range")}
		}
		if !ranged {
			toRaw = fromRaw
		}
		from, fromErr := strconv.ParseUint(fromRaw, 10, 16)
		to, toErr := strconv.ParseUint(toRaw, 10, 16)
		if fromErr != nil || toErr != nil || from == 0 || to == 0 || from > to {
			return nil, commandError{"config.node_egress_grant_port_invalid", fmt.Errorf("invalid port range")}
		}
		ranges = append(ranges, configstore.EgressPortRange{From: uint16(from), To: uint16(to)})
	}
	return ranges, nil
}

package cli

import (
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configops"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TestPeerEgressGrantCLICompleteLifecycle(t *testing.T) {
	cfg := cliEgressConfig()
	path := seedConfig(t, cfg)
	peerNodeID := cfg.Peers[0].NodeID

	code, output := runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "0",
		"local", "peer", "egress", "set", "A",
		"--allow-cidrs", "9.0.0.0/8,8.0.0.0/8",
		"--allow-private-cidrs", "10.0.0.0/8",
		"--deny-cidrs", "9.9.0.0/16",
		"--allow-ports", "443,8000-8099",
	)
	if code != 0 {
		t.Fatalf("egress set code=%d output=%s", code, output)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	grant := loaded.NodeEgressGrants[peerNodeID]
	if loaded.Revision != 1 || grant.SourceNodeID != peerNodeID || grant.Network != "tcp" ||
		len(grant.AllowCIDRs) != 2 || len(grant.AllowPorts) != 2 ||
		grant.AllowPorts[1] != (configstore.EgressPortRange{From: 8000, To: 8099}) {
		t.Fatalf("stored grant = %+v revision=%d", grant, loaded.Revision)
	}

	for _, args := range [][]string{
		{"local", "peer", "egress", "list"},
		{"local", "peer", "egress", "show", peerNodeID},
	} {
		full := append([]string{"--offline", "--config", path, "--json"}, args...)
		if code, output := runCLI(t, full...); code != 0 || !strings.Contains(output, peerNodeID) {
			t.Fatalf("egress read %v code=%d output=%s", args, code, output)
		}
	}

	code, output = runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "1",
		"local", "peer", "egress", "set", peerNodeID,
		"--allow-cidrs", "1.1.1.0/24", "--allow-ports", "53",
	)
	if code != 0 {
		t.Fatalf("egress replacement code=%d output=%s", code, output)
	}
	loaded, err = configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	grant = loaded.NodeEgressGrants[peerNodeID]
	if loaded.Revision != 2 || len(grant.AllowCIDRs) != 1 || grant.AllowCIDRs[0] != "1.1.1.0/24" ||
		len(grant.AllowPrivateCIDRs) != 0 || len(grant.DenyCIDRs) != 0 ||
		len(grant.AllowPorts) != 1 || grant.AllowPorts[0].From != 53 || grant.AllowPorts[0].To != 53 {
		t.Fatalf("replacement retained omitted fields: %+v revision=%d", grant, loaded.Revision)
	}

	code, output = runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "2",
		"local", "peer", "egress", "revoke", "A",
	)
	if code != 0 || objectField(t, decodeJSON(t, output), "result")["node_egress_grant_revoked"] != true {
		t.Fatalf("egress revoke code=%d output=%s", code, output)
	}
	loaded, err = configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 3 || len(loaded.NodeEgressGrants) != 0 {
		t.Fatalf("revoke result = %+v", loaded)
	}
	code, output = runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "3",
		"local", "peer", "egress", "revoke", "A",
	)
	if code == 0 || jsonField(t, output, "error_code") != "config.node_egress_grant_unknown" {
		t.Fatalf("second revoke code=%d output=%s", code, output)
	}
}

func TestPeerEgressGrantCLIRejectsInvalidPortsWithoutWriting(t *testing.T) {
	for _, expression := range []string{"", "0", "65536", "443-1", "1-2-3", "tcp"} {
		t.Run(expression, func(t *testing.T) {
			path := seedConfig(t, cliEgressConfig())
			code, output := runDaemonCLI(t,
				"--offline", "--config", path, "--json", "--revision", "0",
				"local", "peer", "egress", "set", "A",
				"--allow-cidrs", "8.0.0.0/8", "--allow-ports", expression,
			)
			if code == 0 {
				t.Fatalf("invalid ports %q succeeded: %s", expression, output)
			}
			loaded, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Revision != 0 || len(loaded.NodeEgressGrants) != 0 {
				t.Fatalf("invalid ports changed config: %+v", loaded)
			}
		})
	}
}

func TestPeerLifecycleCLIUsesAtomicGrantRules(t *testing.T) {
	t.Run("direction requires explicit revoke", func(t *testing.T) {
		cfg := cliEgressConfigWithGrant()
		path := seedConfig(t, cfg)
		code, output := runDaemonCLI(t,
			"--offline", "--config", path, "--json", "--revision", "0",
			"local", "peer", "set", "A", "--direction", "outbound",
		)
		if code == 0 || jsonField(t, output, "error_code") != configops.CodeNodeEgressGrantRevokeRequired {
			t.Fatalf("implicit revoke code=%d output=%s", code, output)
		}
		code, output = runDaemonCLI(t,
			"--offline", "--config", path, "--json", "--revision", "0",
			"local", "peer", "set", "A", "--direction", "outbound", "--revoke-egress-grant",
		)
		if code != 0 || objectField(t, decodeJSON(t, output), "result")["node_egress_grant_revoked"] != true {
			t.Fatalf("explicit revoke code=%d output=%s", code, output)
		}
		loaded, err := configstore.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 1 || loaded.Peers[0].Direction != route.DirectionOutbound || len(loaded.NodeEgressGrants) != 0 {
			t.Fatalf("explicit direction revoke result = %+v", loaded)
		}
	})

	t.Run("disable preserves and remove cascades", func(t *testing.T) {
		cfg := cliEgressConfigWithGrant()
		path := seedConfig(t, cfg)
		code, output := runDaemonCLI(t,
			"--offline", "--config", path, "--json", "--revision", "0",
			"local", "peer", "disable", "A", "--reason", "maintenance",
		)
		if code != 0 {
			t.Fatalf("disable code=%d output=%s", code, output)
		}
		loaded, err := configstore.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Peers[0].Enabled || len(loaded.NodeEgressGrants) != 1 {
			t.Fatalf("disable changed grant lifecycle: %+v", loaded)
		}
		code, output = runDaemonCLI(t,
			"--offline", "--config", path, "--json", "--revision", "1",
			"local", "peer", "remove", "A",
		)
		if code != 0 || objectField(t, decodeJSON(t, output), "result")["node_egress_grant_revoked"] != true {
			t.Fatalf("remove code=%d output=%s", code, output)
		}
		loaded, err = configstore.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 2 || len(loaded.Peers) != 0 || len(loaded.NodeEgressGrants) != 0 {
			t.Fatalf("remove did not cascade: %+v", loaded)
		}
	})

	t.Run("exit peer dependency rolls back", func(t *testing.T) {
		cfg := cliEgressConfigWithGrant()
		cfg.Peers[0].Direction = route.DirectionBidirectional
		cfg.NodeInbound = []configstore.InboundConfig{{
			Kind: "socks", Purpose: "user", Listen: "127.0.0.1:19081", Enabled: false, ExitPeer: "A",
		}}
		path := seedConfig(t, cfg)
		code, output := runDaemonCLI(t,
			"--offline", "--config", path, "--json", "--revision", "0",
			"local", "peer", "remove", "A",
		)
		if code == 0 || jsonField(t, output, "error_code") != configops.CodePeerInUse {
			t.Fatalf("dependent remove code=%d output=%s", code, output)
		}
		loaded, err := configstore.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 0 || len(loaded.Peers) != 1 || len(loaded.NodeEgressGrants) != 1 {
			t.Fatalf("failed remove changed config: %+v", loaded)
		}
	})
}

func cliEgressConfig() configstore.Config {
	cfg := configstore.DefaultConfig()
	cfg.Node = node("local")
	cfg.XrayProfiles = runtimeTestProfiles()
	cfg.Peers = []configstore.PeerConfig{{
		Name:          "A",
		NodeID:        legacyNodeID("peer-a"),
		DisplayName:   "A",
		Addr:          "10.20.0.2:19080",
		GatewayAddr:   "10.20.0.2:19080",
		Direction:     route.DirectionInbound,
		XrayProfileID: "vless",
		Enabled:       true,
		RendrCapable:  true,
	}}
	return cfg
}

func cliEgressConfigWithGrant() configstore.Config {
	cfg := cliEgressConfig()
	nodeID := cfg.Peers[0].NodeID
	cfg.NodeEgressGrants[nodeID] = configstore.NodeEgressGrant{
		SourceNodeID: nodeID,
		Network:      "tcp",
		AllowCIDRs:   []string{"8.0.0.0/8"},
		AllowPorts:   []configstore.EgressPortRange{{From: 443, To: 443}},
	}
	return cfg
}

package configops

import (
	"reflect"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TestRemovePeerCascadesNodeEgressGrantWithoutMutatingSource(t *testing.T) {
	source := lifecycleConfig()
	wantSource := cloneForTest(t, source)

	result, err := RemovePeer(source, "A")
	if err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if result.Peer.NodeID != "node-a" || !result.NodeEgressGrantRevoked {
		t.Fatalf("result = %+v", result)
	}
	if _, _, found := configstore.FindPeer(result.Config.Peers, "node-a"); found {
		t.Fatal("removed peer remains in result")
	}
	if _, found := result.Config.NodeEgressGrants["node-a"]; found {
		t.Fatal("removed peer grant remains in result")
	}
	if result.Config.Revision != source.Revision {
		t.Fatalf("revision changed from %d to %d", source.Revision, result.Config.Revision)
	}
	assertConfigEqual(t, source, wantSource)

	result.Config.Peers[0].Children[0].Name = "changed"
	result.Config.XrayProfiles["peer"].VLESS.UUID = "changed"
	profile := result.Config.XrayProfiles["peer"]
	profile.Options["marker"] = "changed"
	result.Config.XrayProfiles["peer"] = profile
	trust := result.Config.PeerTrust["node-b"]
	trust.Allow[0] = "changed"
	result.Config.PeerTrust["node-b"] = trust
	grant := result.Config.NodeEgressGrants["node-b"]
	grant.AllowCIDRs[0] = "198.51.100.0/24"
	result.Config.NodeEgressGrants["node-b"] = grant
	assertConfigEqual(t, source, wantSource)
}

func TestRemovePeerRejectsExitPeerDependencyAtomically(t *testing.T) {
	for _, exitPeer := range []string{"A", "node-a"} {
		t.Run(exitPeer, func(t *testing.T) {
			source := lifecycleConfig()
			source.NodeInbound = []configstore.InboundConfig{{
				Kind: "socks", Purpose: "user", ExitPeer: exitPeer, Enabled: false,
			}}
			wantSource := cloneForTest(t, source)

			result, err := RemovePeer(source, "node-a")
			if code := publicerr.Code(err, "operation.failed"); code != CodePeerInUse {
				t.Fatalf("error code = %q, err = %v", code, err)
			}
			if !reflect.DeepEqual(result, PeerMutationResult{}) {
				t.Fatalf("failed result = %+v", result)
			}
			assertConfigEqual(t, source, wantSource)
		})
	}
}

func TestUpdatePeerDirectionRequiresExplicitGrantRevocation(t *testing.T) {
	for _, initial := range []route.Direction{route.DirectionInbound, route.DirectionBidirectional} {
		t.Run(string(initial), func(t *testing.T) {
			source := lifecycleConfig()
			source.Peers[0].Direction = initial
			wantSource := cloneForTest(t, source)

			result, err := UpdatePeerDirection(source, "A", route.DirectionOutbound, false)
			if code := publicerr.Code(err, "operation.failed"); code != CodeNodeEgressGrantRevokeRequired {
				t.Fatalf("error code = %q, err = %v", code, err)
			}
			if !reflect.DeepEqual(result, PeerMutationResult{}) {
				t.Fatalf("failed result = %+v", result)
			}
			assertConfigEqual(t, source, wantSource)
		})
	}
}

func TestUpdatePeerDirectionRevokesGrantAtomically(t *testing.T) {
	source := lifecycleConfig()
	wantSource := cloneForTest(t, source)

	result, err := UpdatePeerDirection(source, "node-a", route.DirectionOutbound, true)
	if err != nil {
		t.Fatalf("UpdatePeerDirection: %v", err)
	}
	if result.Peer.Direction != route.DirectionOutbound || !result.NodeEgressGrantRevoked {
		t.Fatalf("result = %+v", result)
	}
	if _, found := result.Config.NodeEgressGrants["node-a"]; found {
		t.Fatal("grant remains after explicit revocation")
	}
	if result.Config.Revision != source.Revision {
		t.Fatalf("revision changed from %d to %d", source.Revision, result.Config.Revision)
	}
	assertConfigEqual(t, source, wantSource)
}

func TestUpdatePeerDirectionKeepsGrantWhenInboundRemains(t *testing.T) {
	source := lifecycleConfig()
	result, err := UpdatePeerDirection(source, "A", route.DirectionBidirectional, false)
	if err != nil {
		t.Fatalf("UpdatePeerDirection: %v", err)
	}
	if result.Peer.Direction != route.DirectionBidirectional || result.NodeEgressGrantRevoked {
		t.Fatalf("result = %+v", result)
	}
	if _, found := result.Config.NodeEgressGrants["node-a"]; !found {
		t.Fatal("grant was removed while inbound capability remained")
	}
}

func TestSetPeerEnabledPreservesGrant(t *testing.T) {
	source := lifecycleConfig()
	wantSource := cloneForTest(t, source)

	disabled, err := SetPeerEnabled(source, "A", false, "")
	if err != nil {
		t.Fatalf("SetPeerEnabled(disable): %v", err)
	}
	if disabled.Peer.Enabled || disabled.Peer.DisabledCause != "disabled" {
		t.Fatalf("disabled peer = %+v", disabled.Peer)
	}
	if disabled.NodeEgressGrantRevoked {
		t.Fatal("disable reported a grant revocation")
	}
	if _, found := disabled.Config.NodeEgressGrants["node-a"]; !found {
		t.Fatal("disable removed node egress grant")
	}
	assertConfigEqual(t, source, wantSource)

	enabled, err := SetPeerEnabled(disabled.Config, "node-a", true, "ignored")
	if err != nil {
		t.Fatalf("SetPeerEnabled(enable): %v", err)
	}
	if !enabled.Peer.Enabled || enabled.Peer.DisabledCause != "" {
		t.Fatalf("enabled peer = %+v", enabled.Peer)
	}
	if _, found := enabled.Config.NodeEgressGrants["node-a"]; !found {
		t.Fatal("enable removed node egress grant")
	}
}

func TestPeerMutationsReturnStableValidationCodes(t *testing.T) {
	source := lifecycleConfig()
	tests := []struct {
		name string
		run  func() error
		code string
	}{
		{
			name: "remove unknown",
			run: func() error {
				_, err := RemovePeer(source, "missing")
				return err
			},
			code: CodePeerUnknown,
		},
		{
			name: "update unknown",
			run: func() error {
				_, err := UpdatePeerDirection(source, "missing", route.DirectionInbound, false)
				return err
			},
			code: CodePeerUnknown,
		},
		{
			name: "disable unknown",
			run: func() error {
				_, err := SetPeerEnabled(source, "missing", false, "maintenance")
				return err
			},
			code: CodePeerUnknown,
		},
		{
			name: "invalid direction",
			run: func() error {
				_, err := UpdatePeerDirection(source, "A", route.Direction("sideways"), true)
				return err
			},
			code: CodePeerDirectionInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); publicerr.Code(err, "operation.failed") != test.code {
				t.Fatalf("error code = %q, want %q, err = %v", publicerr.Code(err, "operation.failed"), test.code, err)
			}
		})
	}
}

func lifecycleConfig() configstore.Config {
	return configstore.Config{
		SchemaVersion: configstore.CurrentSchemaVersion,
		Revision:      41,
		Peers: []configstore.PeerConfig{
			{
				Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, Enabled: true,
				Children: []configstore.PeerConfig{{Name: "nested-a", NodeID: "nested-a"}},
			},
			{
				Name: "B", NodeID: "node-b", Direction: route.DirectionInbound, Enabled: true,
				Children: []configstore.PeerConfig{{Name: "nested-b", NodeID: "nested-b"}},
			},
		},
		XrayProfiles: map[string]configstore.XrayProfile{
			"peer": {
				ID: "peer", Kind: "vless", VLESS: &configstore.VLESSProfile{UUID: "credential"},
				Options: map[string]string{"marker": "original"},
			},
		},
		PeerTrust: map[string]configstore.PeerTrustGrant{
			"node-b": {PeerNodeID: "node-b", Allow: []string{"nodes.manage"}},
		},
		NodeEgressGrants: map[string]configstore.NodeEgressGrant{
			"node-a": {
				SourceNodeID: "node-a", Network: "tcp",
				AllowCIDRs: []string{"203.0.113.0/24"},
				AllowPorts: []configstore.EgressPortRange{{From: 443, To: 443}},
			},
			"node-b": {
				SourceNodeID: "node-b", Network: "tcp",
				AllowCIDRs: []string{"192.0.2.0/24"},
				AllowPorts: []configstore.EgressPortRange{{From: 8443, To: 8443}},
			},
		},
	}
}

func cloneForTest(t *testing.T, source configstore.Config) configstore.Config {
	t.Helper()
	return cloneConfig(source)
}

func assertConfigEqual(t *testing.T, got, want configstore.Config) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source config changed\ngot:  %#v\nwant: %#v", got, want)
	}
}

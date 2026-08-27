package configops

import (
	"reflect"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

func TestAddPeerRequiresProfileForEveryEnabledDirection(t *testing.T) {
	for _, direction := range []route.Direction{
		route.DirectionInbound,
		route.DirectionOutbound,
		route.DirectionBidirectional,
	} {
		t.Run(string(direction), func(t *testing.T) {
			source := configstore.DefaultConfig()
			wantSource := cloneForTest(t, source)
			result, err := AddPeer(source, configstore.PeerConfig{
				Name: "B", NodeID: "node-b", Direction: direction, Enabled: true,
			})
			if code := publicerr.Code(err, "operation.failed"); code != CodePeerProfileRequired {
				t.Fatalf("error code = %q, err = %v", code, err)
			}
			if !reflect.DeepEqual(result, PeerMutationResult{}) {
				t.Fatalf("failed result = %+v", result)
			}
			assertConfigEqual(t, source, wantSource)
		})
	}
}

func TestAddPeerAllowsDisabledAddressBookEntryWithoutProfile(t *testing.T) {
	source := configstore.DefaultConfig()
	peer := configstore.PeerConfig{
		Name: "B", NodeID: "node-b", Direction: route.DirectionInbound, Enabled: false,
	}
	result, err := AddPeer(source, peer)
	if err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	if len(result.Config.Peers) != 1 || !reflect.DeepEqual(result.Peer, peer) {
		t.Fatalf("result = %+v", result)
	}
	if len(source.Peers) != 0 {
		t.Fatalf("source peers changed: %+v", source.Peers)
	}
}

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
	result.Config.XrayProfiles["peer-a"].VLESS.UUID = "changed"
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

func TestSetPeerEnabledRejectsMissingOrReusedCredential(t *testing.T) {
	t.Run("missing profile", func(t *testing.T) {
		source := configstore.DefaultConfig()
		source.Peers = []configstore.PeerConfig{{
			Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, Enabled: false,
		}}
		wantSource := cloneForTest(t, source)
		result, err := SetPeerEnabled(source, "C", true, "")
		if code := publicerr.Code(err, "operation.failed"); code != "config.peer_inbound_profile_incompatible" {
			t.Fatalf("error code = %q, err = %v", code, err)
		}
		if !reflect.DeepEqual(result, PeerMutationResult{}) {
			t.Fatalf("failed result = %+v", result)
		}
		assertConfigEqual(t, source, wantSource)
	})

	t.Run("credential already active", func(t *testing.T) {
		source := configstore.DefaultConfig()
		source.XrayProfiles["shared"] = configstore.XrayProfile{
			ID: "shared", Kind: "vless", VLESS: &configstore.VLESSProfile{
				UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			},
		}
		source.Peers = []configstore.PeerConfig{
			{
				Name: "B", NodeID: "node-b", Addr: "10.20.0.2:2443", GatewayAddr: "10.20.0.2:2443",
				Direction: route.DirectionOutbound, XrayProfileID: "shared", Enabled: true,
			},
			{Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, XrayProfileID: "shared", Enabled: false},
		}
		wantSource := cloneForTest(t, source)
		result, err := SetPeerEnabled(source, "C", true, "")
		if code := publicerr.Code(err, "operation.failed"); code != "config.peer_credential_duplicate" {
			t.Fatalf("error code = %q, err = %v", code, err)
		}
		if !reflect.DeepEqual(result, PeerMutationResult{}) {
			t.Fatalf("failed result = %+v", result)
		}
		assertConfigEqual(t, source, wantSource)
	})
}

func TestQuarantinedPeerRequiresCredentialRotationAndExplicitEnable(t *testing.T) {
	source := lifecycleConfig()
	source.Peers[0].Enabled = false
	source.Peers[0].DisabledCause = configstore.PeerCredentialQuarantineCause
	fingerprint, err := xraycredential.VLESSFingerprint(source.XrayProfiles["peer-a"].VLESS.UUID)
	if err != nil {
		t.Fatal(err)
	}
	source.PeerCredentialQuarantines = []configstore.PeerCredentialQuarantine{{
		CredentialFingerprint: fingerprint,
		PeerNodeIDs:           []string{"node-a", "retired-node"},
		Reason:                configstore.PeerCredentialCollisionReason,
	}}
	source.XrayProfiles["peer-a-alias"] = configstore.XrayProfile{
		ID: "peer-a-alias", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: "66ad4540-b58c-4ead-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		},
	}
	source.XrayProfiles["peer-c"] = configstore.XrayProfile{
		ID: "peer-c", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: "16f5cc3e-8186-4751-b6cd-45cc70d4b4fe", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		},
	}
	wantSource := cloneForTest(t, source)

	disabledAgain, err := SetPeerEnabled(source, "A", false, "maintenance")
	if err != nil {
		t.Fatalf("SetPeerEnabled(disable quarantined): %v", err)
	}
	if !configstore.IsPeerCredentialQuarantined(disabledAgain.Peer) {
		t.Fatalf("disable cleared quarantine: %+v", disabledAgain.Peer)
	}
	assertConfigEqual(t, source, wantSource)

	rejected, err := SetPeerEnabled(source, "A", true, "")
	if code := publicerr.Code(err, "operation.failed"); code != CodePeerCredentialQuarantined {
		t.Fatalf("quarantined enable code = %q, err = %v", code, err)
	}
	if !reflect.DeepEqual(rejected, PeerMutationResult{}) {
		t.Fatalf("failed enable result = %+v", rejected)
	}
	assertConfigEqual(t, source, wantSource)

	equivalent, err := UpdatePeerProfile(source, "node-a", "peer-a-alias")
	if err != nil {
		t.Fatalf("UpdatePeerProfile(equivalent): %v", err)
	}
	if equivalent.Peer.XrayProfileID != "peer-a-alias" || !configstore.IsPeerCredentialQuarantined(equivalent.Peer) {
		t.Fatalf("equivalent credential cleared quarantine: %+v", equivalent.Peer)
	}
	if _, err := SetPeerEnabled(equivalent.Config, "A", true, ""); publicerr.Code(err, "operation.failed") != CodePeerCredentialQuarantined {
		t.Fatalf("equivalent credential became enableable: %v", err)
	}
	assertConfigEqual(t, source, wantSource)

	reusedActive, err := UpdatePeerProfile(source, "A", "peer-b")
	if err != nil {
		t.Fatalf("UpdatePeerProfile(active credential): %v", err)
	}
	if !configstore.IsPeerCredentialQuarantined(reusedActive.Peer) {
		t.Fatalf("credential bound to another peer cleared quarantine: %+v", reusedActive.Peer)
	}
	assertConfigEqual(t, source, wantSource)

	empty, err := UpdatePeerProfile(source, "A", "")
	if err != nil {
		t.Fatalf("UpdatePeerProfile(empty): %v", err)
	}
	if empty.Peer.XrayProfileID != "" || !configstore.IsPeerCredentialQuarantined(empty.Peer) {
		t.Fatalf("empty profile destroyed quarantine evidence: %+v", empty.Peer)
	}
	rotated, err := UpdatePeerProfile(empty.Config, "A", "peer-c")
	if err != nil {
		t.Fatalf("UpdatePeerProfile(distinct): %v", err)
	}
	if rotated.Peer.Enabled || rotated.Peer.XrayProfileID != "peer-c" || rotated.Peer.DisabledCause != configstore.PeerCredentialRotatedDisabled {
		t.Fatalf("rotated peer = %+v", rotated.Peer)
	}
	assertConfigEqual(t, source, wantSource)

	enabled, err := SetPeerEnabled(rotated.Config, "node-a", true, "")
	if err != nil {
		t.Fatalf("SetPeerEnabled(after rotation): %v", err)
	}
	if !enabled.Peer.Enabled || enabled.Peer.DisabledCause != "" || enabled.Peer.XrayProfileID != "peer-c" {
		t.Fatalf("enabled rotated peer = %+v", enabled.Peer)
	}
	if len(enabled.Config.PeerCredentialQuarantines) != 1 || enabled.Config.PeerCredentialQuarantines[0].CredentialFingerprint != fingerprint {
		t.Fatalf("rotation removed durable quarantine: %+v", enabled.Config.PeerCredentialQuarantines)
	}
	reused, err := AddPeer(enabled.Config, configstore.PeerConfig{
		Name: "D", NodeID: "node-d", Direction: route.DirectionInbound,
		XrayProfileID: "peer-a-alias", Enabled: false,
	})
	if err != nil {
		t.Fatalf("AddPeer(disabled old credential): %v", err)
	}
	if _, err := SetPeerEnabled(reused.Config, "D", true, ""); publicerr.Code(err, "operation.failed") != CodePeerCredentialQuarantined {
		t.Fatalf("retired credential became reusable after full rotation: %v", err)
	}
	assertConfigEqual(t, source, wantSource)
}

func TestPeerMutationsReturnStableValidationCodes(t *testing.T) {
	source := lifecycleConfig()
	tests := []struct {
		name string
		run  func() error
		code string
	}{
		{
			name: "add without profile",
			run: func() error {
				_, err := AddPeer(source, configstore.PeerConfig{
					Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, Enabled: true,
				})
				return err
			},
			code: CodePeerProfileRequired,
		},
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
				Name: "A", NodeID: "node-a", Direction: route.DirectionInbound, XrayProfileID: "peer-a", Enabled: true,
				Children: []configstore.PeerConfig{{Name: "nested-a", NodeID: "nested-a-id", Direction: route.DirectionInbound}},
			},
			{
				Name: "B", NodeID: "node-b", Direction: route.DirectionInbound, XrayProfileID: "peer-b", Enabled: true,
				Children: []configstore.PeerConfig{{Name: "nested-b", NodeID: "nested-b-id", Direction: route.DirectionInbound}},
			},
		},
		XrayProfiles: map[string]configstore.XrayProfile{
			"peer-a": {
				ID: "peer-a", Kind: "vless", VLESS: &configstore.VLESSProfile{
					UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
				},
			},
			"peer-b": {
				ID: "peer-b", Kind: "vless", VLESS: &configstore.VLESSProfile{
					UUID: "f3c9805c-12ea-48f0-b762-5739f2365620", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
				},
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

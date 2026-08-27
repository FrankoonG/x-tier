package xrayconfig

import (
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/route"
)

const (
	testUUID        = "66ad4540-b58c-4ad2-9926-ea63445a9b57"
	testInboundUUID = "f3c9805c-12ea-48f0-b762-5739f2365620"
)

func TestCompileInitialVerticalSlice(t *testing.T) {
	cfg := testConfig()
	compiled, err := Compile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Inbounds) != 2 {
		t.Fatalf("inbounds = %d, want 2", len(compiled.Inbounds))
	}
	if compiled.Routes[UserSOCKSTag].Kind != RouteUser || compiled.Routes[UserSOCKSTag].PeerTag == "" {
		t.Fatalf("user route = %+v", compiled.Routes[UserSOCKSTag])
	}
	if compiled.Routes[NodeVLESSTag].Kind != RouteCarrier {
		t.Fatalf("carrier route = %+v", compiled.Routes[NodeVLESSTag])
	}
	if len(compiled.CarrierPeers) != 1 {
		t.Fatalf("carrier principals = %+v", compiled.CarrierPeers)
	}
	for handle, nodeID := range compiled.CarrierPeers {
		if nodeID != "node-a" || handle == "" || strings.Contains(handle, nodeID) {
			t.Fatalf("carrier principal %q=%q is not opaque and peer-bound", handle, nodeID)
		}
	}
	if info, err := compiled.Outbound.Info(); err != nil || info.EncodedBytes == 0 {
		t.Fatalf("generation info = %+v, %v", info, err)
	}
}

func TestCompileRejectsEnabledUserInboundWhenExitPeerIsDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Peers[0].Enabled = false
	if _, err := Compile(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_exit_peer_unavailable") {
		t.Fatalf("Compile error = %v, want unavailable exit peer rejection", err)
	}
}

func TestCompileRejectsCredentialAndUnavailableProtocol(t *testing.T) {
	profile := configstore.XrayProfile{ID: "bad", Kind: "vless", VLESS: &configstore.VLESSProfile{
		UUID: testUUID, Transport: "tcp", Security: "none",
	}}
	if err := ValidateProfile(profile); err == nil {
		t.Fatal("implicit plaintext was accepted")
	}
	if err := ValidateProfile(configstore.XrayProfile{ID: "h", Kind: "hysteria2"}); err == nil {
		t.Fatal("unavailable protocol was accepted")
	}
}

func TestCompileRejectsSOCKSPeerProfileWithoutPanicking(t *testing.T) {
	cfg := testConfig()
	cfg.Peers[0].XrayProfileID = "socks"
	if _, err := Compile(cfg); err == nil {
		t.Fatal("SOCKS profile was accepted as a VLESS peer carrier")
	}
}

func TestCompileRejectsXrayEquivalentInboundCredentials(t *testing.T) {
	cfg := testConfig()
	cfg.XrayProfiles["collision"] = configstore.XrayProfile{
		ID: "collision", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: "f3c9805c-12ea-4ff0-b762-5739f2365620", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		},
	}
	cfg.Peers = append(cfg.Peers, configstore.PeerConfig{
		Name: "C", NodeID: "node-c", Direction: route.DirectionInbound,
		XrayProfileID: "collision", Enabled: true, RendrCapable: true,
	})
	if _, err := Compile(cfg); err == nil || !strings.Contains(err.Error(), "credential_duplicate") {
		t.Fatalf("Compile error = %v, want Xray-equivalent credential rejection", err)
	}
}

func TestCompileRejectsCrossDirectionCredentialReuse(t *testing.T) {
	cfg := testConfig()
	cfg.Peers[1].XrayProfileID = "vless"
	if _, err := Compile(cfg); err == nil || !strings.Contains(err.Error(), "config.peer_credential_duplicate") {
		t.Fatalf("Compile error = %v, want cross-direction reuse rejection", err)
	}
}

func TestCompileRejectsConflictingEnabledInboundListeners(t *testing.T) {
	cfg := testConfig()
	cfg.NodeInbound[1].Listen = cfg.NodeInbound[0].Listen
	if _, err := Compile(cfg); err == nil || !strings.Contains(err.Error(), "config.inbound_listen_conflict") {
		t.Fatalf("Compile error = %v, want listener conflict", err)
	}
}

func TestCompileRejectsAnonymousLoopbackSOCKS(t *testing.T) {
	cfg := testConfig()
	cfg.NodeInbound[0].Listen = "127.0.0.1:1080"
	cfg.XrayProfiles["socks"] = configstore.XrayProfile{ID: "socks", Kind: "socks", SOCKS: &configstore.SOCKSProfile{}}
	if _, err := Compile(cfg); err == nil {
		t.Fatal("anonymous loopback SOCKS was accepted")
	}
}

func TestCompileRejectsPlaintextOutsideIsolatedPrivateNetwork(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*configstore.Config)
	}{
		{name: "public peer", mutate: func(cfg *configstore.Config) { cfg.Peers[0].GatewayAddr = "203.0.113.8:2443" }},
		{name: "domain peer", mutate: func(cfg *configstore.Config) { cfg.Peers[0].GatewayAddr = "peer.example:2443" }},
		{name: "wildcard node inbound", mutate: func(cfg *configstore.Config) { cfg.NodeInbound[1].Listen = "0.0.0.0:2443" }},
		{name: "public node inbound", mutate: func(cfg *configstore.Config) { cfg.NodeInbound[1].Listen = "203.0.113.9:2443" }},
		{name: "wildcard user inbound", mutate: func(cfg *configstore.Config) { cfg.NodeInbound[0].Listen = "0.0.0.0:1080" }},
		{name: "public user inbound", mutate: func(cfg *configstore.Config) { cfg.NodeInbound[0].Listen = "203.0.113.10:1080" }},
		{name: "domain user inbound", mutate: func(cfg *configstore.Config) { cfg.NodeInbound[0].Listen = "proxy.example:1080" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.mutate(&cfg)
			if _, err := Compile(cfg); err == nil {
				t.Fatal("unsafe plaintext endpoint was accepted")
			}
		})
	}
}

func TestCompileProfileUsesLiveBuilders(t *testing.T) {
	profiles := testConfig().XrayProfiles
	for _, id := range []string{"vless", "socks"} {
		if err := CompileProfile(profiles[id]); err != nil {
			t.Fatalf("CompileProfile(%s): %v", id, err)
		}
	}
	if err := CompileProfile(configstore.XrayProfile{
		ID: "anonymous", Kind: "socks", SOCKS: &configstore.SOCKSProfile{},
	}); err == nil {
		t.Fatal("anonymous SOCKS profile compiled")
	}
}

func testConfig() configstore.Config {
	cfg := configstore.DefaultConfig()
	cfg.XrayProfiles = map[string]configstore.XrayProfile{
		"vless": {ID: "vless", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: testUUID, Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		}},
		"socks": {ID: "socks", Kind: "socks", SOCKS: &configstore.SOCKSProfile{Username: "terminal", Password: "not-a-fixture-secret"}},
		"vless-in": {ID: "vless-in", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: testInboundUUID, Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		}},
	}
	cfg.Peers = []configstore.PeerConfig{
		{
			Name: "B", NodeID: "node-b", Addr: "10.20.0.2:2443", GatewayAddr: "10.20.0.2:2443",
			Direction: route.DirectionOutbound, XrayProfileID: "vless", Enabled: true, RendrCapable: true,
		},
		{
			Name: "A", NodeID: "node-a", Direction: route.DirectionInbound,
			XrayProfileID: "vless-in", Enabled: true, RendrCapable: true,
		},
	}
	cfg.NodeInbound = []configstore.InboundConfig{
		{Kind: "socks", Purpose: "user", Listen: "10.20.0.3:1080", Enabled: true, XrayProfileID: "socks", ExitPeer: "B"},
		{Kind: "node-vless", Purpose: "node", Listen: "10.20.0.3:2443", Enabled: true},
	}
	return cfg
}

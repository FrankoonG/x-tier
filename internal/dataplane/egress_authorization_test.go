package dataplane

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/egresspolicy"
	"github.com/FrankoonG/x-tier/internal/rendradapter"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
)

func TestCompileEgressAuthorizationIsCanonicalAndOwnsPolicy(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	first, err := compileEgressAuthorization(cfg)
	if err != nil {
		t.Fatal(err)
	}
	grant := cfg.NodeEgressGrants[cfg.Peers[0].NodeID]
	grant.AllowPrivateCIDRs[0], grant.AllowPrivateCIDRs[3] = grant.AllowPrivateCIDRs[3], grant.AllowPrivateCIDRs[0]
	cfg.NodeEgressGrants[cfg.Peers[0].NodeID] = grant
	second, err := compileEgressAuthorization(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.digest != second.digest {
		t.Fatal("semantic grant reordering changed the authorization digest")
	}
	grant.AllowPorts = []configstore.EgressPortRange{{From: 443, To: 443}}
	cfg.NodeEgressGrants[cfg.Peers[0].NodeID] = grant
	third, err := compileEgressAuthorization(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.digest == third.digest {
		t.Fatal("authorization rule change retained the previous digest")
	}
	if !first.policies[grant.SourceNodeID].Allows(netip.MustParseAddr("10.1.2.3"), 80) {
		t.Fatal("caller mutation changed the compiled authorization policy")
	}
}

func TestEgressWithoutGrantOrMatchingSourceRejectsBeforeDNS(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	delete(cfg.NodeEgressGrants, cfg.Peers[0].NodeID)
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, plane) })
	lookupCalls := 0
	plane.egressLookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookupCalls++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	request := validEgressRequest(t, cfg, "origin.example", 443)
	if conn, err := plane.dialEgress(context.Background(), request); err == nil || conn != nil {
		t.Fatalf("grantless egress = %v, %v", conn, err)
	}
	if lookupCalls != 0 {
		t.Fatalf("grantless egress reached DNS %d times", lookupCalls)
	}

}

func TestEgressWrongSourceCannotBorrowAnotherNodeGrant(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, plane) })
	lookupCalls := 0
	plane.egressLookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookupCalls++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	request := validEgressRequest(t, cfg, "origin.example", 443)
	request.Claim.Origin.ClaimedPeerNodeID = "node-0000000000000000000000000000000c"
	if conn, err := plane.dialEgress(context.Background(), request); err == nil || conn != nil {
		t.Fatalf("wrong-source egress = %v, %v", conn, err)
	}
	if lookupCalls != 0 {
		t.Fatalf("wrong-source egress reached DNS %d times", lookupCalls)
	}
}

func TestEgressGrantChangeRotatesRuntimeAndRevokesCapturedSnapshot(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, plane) })
	previousRuntime := plane.currentRendr()
	previousAuthorization := plane.activeEgressAuthorization()

	replacement := newFakeRendrRuntime("running")
	plane.lifecycleMu.Lock()
	plane.rendrFactory = func(context.Context) (rendrRuntime, error) { return replacement, nil }
	plane.lifecycleMu.Unlock()
	candidate := terminalConfig(cfg.NodeInbound[0].Listen)
	candidate.Revision = cfg.Revision + 1
	grant := candidate.NodeEgressGrants[candidate.Peers[0].NodeID]
	grant.AllowPorts = []configstore.EgressPortRange{{From: 443, To: 443}}
	candidate.NodeEgressGrants[candidate.Peers[0].NodeID] = grant
	if err := plane.Apply(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if plane.currentRendr() != replacement || plane.currentRendr() == previousRuntime {
		t.Fatal("grant change did not rotate the rendr runtime")
	}
	if plane.activeEgressAuthorization() == previousAuthorization {
		t.Fatal("grant change retained the previous authorization snapshot")
	}

	lookupCalls := 0
	plane.egressLookup = func(context.Context, string, string) ([]netip.Addr, error) {
		lookupCalls++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	request := validEgressRequest(t, candidate, "origin.example", 443)
	if conn, err := plane.dialEgressAuthorized(context.Background(), previousAuthorization, request); err == nil || conn != nil {
		t.Fatalf("captured old authorization dial = %v, %v", conn, err)
	}
	if lookupCalls != 0 {
		t.Fatalf("revoked authorization reached DNS %d times", lookupCalls)
	}
	boundEgress := replacement.boundEgressDialer()
	if boundEgress == nil {
		t.Fatal("replacement rendr runtime has no egress dialer")
	}
	request.Destination.Port = 80
	if conn, err := boundEgress(context.Background(), request); !errors.Is(err, egresspolicy.ErrAddressDenied) || conn != nil {
		t.Fatalf("replacement authorization-bound dial = %v, %v", conn, err)
	}
	if lookupCalls != 0 {
		t.Fatalf("unauthorized candidate port reached DNS %d times", lookupCalls)
	}
}

func TestInvalidEgressAuthorizationFailsClosed(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	grant := cfg.NodeEgressGrants[cfg.Peers[0].NodeID]
	grant.AllowCIDRs = []string{"8.8.8.1/24"}
	cfg.NodeEgressGrants[cfg.Peers[0].NodeID] = grant
	if snapshot, err := compileEgressAuthorization(cfg); !errors.Is(err, ErrEgressAuthorizationInvalid) || snapshot != nil {
		t.Fatalf("invalid authorization = %#v, %v", snapshot, err)
	}
}

func TestEgressAuthorizationStatusTracksEffectiveSnapshot(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, plane) })
	status := plane.Status()
	if status.EgressAuthorizationRevision != cfg.Revision || status.EgressAuthorizationSources != 1 ||
		status.EgressAuthorizationDigest == ([32]byte{}) {
		t.Fatalf("initial authorization status = %+v", status)
	}

	if err := plane.FailStop(context.Background(), "test.fail_stop"); err != nil {
		t.Fatal(err)
	}
	status = plane.Status()
	if !status.FailStopped || status.EgressAuthorizationRevision != -1 || status.EgressAuthorizationSources != 0 {
		t.Fatalf("fail-stopped authorization status = %+v", status)
	}
}

func TestSemanticGrantReorderingReusesRuntimeAndAuthorizationSnapshot(t *testing.T) {
	cfg := terminalConfig(reserveAddress(t))
	plane, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePlane(t, plane) })
	previousRuntime := plane.currentRendr()
	previousAuthorization := plane.activeEgressAuthorization()

	candidate := terminalConfig(cfg.NodeInbound[0].Listen)
	candidate.Revision = cfg.Revision + 1
	grant := candidate.NodeEgressGrants[candidate.Peers[0].NodeID]
	for left, right := 0, len(grant.AllowPrivateCIDRs)-1; left < right; left, right = left+1, right-1 {
		grant.AllowPrivateCIDRs[left], grant.AllowPrivateCIDRs[right] = grant.AllowPrivateCIDRs[right], grant.AllowPrivateCIDRs[left]
	}
	candidate.NodeEgressGrants[candidate.Peers[0].NodeID] = grant
	if err := plane.Apply(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if plane.currentRendr() != previousRuntime || plane.activeEgressAuthorization() != previousAuthorization {
		t.Fatal("semantic grant reordering rotated runtime or authorization snapshot")
	}
	status := plane.Status()
	if status.AppliedRevision != candidate.Revision || status.EgressAuthorizationRevision != cfg.Revision {
		t.Fatalf("semantic reorder status = %+v", status)
	}
}

func validEgressRequest(t *testing.T, cfg configstore.Config, host string, port uint16) rendradapter.EgressRequest {
	t.Helper()
	return rendradapter.EgressRequest{
		Claim: rendradapter.FlowClaim{
			Origin: rendradapter.OriginClaim{
				Assurance:         rendradapter.OriginAssuranceXrayBearer,
				ClaimedPeerNodeID: cfg.Peers[0].NodeID,
				InboundTag:        xrayconfig.NodeVLESSTag,
				PrincipalHandle:   singleCarrierAccount(t, cfg),
			},
			FlowID:         [16]byte{1},
			PeerInstanceID: rendradapter.PeerInstanceID{1},
		},
		Destination: rendradapter.Destination{Host: host, Port: port},
	}
}

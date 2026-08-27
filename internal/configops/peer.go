package configops

import (
	"maps"
	"slices"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

const (
	CodePeerIdentityRequired          = "config.peer_identity_required"
	CodePeerExists                    = "config.peer_exists"
	CodePeerProfileRequired           = "config.peer_profile_required"
	CodePeerCredentialQuarantined     = "config.peer_credential_quarantined"
	CodePeerUnknown                   = "config.peer_unknown"
	CodePeerInUse                     = "config.peer_in_use"
	CodePeerDirectionInvalid          = "config.peer_direction_invalid"
	CodeNodeEgressGrantRevokeRequired = "config.node_egress_grant_revoke_required"
)

// PeerMutationResult contains a detached configuration value and the peer
// affected by one pure mutation. The input configuration is never modified.
type PeerMutationResult struct {
	Config                 configstore.Config
	Peer                   configstore.PeerConfig
	NodeEgressGrantRevoked bool
}

// AddPeer appends one directly managed peer to a detached Config value. All
// enabled peer directions participate in the VLESS carrier and therefore need
// an explicit profile before the candidate reaches persistent validation.
func AddPeer(source configstore.Config, peer configstore.PeerConfig) (PeerMutationResult, error) {
	if peer.Name == "" || peer.NodeID == "" {
		return PeerMutationResult{}, publicerr.Errorf(
			CodePeerIdentityRequired,
			"peer name and node ID are required",
		)
	}
	if !validDirection(peer.Direction) {
		return PeerMutationResult{}, publicerr.Errorf(
			CodePeerDirectionInvalid,
			"peer direction %q is invalid",
			peer.Direction,
		)
	}
	if peer.Enabled && peer.XrayProfileID == "" {
		return PeerMutationResult{}, publicerr.Errorf(
			CodePeerProfileRequired,
			"enabled peer %q requires a VLESS profile",
			peer.Name,
		)
	}

	candidate := cloneConfig(source)
	if _, _, found := configstore.FindPeer(candidate.Peers, peer.Name); found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerExists, "peer %q already exists", peer.Name)
	}
	if _, _, found := configstore.FindPeer(candidate.Peers, peer.NodeID); found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerExists, "peer node ID %q already exists", peer.NodeID)
	}
	candidate.Peers = append(candidate.Peers, peer)
	return PeerMutationResult{Config: candidate, Peer: peer}, nil
}

// RemovePeer removes a directly managed peer and its node egress grant from a
// detached Config value. An inbound exit_peer reference prevents the entire
// mutation, including grant revocation.
func RemovePeer(source configstore.Config, peerRef string) (PeerMutationResult, error) {
	candidate := cloneConfig(source)

	peer, index, found := configstore.FindPeer(candidate.Peers, peerRef)
	if !found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerUnknown, "peer %q was not found", peerRef)
	}
	for _, inbound := range candidate.NodeInbound {
		if inbound.ExitPeer == peer.Name || inbound.ExitPeer == peer.NodeID {
			return PeerMutationResult{}, publicerr.Errorf(
				CodePeerInUse,
				"peer %q is referenced by inbound %q",
				peer.NodeID,
				inbound.Kind,
			)
		}
	}

	copy(candidate.Peers[index:], candidate.Peers[index+1:])
	candidate.Peers[len(candidate.Peers)-1] = configstore.PeerConfig{}
	candidate.Peers = candidate.Peers[:len(candidate.Peers)-1]

	_, revoked := candidate.NodeEgressGrants[peer.NodeID]
	if revoked {
		delete(candidate.NodeEgressGrants, peer.NodeID)
	}
	return PeerMutationResult{
		Config:                 candidate,
		Peer:                   peer,
		NodeEgressGrantRevoked: revoked,
	}, nil
}

// UpdatePeerDirection changes one peer's direction in a detached Config
// value. A grant cannot survive a direction that does not accept inbound
// traffic. Setting revokeNodeEgressGrant explicitly revokes an existing grant
// as part of the same value mutation.
func UpdatePeerDirection(
	source configstore.Config,
	peerRef string,
	direction route.Direction,
	revokeNodeEgressGrant bool,
) (PeerMutationResult, error) {
	if !validDirection(direction) {
		return PeerMutationResult{}, publicerr.Errorf(
			CodePeerDirectionInvalid,
			"peer direction %q is invalid",
			direction,
		)
	}
	candidate := cloneConfig(source)

	peer, index, found := configstore.FindPeer(candidate.Peers, peerRef)
	if !found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerUnknown, "peer %q was not found", peerRef)
	}
	_, hasGrant := candidate.NodeEgressGrants[peer.NodeID]
	if hasGrant && !direction.CanAcceptInbound() && !revokeNodeEgressGrant {
		return PeerMutationResult{}, publicerr.Errorf(
			CodeNodeEgressGrantRevokeRequired,
			"peer %q must revoke its node egress grant before losing inbound direction",
			peer.NodeID,
		)
	}

	peer.Direction = direction
	candidate.Peers[index] = peer
	if hasGrant && revokeNodeEgressGrant {
		delete(candidate.NodeEgressGrants, peer.NodeID)
	}
	return PeerMutationResult{
		Config:                 candidate,
		Peer:                   peer,
		NodeEgressGrantRevoked: hasGrant && revokeNodeEgressGrant,
	}, nil
}

// UpdatePeerProfile changes a peer's profile binding. A security quarantine is
// cleared only when the replacement credential is not Xray-equivalent to the
// compromised credential; enabling remains a separate explicit operation.
func UpdatePeerProfile(source configstore.Config, peerRef, profileID string) (PeerMutationResult, error) {
	candidate := cloneConfig(source)
	peer, index, found := configstore.FindPeer(candidate.Peers, peerRef)
	if !found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerUnknown, "peer %q was not found", peerRef)
	}
	peer.XrayProfileID = profileID
	if configstore.IsPeerCredentialQuarantined(peer) && replacementCredentialAllowed(candidate, peer.NodeID, profileID) {
		peer.DisabledCause = configstore.PeerCredentialRotatedDisabled
	}
	candidate.Peers[index] = peer
	return PeerMutationResult{Config: candidate, Peer: peer}, nil
}

// SetPeerEnabled changes one peer's enabled state in a detached Config value.
// Node egress grants intentionally survive temporary disable operations.
func SetPeerEnabled(
	source configstore.Config,
	peerRef string,
	enabled bool,
	disabledCause string,
) (PeerMutationResult, error) {
	candidate := cloneConfig(source)

	peer, index, found := configstore.FindPeer(candidate.Peers, peerRef)
	if !found {
		return PeerMutationResult{}, publicerr.Errorf(CodePeerUnknown, "peer %q was not found", peerRef)
	}
	if enabled && configstore.IsPeerCredentialQuarantined(peer) {
		return PeerMutationResult{}, publicerr.Errorf(
			CodePeerCredentialQuarantined,
			"peer %q must rotate to a new unique VLESS credential before enabling",
			peer.Name,
		)
	}
	peer.Enabled = enabled
	if enabled {
		peer.DisabledCause = ""
	} else if !configstore.IsPeerCredentialQuarantined(peer) {
		peer.DisabledCause = disabledCause
		if peer.DisabledCause == "" {
			peer.DisabledCause = "disabled"
		}
	}
	candidate.Peers[index] = peer
	if enabled {
		if err := configstore.Validate(candidate); err != nil {
			return PeerMutationResult{}, err
		}
	}
	return PeerMutationResult{Config: candidate, Peer: peer}, nil
}

func cloneConfig(source configstore.Config) configstore.Config {
	clone := source
	clone.NodeInbound = slices.Clone(source.NodeInbound)
	clone.Peers = clonePeers(source.Peers)
	clone.XrayProfiles = cloneXrayProfiles(source.XrayProfiles)
	clone.PeerTrust = clonePeerTrust(source.PeerTrust)
	clone.NodeEgressGrants = cloneNodeEgressGrants(source.NodeEgressGrants)
	clone.PeerCredentialQuarantines = clonePeerCredentialQuarantines(source.PeerCredentialQuarantines)
	return clone
}

func clonePeers(source []configstore.PeerConfig) []configstore.PeerConfig {
	clone := slices.Clone(source)
	for index := range clone {
		clone[index].Children = clonePeers(source[index].Children)
	}
	return clone
}

func cloneXrayProfiles(source map[string]configstore.XrayProfile) map[string]configstore.XrayProfile {
	clone := maps.Clone(source)
	for id, profile := range clone {
		if profile.VLESS != nil {
			value := *profile.VLESS
			profile.VLESS = &value
		}
		if profile.SOCKS != nil {
			value := *profile.SOCKS
			profile.SOCKS = &value
		}
		profile.Options = maps.Clone(profile.Options)
		clone[id] = profile
	}
	return clone
}

func clonePeerTrust(source map[string]configstore.PeerTrustGrant) map[string]configstore.PeerTrustGrant {
	clone := maps.Clone(source)
	for nodeID, grant := range clone {
		grant.Allow = slices.Clone(grant.Allow)
		clone[nodeID] = grant
	}
	return clone
}

func cloneNodeEgressGrants(source map[string]configstore.NodeEgressGrant) map[string]configstore.NodeEgressGrant {
	clone := maps.Clone(source)
	for nodeID, grant := range clone {
		grant.AllowCIDRs = slices.Clone(grant.AllowCIDRs)
		grant.AllowPrivateCIDRs = slices.Clone(grant.AllowPrivateCIDRs)
		grant.DenyCIDRs = slices.Clone(grant.DenyCIDRs)
		grant.AllowPorts = slices.Clone(grant.AllowPorts)
		clone[nodeID] = grant
	}
	return clone
}

func clonePeerCredentialQuarantines(source []configstore.PeerCredentialQuarantine) []configstore.PeerCredentialQuarantine {
	clone := slices.Clone(source)
	for index := range clone {
		clone[index].PeerNodeIDs = slices.Clone(source[index].PeerNodeIDs)
	}
	return clone
}

func validDirection(direction route.Direction) bool {
	switch direction {
	case route.DirectionInbound, route.DirectionOutbound, route.DirectionBidirectional:
		return true
	default:
		return false
	}
}

func replacementCredentialAllowed(cfg configstore.Config, peerNodeID, replacementProfileID string) bool {
	if replacementProfileID == "" {
		return false
	}
	replacement, replacementOK := cfg.XrayProfiles[replacementProfileID]
	if !replacementOK || replacement.Kind != "vless" || replacement.VLESS == nil {
		return false
	}
	if configstore.IsVLESSCredentialQuarantined(cfg, replacement.VLESS.UUID) {
		return false
	}
	for _, other := range cfg.Peers {
		if other.NodeID == peerNodeID {
			continue
		}
		profile, ok := cfg.XrayProfiles[other.XrayProfileID]
		if !ok || profile.Kind != "vless" || profile.VLESS == nil {
			continue
		}
		if sameVLESSCredential(replacement.VLESS.UUID, profile.VLESS.UUID) {
			return false
		}
	}
	return true
}

func sameVLESSCredential(first, second string) bool {
	firstKey, firstErr := xraycredential.VLESSKey(first)
	secondKey, secondErr := xraycredential.VLESSKey(second)
	return firstErr == nil && secondErr == nil && firstKey == secondKey
}

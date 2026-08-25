package xrayconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	stdnet "net"
	"strconv"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
	"github.com/FrankoonG/x-tier/internal/xrayrt"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/blackhole"
	"github.com/xtls/xray-core/proxy/socks"
	"github.com/xtls/xray-core/proxy/vless"
	vlessinbound "github.com/xtls/xray-core/proxy/vless/inbound"
	vlessoutbound "github.com/xtls/xray-core/proxy/vless/outbound"

	_ "github.com/xtls/xray-core/transport/internet/tcp"
)

const (
	EgressOutboundTag  = "xtier-egress"
	GenerationGuardTag = "xtier-generation-guard"
	UserSOCKSTag       = "xtier-user-socks"
	NodeVLESSTag       = "xtier-node-vless"
)

type RouteKind string

const (
	RouteUser    RouteKind = "user"
	RouteCarrier RouteKind = "carrier"
)

type InboundRoute struct {
	Kind       RouteKind
	PeerTag    string
	InboundTag string
}

type Compiled struct {
	Inbounds     []*core.InboundHandlerConfig
	Outbound     xrayrt.GenerationConfig
	Routes       map[string]InboundRoute
	PeerTags     map[string]string
	CarrierPeers map[string]string
	Listeners    map[string]string
}

type inboundPeerCredential struct {
	NodeID  string
	Profile configstore.XrayProfile
}

func Compile(cfg configstore.Config) (Compiled, error) {
	if err := configstore.Validate(cfg); err != nil {
		return Compiled{}, err
	}
	result := Compiled{
		Routes:       make(map[string]InboundRoute),
		PeerTags:     make(map[string]string),
		CarrierPeers: make(map[string]string),
		Listeners:    make(map[string]string),
	}
	outbounds := []*core.OutboundHandlerConfig{{
		Tag:           GenerationGuardTag,
		ProxySettings: serial.ToTypedMessage(&blackhole.Config{}),
	}}
	for _, peer := range cfg.Peers {
		if !peer.Enabled || !peer.Direction.CanDialOutbound() {
			continue
		}
		profile, ok := cfg.XrayProfiles[peer.XrayProfileID]
		if !ok {
			return Compiled{}, fmt.Errorf("xrayconfig: peer %q profile is missing", peer.Name)
		}
		outbound, tag, err := compileVLESSOutbound(peer, profile)
		if err != nil {
			return Compiled{}, err
		}
		outbounds = append(outbounds, outbound)
		if previous := result.PeerTags[peer.Name]; previous != "" && previous != tag {
			return Compiled{}, fmt.Errorf("xrayconfig: peer identifier %q is ambiguous", peer.Name)
		}
		if previous := result.PeerTags[peer.NodeID]; previous != "" && previous != tag {
			return Compiled{}, fmt.Errorf("xrayconfig: peer identifier %q is ambiguous", peer.NodeID)
		}
		result.PeerTags[peer.Name] = tag
		result.PeerTags[peer.NodeID] = tag
	}
	for _, inbound := range cfg.NodeInbound {
		if !inbound.Enabled {
			continue
		}
		switch inbound.Purpose {
		case "user":
			profile := cfg.XrayProfiles[inbound.XrayProfileID]
			compiled, err := compileSOCKSInbound(inbound, profile)
			if err != nil {
				return Compiled{}, err
			}
			peerTag := result.PeerTags[inbound.ExitPeer]
			if _, exists := result.Routes[UserSOCKSTag]; exists {
				return Compiled{}, fmt.Errorf("xrayconfig: duplicate user SOCKS inbound")
			}
			if peerTag == "" {
				return Compiled{}, fmt.Errorf("xrayconfig: user SOCKS exit peer is unavailable")
			}
			result.Inbounds = append(result.Inbounds, compiled)
			result.Routes[UserSOCKSTag] = InboundRoute{Kind: RouteUser, PeerTag: peerTag, InboundTag: UserSOCKSTag}
			result.Listeners[UserSOCKSTag] = inbound.Listen
		case "node":
			credentials, err := inboundPeerCredentials(cfg)
			if err != nil {
				return Compiled{}, err
			}
			compiled, carrierPeers, err := compileVLESSInbound(inbound, credentials)
			if err != nil {
				return Compiled{}, err
			}
			if _, exists := result.Routes[NodeVLESSTag]; exists {
				return Compiled{}, fmt.Errorf("xrayconfig: duplicate node VLESS inbound")
			}
			if len(carrierPeers) != 0 {
				result.Inbounds = append(result.Inbounds, compiled)
			}
			result.Routes[NodeVLESSTag] = InboundRoute{Kind: RouteCarrier, InboundTag: NodeVLESSTag}
			for account, nodeID := range carrierPeers {
				result.CarrierPeers[account] = nodeID
			}
			result.Listeners[NodeVLESSTag] = inbound.Listen
		default:
			return Compiled{}, fmt.Errorf("xrayconfig: inbound %q has unsupported purpose %q", inbound.Kind, inbound.Purpose)
		}
	}
	generation, err := xrayrt.NewGenerationConfig(outbounds)
	if err != nil {
		return Compiled{}, err
	}
	result.Outbound = generation
	return result, nil
}

func ValidateProfile(profile configstore.XrayProfile) error {
	switch profile.Kind {
	case "vless":
		if profile.VLESS == nil {
			return errors.New("xrayconfig: vless settings required")
		}
		if _, err := xraycredential.VLESSKey(profile.VLESS.UUID); err != nil {
			return fmt.Errorf("xrayconfig: invalid vless credential: %w", err)
		}
		if profile.VLESS.Transport != "tcp" || profile.VLESS.Security != "none" || !profile.VLESS.AllowInsecurePlaintext {
			return errors.New("xrayconfig: only explicitly allowed private VLESS/TCP plaintext is implemented")
		}
		if profile.VLESS.Flow != "" {
			return errors.New("xrayconfig: VLESS flow is not implemented in the initial slice")
		}
	case "socks":
		if profile.SOCKS == nil {
			return errors.New("xrayconfig: socks settings required")
		}
		if (profile.SOCKS.Username == "") != (profile.SOCKS.Password == "") {
			return errors.New("xrayconfig: SOCKS username and password must be set together")
		}
	default:
		return fmt.Errorf("xrayconfig: profile kind %q is unavailable", profile.Kind)
	}
	return nil
}

// CompileProfile validates a supported profile with the same protobuf builders
// used by the live data plane, without opening any listeners or connections.
func CompileProfile(profile configstore.XrayProfile) error {
	if err := ValidateProfile(profile); err != nil {
		return err
	}
	switch profile.Kind {
	case "vless":
		inbound := configstore.InboundConfig{
			Kind:    "node-vless",
			Purpose: "node",
			Listen:  "127.0.0.1:1",
			Enabled: true,
		}
		if _, _, err := compileVLESSInbound(inbound, []inboundPeerCredential{{
			NodeID: "profile-validation-peer", Profile: profile,
		}}); err != nil {
			return err
		}
		peer := configstore.PeerConfig{
			Name:          "profile-validation",
			NodeID:        "profile-validation-node",
			GatewayAddr:   "127.0.0.1:1",
			XrayProfileID: profile.ID,
		}
		_, _, err := compileVLESSOutbound(peer, profile)
		return err
	case "socks":
		_, err := compileSOCKSInbound(configstore.InboundConfig{
			Kind:          "socks",
			Purpose:       "user",
			Listen:        "127.0.0.1:1",
			Enabled:       true,
			XrayProfileID: profile.ID,
		}, profile)
		return err
	default:
		return fmt.Errorf("xrayconfig: profile kind %q is unavailable", profile.Kind)
	}
}

func compileSOCKSInbound(inbound configstore.InboundConfig, profile configstore.XrayProfile) (*core.InboundHandlerConfig, error) {
	if inbound.Kind != "socks" || profile.Kind != "socks" {
		return nil, errors.New("xrayconfig: incompatible SOCKS inbound")
	}
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	if profile.SOCKS.Username == "" || profile.SOCKS.Password == "" {
		return nil, errors.New("xrayconfig: SOCKS authentication is required")
	}
	if err := configstore.ValidateIsolatedPlaintextEndpoint(inbound.Listen); err != nil {
		return nil, fmt.Errorf("xrayconfig: SOCKS plaintext listen scope: %w", err)
	}
	receiver, err := receiverConfig(inbound.Listen)
	if err != nil {
		return nil, fmt.Errorf("xrayconfig: SOCKS listen: %w", err)
	}
	server := &socks.ServerConfig{
		AuthType:   socks.AuthType_PASSWORD,
		Accounts:   map[string]string{profile.SOCKS.Username: profile.SOCKS.Password},
		UdpEnabled: false,
	}
	return &core.InboundHandlerConfig{
		Tag:              UserSOCKSTag,
		ReceiverSettings: serial.ToTypedMessage(receiver),
		ProxySettings:    serial.ToTypedMessage(server),
	}, nil
}

func compileVLESSInbound(inbound configstore.InboundConfig, peers []inboundPeerCredential) (*core.InboundHandlerConfig, map[string]string, error) {
	if inbound.Kind != "node-vless" || inbound.Purpose != "node" || inbound.XrayProfileID != "" {
		return nil, nil, errors.New("xrayconfig: incompatible node VLESS inbound")
	}
	if err := configstore.ValidateIsolatedPlaintextEndpoint(inbound.Listen); err != nil {
		return nil, nil, fmt.Errorf("xrayconfig: VLESS plaintext listen scope: %w", err)
	}
	receiver, err := receiverConfig(inbound.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("xrayconfig: VLESS listen: %w", err)
	}
	users := make([]*protocol.User, 0, len(peers))
	identities := make(map[string]string, len(peers))
	credentials := make(map[string]string, len(peers))
	for _, peer := range peers {
		if peer.NodeID == "" {
			return nil, nil, errors.New("xrayconfig: inbound peer node identity is required")
		}
		if err := ValidateProfile(peer.Profile); err != nil {
			return nil, nil, fmt.Errorf("xrayconfig: inbound peer %q: %w", peer.NodeID, err)
		}
		credentialKey, err := xraycredential.VLESSKey(peer.Profile.VLESS.UUID)
		if err != nil {
			return nil, nil, fmt.Errorf("xrayconfig: inbound peer %q credential: %w", peer.NodeID, err)
		}
		if previous := credentials[credentialKey]; previous != "" && previous != peer.NodeID {
			return nil, nil, fmt.Errorf("xrayconfig: inbound peers %q and %q reuse a VLESS credential", previous, peer.NodeID)
		}
		credentials[credentialKey] = peer.NodeID
		handle := accountHandle("peer", peer.NodeID+"\x00"+credentialKey)
		identities[handle] = peer.NodeID
		users = append(users, &protocol.User{
			Email: handle,
			Account: serial.ToTypedMessage(&vless.Account{
				Id: peer.Profile.VLESS.UUID, Encryption: "none",
			}),
		})
	}
	return &core.InboundHandlerConfig{
		Tag:              NodeVLESSTag,
		ReceiverSettings: serial.ToTypedMessage(receiver),
		ProxySettings:    serial.ToTypedMessage(&vlessinbound.Config{Users: users}),
	}, identities, nil
}

func inboundPeerCredentials(cfg configstore.Config) ([]inboundPeerCredential, error) {
	result := make([]inboundPeerCredential, 0)
	for _, peer := range cfg.Peers {
		if !peer.Enabled || !peer.Direction.CanAcceptInbound() {
			continue
		}
		profile, ok := cfg.XrayProfiles[peer.XrayProfileID]
		if !ok || profile.Kind != "vless" || profile.VLESS == nil {
			return nil, fmt.Errorf("xrayconfig: inbound peer %q profile is missing or incompatible", peer.Name)
		}
		result = append(result, inboundPeerCredential{NodeID: peer.NodeID, Profile: profile})
	}
	return result, nil
}

func compileVLESSOutbound(peer configstore.PeerConfig, profile configstore.XrayProfile) (*core.OutboundHandlerConfig, string, error) {
	if profile.Kind != "vless" || profile.VLESS == nil {
		return nil, "", fmt.Errorf("xrayconfig: peer %q: incompatible VLESS profile", peer.Name)
	}
	if err := ValidateProfile(profile); err != nil {
		return nil, "", fmt.Errorf("xrayconfig: peer %q: %w", peer.Name, err)
	}
	credentialKey, err := xraycredential.VLESSKey(profile.VLESS.UUID)
	if err != nil {
		return nil, "", fmt.Errorf("xrayconfig: peer %q credential: %w", peer.Name, err)
	}
	address := peer.GatewayAddr
	if address == "" {
		address = peer.Addr
	}
	if err := configstore.ValidateIsolatedPlaintextEndpoint(address); err != nil {
		return nil, "", fmt.Errorf("xrayconfig: peer %q plaintext scope: %w", peer.Name, err)
	}
	host, port, err := splitEndpoint(address)
	if err != nil {
		return nil, "", fmt.Errorf("xrayconfig: peer %q address: %w", peer.Name, err)
	}
	tag := peerTag(peer.NodeID)
	return &core.OutboundHandlerConfig{
		Tag: tag,
		ProxySettings: serial.ToTypedMessage(&vlessoutbound.Config{Vnext: &protocol.ServerEndpoint{
			Address: xnet.NewIPOrDomain(xnet.ParseAddress(host)),
			Port:    uint32(port),
			User: &protocol.User{
				Email: accountHandle("node", peer.NodeID+"\x00"+credentialKey),
				Account: serial.ToTypedMessage(&vless.Account{
					Id:         profile.VLESS.UUID,
					Encryption: "none",
				}),
			},
		}}),
	}, tag, nil
}

func receiverConfig(address string) (*proxyman.ReceiverConfig, error) {
	host, port, err := splitEndpoint(address)
	if err != nil {
		return nil, err
	}
	ip := stdnet.ParseIP(host)
	if ip == nil {
		return nil, errors.New("listen host must be an IP literal")
	}
	return &proxyman.ReceiverConfig{
		PortList: &xnet.PortList{Range: []*xnet.PortRange{xnet.SinglePortRange(xnet.Port(port))}},
		Listen:   xnet.NewIPOrDomain(xnet.IPAddress(ip)),
	}, nil
}

func splitEndpoint(address string) (string, uint16, error) {
	host, rawPort, err := stdnet.SplitHostPort(address)
	if err != nil || host == "" {
		return "", 0, errors.New("host:port required")
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("port is out of range")
	}
	return host, uint16(port), nil
}

func peerTag(nodeID string) string {
	digest := sha256.Sum256([]byte(nodeID))
	return "peer-" + hex.EncodeToString(digest[:12])
}

func accountHandle(kind, id string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + id))
	return "x" + kind[:1] + "." + hex.EncodeToString(digest[:12])
}

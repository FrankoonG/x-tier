package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/settings"
)

type Config struct {
	Revision     int64                     `json:"revision"`
	Node         NodeConfig                `json:"node"`
	System       settings.Config           `json:"system"`
	NodeInbound  []InboundConfig           `json:"node_inbound,omitempty"`
	Peers        []PeerConfig              `json:"peers,omitempty"`
	XrayProfiles map[string]XrayProfile    `json:"xray_profiles,omitempty"`
	PeerTrust    map[string]PeerTrustGrant `json:"peer_trust,omitempty"`
}

type NodeConfig struct {
	NodeID            string `json:"node_id,omitempty"`
	DisplayName       string `json:"display_name,omitempty"`
	Role              string `json:"role,omitempty"`
	PublicKey         string `json:"public_key,omitempty"`
	RendrPersistentID string `json:"rendr_persistent_id,omitempty"`
	RendrInstanceID   string `json:"rendr_instance_id,omitempty"`
	RendrCapable      bool   `json:"rendr_capable"`
	Disabled          bool   `json:"disabled,omitempty"`
	DisabledCause     string `json:"disabled_cause,omitempty"`
}

type InboundConfig struct {
	Kind          string `json:"kind"`
	Listen        string `json:"listen"`
	Enabled       bool   `json:"enabled"`
	XrayProfileID string `json:"xray_profile_id,omitempty"`
	DisabledCause string `json:"disabled_cause,omitempty"`
}

type PeerConfig struct {
	Name          string          `json:"name"`
	NodeID        string          `json:"node_id"`
	DisplayName   string          `json:"display_name,omitempty"`
	Addr          string          `json:"addr,omitempty"`
	Direction     route.Direction `json:"direction"`
	XrayProfileID string          `json:"xray_profile_id,omitempty"`
	GatewayAddr   string          `json:"gateway_addr,omitempty"`
	NestedEnabled bool            `json:"nested_enabled"`
	Enabled       bool            `json:"enabled"`
	DisabledCause string          `json:"disabled_cause,omitempty"`
	RendrCapable  bool            `json:"rendr_capable"`
	InstanceID    string          `json:"rendr_instance_id,omitempty"`
	Children      []PeerConfig    `json:"children,omitempty"`
}

type XrayProfile struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Options map[string]string `json:"options,omitempty"`
}

type PeerTrustGrant struct {
	PeerNodeID string   `json:"peer_node_id"`
	Allow      []string `json:"allow"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Audit      bool     `json:"audit"`
}

func DefaultConfig() Config {
	return Config{
		System:       settings.Defaults(),
		XrayProfiles: map[string]XrayProfile{},
		PeerTrust:    map[string]PeerTrustGrant{},
	}
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	normalize(&cfg)
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405.000000000"))
		if b, readErr := os.ReadFile(path); readErr == nil {
			_ = os.WriteFile(backup, b, 0o600)
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func WithLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("config.locked: %w", err)
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	defer os.Remove(lockPath)
	return fn()
}

func Validate(cfg Config) error {
	if err := settings.Validate(cfg.System); err != nil {
		return err
	}
	seenProfiles := map[string]bool{}
	for id, p := range cfg.XrayProfiles {
		if id == "" || p.ID == "" || id != p.ID {
			return fmt.Errorf("config.profile_id_mismatch: %s", id)
		}
		if p.Kind == "" {
			return fmt.Errorf("config.profile_kind_required: %s", id)
		}
		seenProfiles[id] = true
	}
	if cfg.Node.Role != "" && cfg.Node.Role != "fat" && cfg.Node.Role != "thin" {
		return fmt.Errorf("config.role_invalid: %s", cfg.Node.Role)
	}
	for _, in := range cfg.NodeInbound {
		if in.Kind == "" {
			return fmt.Errorf("config.inbound_kind_required")
		}
		if in.Enabled && in.Listen == "" {
			return fmt.Errorf("config.inbound_listen_required: %s", in.Kind)
		}
		if in.XrayProfileID != "" && !seenProfiles[in.XrayProfileID] {
			return fmt.Errorf("config.profile_unknown: %s", in.XrayProfileID)
		}
	}
	return validatePeers(cfg.Peers, seenProfiles)
}

func ValidateRevision(cfg Config, want int64) error {
	if want >= 0 && cfg.Revision != want {
		return fmt.Errorf("config.revision_conflict: have %d want %d", cfg.Revision, want)
	}
	return nil
}

func FindPeer(peers []PeerConfig, name string) (PeerConfig, int, bool) {
	for i, p := range peers {
		if p.Name == name || p.NodeID == name {
			return p, i, true
		}
	}
	return PeerConfig{}, -1, false
}

func SortStable(cfg *Config) {
	sort.Slice(cfg.Peers, func(i, j int) bool { return cfg.Peers[i].Name < cfg.Peers[j].Name })
	sort.Slice(cfg.NodeInbound, func(i, j int) bool { return cfg.NodeInbound[i].Kind < cfg.NodeInbound[j].Kind })
}

func normalize(cfg *Config) {
	cfg.System = settings.ApplyDefaults(cfg.System)
	if cfg.XrayProfiles == nil {
		cfg.XrayProfiles = map[string]XrayProfile{}
	}
	if cfg.PeerTrust == nil {
		cfg.PeerTrust = map[string]PeerTrustGrant{}
	}
	if cfg.Node.RendrCapable == false {
		cfg.Node.RendrCapable = true
	}
	normalizePeers(cfg.Peers)
	SortStable(cfg)
}

func normalizePeers(peers []PeerConfig) {
	for i := range peers {
		if peers[i].NodeID == "" {
			peers[i].NodeID = peers[i].Name
		}
		if peers[i].DisplayName == "" {
			peers[i].DisplayName = peers[i].Name
		}
		if peers[i].Direction == "" {
			peers[i].Direction = route.DirectionOutbound
		}
		if !peers[i].Enabled && peers[i].DisabledCause == "" {
			// zero-value configs created by old callers should still be usable.
			peers[i].Enabled = true
		}
		if !peers[i].RendrCapable {
			peers[i].RendrCapable = true
		}
		if peers[i].InstanceID == "" && peers[i].NodeID != "" {
			peers[i].InstanceID = "inst-" + peers[i].NodeID
		}
		normalizePeers(peers[i].Children)
	}
}

func validatePeers(peers []PeerConfig, profiles map[string]bool) error {
	seen := map[string]bool{}
	for _, p := range peers {
		if p.Name == "" {
			return fmt.Errorf("config.peer_name_required")
		}
		if p.NodeID == "" {
			return fmt.Errorf("config.peer_node_id_required: %s", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("config.peer_duplicate: %s", p.Name)
		}
		seen[p.Name] = true
		if p.Direction != route.DirectionInbound && p.Direction != route.DirectionOutbound && p.Direction != route.DirectionBidirectional {
			return fmt.Errorf("config.peer_direction_invalid: %s", p.Direction)
		}
		if p.Direction.CanDialOutbound() && p.GatewayAddr == "" && p.Addr == "" {
			return fmt.Errorf("config.peer_gateway_required: %s", p.Name)
		}
		if p.XrayProfileID != "" && !profiles[p.XrayProfileID] {
			return fmt.Errorf("config.profile_unknown: %s", p.XrayProfileID)
		}
		if err := validatePeers(p.Children, profiles); err != nil {
			return err
		}
	}
	return nil
}

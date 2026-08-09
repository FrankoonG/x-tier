package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/route"
)

func TestSettingsRejectsHardLimitWithoutWriting(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Revision: 3,
		Node:     node("A"),
		System:   configstore.DefaultConfig().System,
	})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "local", "settings", "set", "--max-nested-depth", "99")
	if code == 0 {
		t.Fatalf("expected failure, output=%s", out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 3 {
		t.Fatalf("revision changed: %d", cfg.Revision)
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	path := seedConfig(t, configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "--dry-run", "local", "peer", "add", "B", "--node-id", "B", "--addr", "b:19080", "--direction", "outbound")
	if code != 0 {
		t.Fatalf("dry run failed: %s", out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Peers) != 0 || cfg.Revision != 0 {
		t.Fatalf("dry run wrote config: peers=%d rev=%d", len(cfg.Peers), cfg.Revision)
	}
}

func TestStaleRevisionIsRejected(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 4, Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "--revision", "3", "local", "identity", "rename", "new-a")
	if code == 0 {
		t.Fatalf("expected stale revision failure: %s", out)
	}
	if got := jsonField(t, out, "error_code"); got != "config.revision_conflict" {
		t.Fatalf("error_code = %s, output=%s", got, out)
	}
}

func TestPeerTrustRejectsFatNodeOnlyScope(t *testing.T) {
	path := seedConfig(t, configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "local", "peer", "trust", "set", "B", "--allow", "users.write")
	if code == 0 {
		t.Fatalf("expected scope failure: %s", out)
	}
	if got := jsonField(t, out, "error_code"); got != "peer_trust.scope_forbidden" {
		t.Fatalf("error_code = %s, output=%s", got, out)
	}
}

func TestPathCompileHonorsNestedAndDirection(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:   node("A"),
		System: configstore.DefaultConfig().System,
		Peers: []configstore.PeerConfig{{
			Name:          "B",
			NodeID:        "B",
			Direction:     route.DirectionOutbound,
			GatewayAddr:   "b:19080",
			NestedEnabled: false,
			Enabled:       true,
			RendrCapable:  true,
			InstanceID:    "inst-B",
			Children: []configstore.PeerConfig{{
				Name:          "C",
				NodeID:        "C",
				Direction:     route.DirectionOutbound,
				GatewayAddr:   "c:19080",
				NestedEnabled: true,
				Enabled:       true,
				RendrCapable:  true,
				InstanceID:    "inst-C",
			}},
		}, {
			Name:          "I",
			NodeID:        "I",
			Direction:     route.DirectionInbound,
			GatewayAddr:   "i:19080",
			NestedEnabled: true,
			Enabled:       true,
			RendrCapable:  true,
			InstanceID:    "inst-I",
		}},
	})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "B/C")
	if code == 0 || jsonField(t, out, "error_code") != "path.nested_disabled" {
		t.Fatalf("expected nested failure, code=%d output=%s", code, out)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "I")
	if code == 0 || jsonField(t, out, "error_code") != "path.edge_not_outbound" {
		t.Fatalf("expected inbound failure, code=%d output=%s", code, out)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "local", "peer", "set", "B", "--nested=true")
	if code != 0 {
		t.Fatalf("set nested: %s", out)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "B/C")
	if code != 0 {
		t.Fatalf("compile nested path: %s", out)
	}
	if got := jsonField(t, out, "ok"); got != "true" {
		t.Fatalf("ok = %s output=%s", got, out)
	}
}

func TestProfileInUseCannotBeRemoved(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.Node = node("A")
	cfg.XrayProfiles["p1"] = configstore.XrayProfile{ID: "p1", Kind: "vless-reality"}
	cfg.Peers = []configstore.PeerConfig{{
		Name:          "B",
		NodeID:        "B",
		Direction:     route.DirectionOutbound,
		GatewayAddr:   "b:19080",
		XrayProfileID: "p1",
		Enabled:       true,
		RendrCapable:  true,
	}}
	path := seedConfig(t, cfg)
	code, out := runCLI(t, "--offline", "--config", path, "--json", "local", "xray", "profile", "remove", "p1")
	if code == 0 || jsonField(t, out, "error_code") != "config.in_use" {
		t.Fatalf("expected in-use failure, code=%d output=%s", code, out)
	}
}

func TestDefaultCLIUsesDaemonControl(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got controlapi.Request
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/command" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(controlapi.Response{ExitCode: 0, Stdout: `{"ok":true,"from":"daemon"}` + "\n"})
	})}
	go func() { _ = server.Serve(ln) }()
	defer server.Close()

	code, out := runCLI(t, "--control", ln.Addr().String(), "--json", "local", "status")
	if code != 0 {
		t.Fatalf("daemon CLI failed: %s", out)
	}
	if got.JSON != true || strings.Join(got.Args, " ") != "local status" {
		t.Fatalf("unexpected daemon request: %+v", got)
	}
	if jsonField(t, out, "from") != "daemon" {
		t.Fatalf("expected daemon output, got %s", out)
	}
}

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	if stdout.Len() > 0 {
		return code, stdout.String()
	}
	return code, stderr.String()
}

func seedConfig(t *testing.T, cfg configstore.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if cfg.System.LogLevel == "" {
		cfg.System = configstore.DefaultConfig().System
	}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

func node(id string) configstore.NodeConfig {
	return configstore.NodeConfig{NodeID: id, DisplayName: id, Role: "thin", RendrCapable: true, RendrInstanceID: "inst-" + id}
}

func jsonField(t *testing.T, raw, name string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("json decode %q: %v", raw, err)
	}
	v, ok := obj[name]
	if !ok {
		t.Fatalf("missing field %s in %s", name, raw)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

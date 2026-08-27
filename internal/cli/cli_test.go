package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/configops"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/webbridge"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

type metadataOnlyError struct{}

func (metadataOnlyError) Error() string           { return "diagnostic text without a code prefix" }
func (metadataOnlyError) PublicErrorCode() string { return "config.peer_name_required" }

func TestErrorCodeUsesTypedMetadataWithoutParsingMessage(t *testing.T) {
	if got := errorCode(metadataOnlyError{}); got != "config.peer_name_required" {
		t.Fatalf("errorCode()=%q", got)
	}
	if got := errorCode(errors.New(`C:\private.path\failure`)); got != "operation.failed" {
		t.Fatalf("untyped path errorCode()=%q", got)
	}
}

func TestSettingsRejectsHardLimitWithoutWriting(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Revision: 3,
		Node:     node("A"),
		System:   configstore.DefaultConfig().System,
	})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "3", "local", "settings", "set", "--max-nested-depth", "99")
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
	cfg := configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System, XrayProfiles: runtimeTestProfiles()}
	path := seedConfig(t, cfg)
	code, out := runCLI(t, "--offline", "--config", path, "--json", "--dry-run", "--revision", "0", "local", "peer", "add", "B", "--node-id", "node-b", "--addr", "10.20.0.2:19080", "--direction", "outbound", "--profile", "vless")
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

func TestDaemonCommandsFailClosedWhenLiveConfigIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"--offline", "--config", path, "--json", "local", "settings", "show"},
		{"--offline", "--config", path, "--json", "--dry-run", "--revision", "0", "local", "settings", "set", "--log-level", "debug"},
		{"--offline", "--config", path, "--json", "--revision", "0", "local", "settings", "set", "--log-level", "debug"},
	}
	for _, args := range commands {
		if code, output := runDaemonCLI(t, args...); code == 0 {
			t.Fatalf("daemon command unexpectedly succeeded: args=%v output=%s", args, output)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("daemon command recreated missing config: args=%v err=%v", args, err)
		}
	}
}

func TestInboundRewritePreservesDisabledState(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.Node = node("A")
	cfg.XrayProfiles = runtimeTestProfiles()
	cfg.NodeInbound = []configstore.InboundConfig{{
		Kind: "node-vless", Purpose: "node", Listen: "127.0.0.1:19080",
		Enabled: false, DisabledCause: "maintenance",
	}}
	path := seedConfig(t, cfg)
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0",
		"local", "inbound", "set", "node-vless", "--listen", "127.0.0.1:29080")
	if code != 0 {
		t.Fatalf("rewrite disabled inbound: %s", out)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].Enabled ||
		loaded.NodeInbound[0].DisabledCause != "maintenance" ||
		loaded.NodeInbound[0].Listen != "127.0.0.1:29080" {
		t.Fatalf("rewrite changed administrative state: %+v", loaded.NodeInbound)
	}
}

func TestInboundSetDoesNotIgnoreExplicitEmptyRequiredFields(t *testing.T) {
	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "listen", flag: "--listen"},
		{name: "profile", flag: "--profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := configstore.DefaultConfig()
			cfg.Node = node("A")
			cfg.XrayProfiles = runtimeTestProfiles()
			cfg.NodeInbound = []configstore.InboundConfig{{
				Kind: "node-vless", Purpose: "node", Listen: "127.0.0.1:19080",
				Enabled: true,
			}}
			path := seedConfig(t, cfg)
			code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0",
				"local", "inbound", "set", "node-vless", test.flag, "")
			if code == 0 {
				t.Fatalf("explicit empty %s was silently ignored: %s", test.name, out)
			}
			loaded, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Revision != 0 || len(loaded.NodeInbound) != 1 ||
				loaded.NodeInbound[0].Listen != "127.0.0.1:19080" {
				t.Fatalf("failed explicit-empty update changed config: %+v", loaded)
			}
		})
	}
}

func TestStaleRevisionIsRejected(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 4, Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "3", "local", "identity", "rename", "new-a")
	if code == 0 {
		t.Fatalf("expected stale revision failure: %s", out)
	}
	if got := jsonField(t, out, "error_code"); got != "config.revision_conflict" {
		t.Fatalf("error_code = %s, output=%s", got, out)
	}
}

func TestPeerTrustRejectsFatNodeOnlyScope(t *testing.T) {
	path := seedConfig(t, configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "peer", "trust", "set", "B", "--allow", "users.write")
	if code == 0 {
		t.Fatalf("expected scope failure: %s", out)
	}
	if got := jsonField(t, out, "error_code"); got != "peer_trust.scope_forbidden" {
		t.Fatalf("error_code = %s, output=%s", got, out)
	}
}

func TestPathCompileHonorsNestedAndDirection(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:         node("A"),
		System:       configstore.DefaultConfig().System,
		XrayProfiles: runtimeTestProfiles(),
		Peers: []configstore.PeerConfig{{
			Name:          "B",
			NodeID:        "node-b",
			Direction:     route.DirectionOutbound,
			GatewayAddr:   "10.20.0.2:19080",
			NestedEnabled: false,
			Enabled:       true,
			RendrCapable:  true,
			XrayProfileID: "vless",
			InstanceID:    "inst-B",
			Children: []configstore.PeerConfig{{
				Name:          "C",
				NodeID:        "node-c",
				Direction:     route.DirectionOutbound,
				GatewayAddr:   "10.20.0.3:19080",
				NestedEnabled: true,
				Enabled:       true,
				RendrCapable:  true,
				InstanceID:    "inst-C",
			}},
		}, {
			Name:          "I",
			NodeID:        "node-i",
			Direction:     route.DirectionInbound,
			GatewayAddr:   "i:19080",
			NestedEnabled: true,
			Enabled:       true,
			RendrCapable:  true,
			XrayProfileID: "vless-in",
			InstanceID:    "inst-I",
		}},
	})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "node-b/node-c")
	if code == 0 || jsonField(t, out, "error_code") != "path.nested_disabled" {
		t.Fatalf("expected nested failure, code=%d output=%s", code, out)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "node-i")
	if code == 0 || jsonField(t, out, "error_code") != "path.edge_not_outbound" {
		t.Fatalf("expected inbound failure, code=%d output=%s", code, out)
	}
	code, out = runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "peer", "set", "B", "--nested=true")
	if code != 0 {
		t.Fatalf("set nested: %s", out)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "path", "compile", "node-b/node-c")
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
	cfg.XrayProfiles["p1"] = configstore.XrayProfile{ID: "p1", Kind: "vless", VLESS: &configstore.VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.Peers = []configstore.PeerConfig{{
		Name:          "B",
		NodeID:        "node-b",
		Direction:     route.DirectionOutbound,
		GatewayAddr:   "10.20.0.2:19080",
		XrayProfileID: "p1",
		Enabled:       true,
		RendrCapable:  true,
	}}
	path := seedConfig(t, cfg)
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "xray", "profile", "remove", "p1")
	if code == 0 || jsonField(t, out, "error_code") != "config.in_use" {
		t.Fatalf("expected in-use failure, code=%d output=%s", code, out)
	}
}

func TestObservedChildProfileReferenceDoesNotBlockRemoval(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.XrayProfiles["local"] = configstore.XrayProfile{ID: "local", Kind: "vless", VLESS: &configstore.VLESSProfile{
		UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.Peers = []configstore.PeerConfig{{
		Name: "managed", NodeID: "managed-id", Direction: route.DirectionInbound,
		Children: []configstore.PeerConfig{{
			Name: "observed", NodeID: "observed-id", Direction: route.DirectionInbound, XrayProfileID: "local",
		}},
	}}
	path := seedConfig(t, cfg)
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "xray", "profile", "remove", "local")
	if code != 0 {
		t.Fatalf("observed child blocked local profile removal: %s", out)
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 {
		t.Fatalf("revision = %d, want 1", loaded.Revision)
	}
	if _, exists := loaded.XrayProfiles["local"]; exists {
		t.Fatal("local profile was not removed")
	}
}

func TestSettingsSetRejectsEmptyPatch(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "settings", "set")
	if code == 0 || jsonField(t, out, "error_code") != "settings.patch_empty" {
		t.Fatalf("empty settings patch code=%d output=%s", code, out)
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 {
		t.Fatalf("empty settings patch changed revision to %d", loaded.Revision)
	}
}

func TestDefaultCLIUsesDaemonControl(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got controlapi.Request
	configPath := filepath.Join(t.TempDir(), "config.json")
	token := createCLIStoreToken(t, configPath, statestore.ControlToken)
	server := &http.Server{Handler: authenticatedCLITestHandler(t, token, http.MethodPost, controlapi.CommandPath, func(body []byte) (int, []byte) {
		if err := json.Unmarshal(body, &got); err != nil {
			return http.StatusBadRequest, []byte(err.Error())
		}
		response, err := json.Marshal(controlapi.Response{ExitCode: 0, Stdout: `{"ok":true,"from":"daemon"}` + "\n"})
		if err != nil {
			t.Fatal(err)
		}
		return http.StatusOK, response
	})}
	go func() { _ = server.Serve(ln) }()
	defer server.Close()

	code, out := runCLI(t, "--config", configPath, "--control", ln.Addr().String(), "--json", "local", "status")
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

func TestDaemonMutationTransportFailureReportsIndeterminateOutcome(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	token := createCLIStoreToken(t, configPath, statestore.ControlToken)
	challenge, err := controlapi.SignChallenge(token, strings.Repeat("cd", 32), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == controlapi.ChallengePath {
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil || !controlapi.VerifyRequestAuthentication(r, token, challenge, body) {
			http.Error(w, "invalid request", http.StatusUnauthorized)
			return
		}
		connection, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr != nil {
			t.Errorf("hijack: %v", hijackErr)
			return
		}
		_ = connection.Close()
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	code, output := runCLI(
		t,
		"--config", configPath,
		"--control", listener.Addr().String(),
		"--json",
		"--revision", "0",
		"local", "identity", "rename", "unknown",
	)
	result := decodeJSON(t, output)
	if code != controlapi.IndeterminateExitCode || result["ok"] != false ||
		result["applied"] != false || result["outcome"] != string(controlapi.MutationOutcomeIndeterminate) ||
		result["error_code"] != "control.mutation_outcome_indeterminate" {
		t.Fatalf("transport failure code=%d result=%#v", code, result)
	}
	requestID, _ := result["request_id"].(string)
	if len(requestID) != 32 {
		t.Fatalf("transport failure request_id=%q", requestID)
	}
}

func TestDaemonMutationPreDeliveryFailureReportsNotApplied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	_ = createCLIStoreToken(t, configPath, statestore.ControlToken)

	code, output := runCLI(
		t,
		"--config", configPath,
		"--control", addr,
		"--json",
		"--revision", "0",
		"local", "identity", "rename", "rejected",
	)
	result := decodeJSON(t, output)
	if code != 1 || result["applied"] != false ||
		result["outcome"] != string(controlapi.MutationOutcomeNotApplied) ||
		result["error_code"] != "control.mutation_rejected" {
		t.Fatalf("pre-delivery failure code=%d result=%#v", code, result)
	}
}

func TestDaemonMutationRendersSignedOutcomeMetadata(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	token := createCLIStoreToken(t, configPath, statestore.ControlToken)
	server := &http.Server{Handler: authenticatedCLITestHandler(t, token, http.MethodPost, controlapi.CommandPath, func([]byte) (int, []byte) {
		response, err := json.Marshal(controlapi.Response{
			ExitCode: 0,
			Stdout:   `{"ok":true,"changed":true}` + "\n",
			Applied:  true,
			Outcome:  controlapi.MutationOutcomeApplied,
		})
		if err != nil {
			t.Fatal(err)
		}
		return http.StatusOK, response
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	code, output := runCLI(
		t,
		"--config", configPath,
		"--control", listener.Addr().String(),
		"--json",
		"--revision", "0",
		"local", "identity", "rename", "applied",
	)
	result := decodeJSON(t, output)
	if code != 0 || result["ok"] != true || result["applied"] != true ||
		result["outcome"] != string(controlapi.MutationOutcomeApplied) {
		t.Fatalf("applied mutation code=%d result=%#v", code, result)
	}
	requestID, _ := result["request_id"].(string)
	if len(requestID) != 32 {
		t.Fatalf("applied mutation request_id=%q", requestID)
	}
}

func TestDaemonMutationAppliedOutcomeForcesSuccessfulProcessExit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := writeDaemonMutationResponse(
		globals{json: true},
		&stdout,
		&stderr,
		"2123456789abcdef0123456789abcdef",
		controlapi.Response{
			ExitCode: 1,
			Stdout:   `{"ok":false,"error_code":"config.commit_visible_and_resynced","message":"commit applied with a durability warning"}`,
			Applied:  true,
			Outcome:  controlapi.MutationOutcomeApplied,
		},
	)
	result := decodeJSON(t, stdout.String())
	if code != 0 || result["applied"] != true ||
		result["outcome"] != string(controlapi.MutationOutcomeApplied) {
		t.Fatalf("applied mutation code=%d result=%#v stderr=%q", code, result, stderr.String())
	}
}

func TestDaemonStatusUsesLiveProviderStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	configPath := filepath.Join(t.TempDir(), "config.json")
	token := createCLIStoreToken(t, configPath, statestore.ControlToken)
	providerStatus := controlapi.DaemonStatus{
		APIVersion:  controlapi.APIVersion,
		State:       controlapi.DaemonStateRunning,
		Revision:    17,
		ConfigPath:  configPath,
		ControlAddr: ln.Addr().String(),
		Rendr: controlapi.RuntimeStatus{
			State:      controlapi.RuntimeStateRunning,
			InstanceID: "live-rendr-instance",
		},
		Xray: controlapi.XrayStatus{
			State:                controlapi.RuntimeStateRunning,
			Draining:             []controlapi.XrayGenerationStatus{},
			StrictStreamOutbound: true,
		},
	}
	server := &http.Server{Handler: authenticatedCLITestHandler(t, token, http.MethodGet, controlapi.StatusPath, func([]byte) (int, []byte) {
		body, err := json.Marshal(providerStatus)
		if err != nil {
			t.Fatal(err)
		}
		return http.StatusOK, body
	})}
	go func() { _ = server.Serve(ln) }()
	defer server.Close()

	code, out := runCLI(t, "--config", configPath, "--control", ln.Addr().String(), "--json", "daemon", "status")
	if code != 0 {
		t.Fatalf("daemon status failed: %s", out)
	}
	daemonStatus := objectField(t, decodeJSON(t, out), "daemon")
	if daemonStatus["state"] != string(controlapi.DaemonStateRunning) || daemonStatus["revision"] != float64(17) {
		t.Fatalf("unexpected daemon status: %s", out)
	}
	rendr := objectField(t, daemonStatus, "rendr")
	xray := objectField(t, daemonStatus, "xray")
	if rendr["instance_id"] != "live-rendr-instance" || xray["state"] != string(controlapi.RuntimeStateRunning) ||
		xray["strict_stream_outbound"] != true || xray["strict_packet_outbound"] != false {
		t.Fatalf("provider runtime status was not preserved: %s", out)
	}
}

func authenticatedCLITestHandler(
	t *testing.T,
	token, method, path string,
	handle func([]byte) (int, []byte),
) http.Handler {
	t.Helper()
	nonce := strings.Repeat("ab", 32)
	challenge, err := controlapi.SignChallenge(token, nonce, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == controlapi.ChallengePath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		if r.Method != method || r.URL.Path != path {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request read failed", http.StatusBadRequest)
			return
		}
		if !controlapi.VerifyRequestAuthentication(r, token, challenge, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		status, response := handle(body)
		if err := controlapi.SignResponse(w.Header(), token, r, body, status, response); err != nil {
			http.Error(w, "response signing failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(response)
	})
}

func TestDaemonStatusRejectsOfflineMode(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	code, out := runCLI(t, "--offline", "--config", path, "--json", "daemon", "status")
	if code == 0 || jsonField(t, out, "error_code") != "daemon.status_requires_control" {
		t.Fatalf("offline daemon status code=%d output=%s", code, out)
	}
}

func TestOfflineMutationIsRejectedWithoutWriting(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 2, Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runCLI(t, "--offline", "--config", path, "--json", "--revision", "2", "local", "identity", "rename", "blocked")
	if code == 0 || jsonField(t, out, "error_code") != "cli.offline_read_only" {
		t.Fatalf("offline mutation result code=%d output=%s", code, out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node.DisplayName != "A" || cfg.Revision != 2 {
		t.Fatalf("offline command wrote config: node=%+v revision=%d", cfg.Node, cfg.Revision)
	}
}

func TestDaemonMutationRequiresRevision(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 2, Node: node("A"), System: configstore.DefaultConfig().System})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "local", "identity", "rename", "blocked")
	if code == 0 || jsonField(t, out, "error_code") != "config.revision_required" {
		t.Fatalf("missing revision result code=%d output=%s", code, out)
	}
}

func TestOwnedDaemonMutationHonorsCanceledContextBeforeDispatch(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 2, Node: node("A"), System: configstore.DefaultConfig().System})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	result := RunOwnedDaemonContext(
		ctx,
		[]string{"--offline", "--config", path, "--json", "--revision", "2", "local", "identity", "rename", "blocked"},
		"",
		&stdout,
		&stderr,
	)
	if result.ExitCode == 0 || result.Applied || result.Outcome != controlapi.MutationOutcomeNotApplied {
		t.Fatalf("canceled daemon mutation result=%+v stdout=%s stderr=%s", result, stdout.String(), stderr.String())
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || loaded.Node.DisplayName != "A" {
		t.Fatalf("canceled daemon mutation changed config: %+v", loaded)
	}
}

func TestIdentityShowAndStatusCoverBackingStates(t *testing.T) {
	tests := []struct {
		name         string
		state        string
		withBacking  bool
		configure    func(t *testing.T, cfg *configstore.Config, backing *identity.Identity)
		aclQualified bool
	}{
		{
			name:         "backed",
			state:        identityStateBacked,
			withBacking:  true,
			aclQualified: true,
			configure: func(_ *testing.T, cfg *configstore.Config, backing *identity.Identity) {
				setPublicIdentity(cfg, backing.Public())
			},
		},
		{
			name:         "recoverable",
			state:        identityStateRecoverable,
			withBacking:  true,
			aclQualified: true,
		},
		{
			name:  "legacy unbacked",
			state: identityStateLegacyUnbacked,
			configure: func(_ *testing.T, cfg *configstore.Config, _ *identity.Identity) {
				cfg.Node.NodeID = legacyNodeID("legacy")
			},
		},
		{
			name:  "v2 backing missing",
			state: identityStateBackingMissing,
			configure: func(t *testing.T, cfg *configstore.Config, _ *identity.Identity) {
				configured := createIdentity(t, filepath.Join(t.TempDir(), "configured-seed.json"))
				setPublicIdentity(cfg, configured.Public())
			},
		},
		{
			name:         "mismatch",
			state:        identityStateMismatch,
			withBacking:  true,
			aclQualified: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			cfg := configstore.DefaultConfig()
			var backing *identity.Identity
			if tc.withBacking {
				store := openCLIStore(t, path)
				var err error
				backing, err = identity.CreateStore(store)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "mismatch" {
				other := createIdentity(t, filepath.Join(dir, "other-keystore", "node-seed.v1.json"))
				setPublicIdentity(&cfg, other.Public())
			} else if tc.configure != nil {
				tc.configure(t, &cfg, backing)
			}
			if err := configstore.Save(path, cfg); err != nil {
				t.Fatal(err)
			}

			for _, command := range [][]string{{"local", "identity", "show"}, {"local", "status"}} {
				code, out := runCLI(t, append([]string{"--offline", "--config", path, "--json"}, command...)...)
				if code != 0 {
					t.Fatalf("%v failed: %s", command, out)
				}
				got := decodeJSON(t, out)
				identityObject := objectField(t, got, "identity")
				if identityObject["state"] != tc.state {
					t.Fatalf("%v state=%v want=%s output=%s", command, identityObject["state"], tc.state, out)
				}
				if identityObject["os_acl_release_qualified"] != tc.aclQualified {
					t.Fatalf("%v ACL qualification=%v want=%v output=%s", command, identityObject["os_acl_release_qualified"], tc.aclQualified, out)
				}
			}
		})
	}
}

func TestIdentityInitCreatesBackedIdentityWithoutLeakingSeed(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	store := openCLIStore(t, path)
	code, out := runStoreDaemonCLI(t, store, "--json", "--revision", "0", "local", "identity", "init", "--name", "alpha")
	if code != 0 {
		t.Fatalf("identity init failed: %s", out)
	}

	seedBytes, err := store.Read(statestore.IdentitySeed, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(seedBytes, &envelope); err != nil {
		t.Fatal(err)
	}
	seedMaterial := fmt.Sprint(envelope["seed"])
	if seedMaterial == "" || strings.Contains(out, seedMaterial) {
		t.Fatalf("identity init leaked seed material: %s", out)
	}
	assertNoSensitiveKeys(t, decodeJSON(t, out))

	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 1 || cfg.Node.NodeID == "" || cfg.Node.PublicKey == "" || cfg.Node.DisplayName != "alpha" || cfg.Node.Role != "" {
		t.Fatalf("unexpected initialized config: revision=%d node=%+v", cfg.Revision, cfg.Node)
	}
	code, out = runCLI(t, "--offline", "--config", path, "--json", "local", "identity", "show")
	if code != 0 {
		t.Fatalf("identity show failed: %s", out)
	}
	state := objectField(t, decodeJSON(t, out), "identity")
	if state["state"] != identityStateBacked || state["os_acl_release_qualified"] != true {
		t.Fatalf("identity is not backed by platform-qualified storage: %s", out)
	}
}

func TestSameDirectoryConfigsUseIndependentIdentityBackings(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "a"),
		filepath.Join(dir, "a.last-good"),
	}
	for _, path := range paths {
		if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
			t.Fatal(err)
		}
	}

	stores := []*statestore.Store{openCLIStore(t, paths[0]), openCLIStore(t, paths[1])}
	for index, store := range stores {
		code, out := runStoreDaemonCLI(
			t, store, "--json", "--revision", "0",
			"local", "identity", "init", "--name", fmt.Sprintf("node-%d", index),
		)
		if code != 0 {
			t.Fatalf("identity init for %q failed: %s", store.ConfigPath(), out)
		}
	}

	if stores[0].ConfigKey() == stores[1].ConfigKey() {
		t.Fatalf("same-directory configs share state key %q", stores[0].ConfigKey())
	}
	first, err := configstore.LoadStoreExisting(stores[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := configstore.LoadStoreExisting(stores[1])
	if err != nil {
		t.Fatal(err)
	}
	if first.Node.NodeID == "" || second.Node.NodeID == "" || first.Node.NodeID == second.Node.NodeID {
		t.Fatalf("identity backings are not independent: first=%q second=%q", first.Node.NodeID, second.Node.NodeID)
	}
	for _, store := range stores {
		if _, err := identity.LoadStore(store); err != nil {
			t.Fatalf("load identity backing for %q: %v", store.ConfigPath(), err)
		}
	}
}

func TestOwnedCLIUsesCanonicalParentForPrivateState(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	aliasDir := filepath.Join(root, "alias")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	realPath := filepath.Join(realDir, "config.json")
	aliasPath := filepath.Join(aliasDir, "config.json")
	if err := configstore.Save(realPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	realStore := openCLIStore(t, realPath)
	canonical, err := configstore.CanonicalPath(aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	aliasStore, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aliasStore.Close() })
	if realStore.ConfigKey() != aliasStore.ConfigKey() {
		t.Fatalf("parent alias resolved a different state key: real=%q alias=%q", realStore.ConfigKey(), aliasStore.ConfigKey())
	}

	code, out := runStoreDaemonCLI(
		t, realStore, "--json", "--revision", "0",
		"local", "identity", "init", "--name", "canonical",
	)
	if code != 0 {
		t.Fatalf("identity init failed: %s", out)
	}
	code, out = runStoreDaemonCLI(t, aliasStore, "--json", "local", "identity", "show")
	if code != 0 {
		t.Fatalf("identity show through parent alias failed: %s", out)
	}
	state := objectField(t, decodeJSON(t, out), "identity")
	if state["state"] != identityStateBacked {
		t.Fatalf("parent alias resolved different private state: %s", out)
	}
	realIdentity, err := identity.LoadStore(realStore)
	if err != nil {
		t.Fatal(err)
	}
	aliasIdentity, err := identity.LoadStore(aliasStore)
	if err != nil {
		t.Fatal(err)
	}
	if realIdentity.Public() != aliasIdentity.Public() {
		t.Fatalf("canonical parent resolved a different identity backing")
	}
}

func TestIdentityInitRejectsRemovedRoleWithoutWriting(t *testing.T) {
	for _, roleArgs := range [][]string{{"--role=fat"}, {"--role", "thin"}} {
		t.Run(strings.Join(roleArgs, "_"), func(t *testing.T) {
			path := seedConfig(t, configstore.DefaultConfig())
			args := []string{"--offline", "--config", path, "--json", "--revision", "0", "local", "identity", "init"}
			code, out := runDaemonCLI(t, append(args, roleArgs...)...)
			if code == 0 || jsonField(t, out, "error_code") != "identity.role_removed" {
				t.Fatalf("removed role code=%d output=%s", code, out)
			}
			if _, err := os.Stat(identitySeedPath(path)); !os.IsNotExist(err) {
				t.Fatalf("removed role created backing: %v", err)
			}
			cfg, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Revision != 0 || cfg.Node.Role != "" || cfg.Node.NodeID != "" {
				t.Fatalf("removed role wrote config: %+v", cfg)
			}
		})
	}
}

func TestIdentityInitPreservesLegacyRoleReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	backing := createIdentity(t, identitySeedPath(path))
	cfg := configstore.DefaultConfig()
	cfg.Node.Role = "thin"
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "identity", "init", "--name", "legacy")
	if code != 0 {
		t.Fatalf("recoverable init failed: %s", out)
	}
	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.Role != "thin" || after.Node.NodeID != backing.NodeID().String() {
		t.Fatalf("identity init modified legacy role or backing: %+v", after.Node)
	}
}

func TestIdentityInitHandlesRecoverableLegacyMismatchAndBacked(t *testing.T) {
	t.Run("recoverable", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		backing := createIdentity(t, identitySeedPath(path))
		cfg := configstore.DefaultConfig()
		cfg.Revision = 7
		if err := configstore.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "7", "local", "identity", "init", "--name", "recovered")
		if code != 0 {
			t.Fatalf("recover failed: %s", out)
		}
		after, err := configstore.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != 8 || after.Node.NodeID != backing.NodeID().String() {
			t.Fatalf("recover changed backing or revision: %+v", after)
		}
		result := objectField(t, decodeJSON(t, out), "result")
		if result["recovered"] != true || result["created"] != false {
			t.Fatalf("unexpected recovery result: %s", out)
		}
	})

	for _, tc := range []struct {
		name      string
		wantCode  string
		configure func(t *testing.T, path string, cfg *configstore.Config)
	}{
		{
			name:     "legacy unbacked",
			wantCode: "identity.legacy_unbacked",
			configure: func(_ *testing.T, _ string, cfg *configstore.Config) {
				cfg.Node.NodeID = legacyNodeID("legacy")
			},
		},
		{
			name:     "mismatch",
			wantCode: "identity.config_mismatch",
			configure: func(t *testing.T, path string, cfg *configstore.Config) {
				createIdentity(t, identitySeedPath(path))
				other := createIdentity(t, filepath.Join(filepath.Dir(path), "other", "node-seed.v1.json"))
				setPublicIdentity(cfg, other.Public())
			},
		},
		{
			name:     "v2 backing missing",
			wantCode: "identity.backing_missing",
			configure: func(t *testing.T, _ string, cfg *configstore.Config) {
				configured := createIdentity(t, filepath.Join(t.TempDir(), "configured-seed.json"))
				setPublicIdentity(cfg, configured.Public())
			},
		},
		{
			name:     "backed",
			wantCode: "identity.exists",
			configure: func(t *testing.T, path string, cfg *configstore.Config) {
				backing := createIdentity(t, identitySeedPath(path))
				setPublicIdentity(cfg, backing.Public())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			cfg := configstore.DefaultConfig()
			cfg.Revision = 3
			tc.configure(t, path, &cfg)
			if err := configstore.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "3", "local", "identity", "init")
			if code == 0 || jsonField(t, out, "error_code") != tc.wantCode {
				t.Fatalf("init result code=%d want=%s output=%s", code, tc.wantCode, out)
			}
			after, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Revision != 3 {
				t.Fatalf("failed init changed revision to %d", after.Revision)
			}
			if tc.name == "legacy unbacked" || tc.name == "v2 backing missing" {
				if _, err := os.Stat(identitySeedPath(path)); !os.IsNotExist(err) {
					t.Fatalf("rejected init created seed: %v", err)
				}
			}
		})
	}
}

func TestOfflineIdentityInitDryRunCreatesNoBacking(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	code, out := runCLI(t, "--offline", "--config", path, "--json", "--dry-run", "local", "identity", "init", "--name", "preview")
	if code != 0 {
		t.Fatalf("identity dry-run failed: %s", out)
	}
	if _, err := os.Stat(identitySeedPath(path)); !os.IsNotExist(err) {
		t.Fatalf("identity dry-run created backing: %v", err)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 0 || cfg.Node.NodeID != "" {
		t.Fatalf("identity dry-run wrote config: %+v", cfg)
	}
}

func TestOfflineReloadRequiresLiveControl(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "reload")
	if code == 0 || jsonField(t, out, "error_code") != "service.reload_requires_control" {
		t.Fatalf("reload code=%d output=%s", code, out)
	}
}

func TestRestoreLastGoodRequiresLiveControlAndIsMutating(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "config", "restore-last-good")
	if code == 0 || jsonField(t, out, "error_code") != "config.restore_requires_control" {
		t.Fatalf("restore code=%d output=%s", code, out)
	}
	if !CommandMutates([]string{"local", "config", "restore-last-good"}) {
		t.Fatal("restore-last-good was not classified as mutating")
	}
}

func TestErrorOutputRedactsSensitiveDetails(t *testing.T) {
	g := globals{json: true}
	var stdout, stderr bytes.Buffer
	if code := writeCommandError(g, &stdout, &stderr, errors.New("backend token=super-secret-token")); code == 0 {
		t.Fatal("error command returned success")
	}
	out := stdout.String()
	if strings.Contains(out, "super-secret-token") || !strings.Contains(out, "redacted") {
		t.Fatalf("sensitive error output = %s", out)
	}
}

func TestPeerCredentialErrorRemainsActionableInTextOutput(t *testing.T) {
	g := globals{}
	var stdout, stderr bytes.Buffer
	err := commandError{
		configops.CodePeerCredentialQuarantined,
		errors.New("peer B must rotate to a new unique VLESS profile before enabling"),
	}
	if code := writeCommandError(g, &stdout, &stderr, err); code == 0 {
		t.Fatal("credential refusal returned success")
	}
	message := stderr.String()
	if !strings.Contains(message, configops.CodePeerCredentialQuarantined) ||
		!strings.Contains(message, "peer B") || strings.Contains(message, "details were redacted") {
		t.Fatalf("credential refusal was not actionable: %q", message)
	}
}

func TestUncontrolledCredentialErrorsRemainRedacted(t *testing.T) {
	for _, errorCode := range []string{
		"config.peer_credential_quarantine_write",
		"config.peer_credential_quarantine_invalid",
		"config.peer_credential_quarantine_reason_invalid",
	} {
		t.Run(errorCode, func(t *testing.T) {
			g := globals{}
			var stdout, stderr bytes.Buffer
			err := commandError{errorCode, errors.New(`C:\private\token=super-secret-token`)}
			if code := writeCommandError(g, &stdout, &stderr, err); code == 0 {
				t.Fatal("credential refusal returned success")
			}
			message := stderr.String()
			if !strings.Contains(message, errorCode) || !strings.Contains(message, "redacted") ||
				strings.Contains(message, "super-secret-token") {
				t.Fatalf("uncontrolled credential error was not safely rendered: %q", message)
			}
		})
	}
}

func TestPeerAddDoesNotForgeRendrInstanceID(t *testing.T) {
	path := seedConfig(t, configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System, XrayProfiles: runtimeTestProfiles()})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "0", "local", "peer", "add", "B", "--node-id", "node-b", "--addr", "10.20.0.2:19080", "--profile", "vless")
	if code != 0 {
		t.Fatalf("peer add failed: %s", out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Peers) != 1 || cfg.Peers[0].InstanceID != "" {
		t.Fatalf("peer add forged runtime identity: %+v", cfg.Peers)
	}
	if strings.Contains(out, "rendr_instance_id") || strings.Contains(out, "inst-B") {
		t.Fatalf("peer output forged runtime identity: %s", out)
	}
}

func TestPeerAddRequiresProfileForEveryEnabledDirection(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound", "bidirectional"} {
		t.Run(direction, func(t *testing.T) {
			path := seedConfig(t, configstore.Config{
				Node:   node("A"),
				System: configstore.DefaultConfig().System,
			})
			code, out := runDaemonCLI(
				t,
				"--offline", "--config", path, "--json", "--revision", "0",
				"local", "peer", "add", "C", "--node-id", "node-c", "--direction", direction,
			)
			if code == 0 || jsonField(t, out, "error_code") != configops.CodePeerProfileRequired {
				t.Fatalf("profile-less %s peer code=%d output=%s", direction, code, out)
			}
			cfg, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Revision != 0 || len(cfg.Peers) != 0 {
				t.Fatalf("rejected %s peer changed config: revision=%d peers=%+v", direction, cfg.Revision, cfg.Peers)
			}
		})
	}
}

func TestPeerAddPreflightsMissingProfileBeforeOfflineAndRevisionGates(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:   node("A"),
		System: configstore.DefaultConfig().System,
	})
	code, out := runCLI(
		t,
		"--offline", "--config", path, "--json",
		"local", "peer", "add", "C", "--node-id", "node-c", "--direction", "inbound",
	)
	if code == 0 || jsonField(t, out, "error_code") != configops.CodePeerProfileRequired {
		t.Fatalf("missing profile preflight code=%d output=%s", code, out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 0 || len(cfg.Peers) != 0 {
		t.Fatalf("preflight failure changed config: revision=%d peers=%+v", cfg.Revision, cfg.Peers)
	}
}

func TestPeerAddPreflightUsesSharedIdentityCode(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:   node("A"),
		System: configstore.DefaultConfig().System,
	})
	code, out := runCLI(
		t,
		"--offline", "--config", path, "--json",
		"local", "peer", "add", "C", "--profile", "vless",
	)
	if code == 0 || jsonField(t, out, "error_code") != configops.CodePeerIdentityRequired {
		t.Fatalf("missing node ID preflight code=%d output=%s", code, out)
	}
}

func TestPeerAddPreflightWrapsFlagParserErrors(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:   node("A"),
		System: configstore.DefaultConfig().System,
	})
	code, out := runCLI(
		t,
		"--offline", "--config", path, "--json",
		"local", "peer", "add", "C", "--not-a-peer-flag",
	)
	if code == 0 || jsonField(t, out, "error_code") != "cli.flag_invalid" {
		t.Fatalf("invalid flag preflight code=%d output=%s", code, out)
	}
}

func TestPeerEnableRejectsMissingRuntimeProfile(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		Node:   node("A"),
		System: configstore.DefaultConfig().System,
		Peers: []configstore.PeerConfig{{
			Name: "C", NodeID: "node-c", Direction: route.DirectionInbound, Enabled: false,
		}},
	})
	code, out := runDaemonCLI(
		t,
		"--offline", "--config", path, "--json", "--revision", "0",
		"local", "peer", "enable", "C",
	)
	if code == 0 || jsonField(t, out, "error_code") != "config.peer_inbound_profile_incompatible" {
		t.Fatalf("profile-less peer enable code=%d output=%s", code, out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 0 || len(cfg.Peers) != 1 || cfg.Peers[0].Enabled {
		t.Fatalf("rejected enable changed config: revision=%d peers=%+v", cfg.Revision, cfg.Peers)
	}
}

func TestCLIRepairsQuarantinedPeerWithoutLosingDurableRevocation(t *testing.T) {
	cfg := quarantinedPeerCLIConfig(t)
	path := seedConfig(t, cfg)

	code, out := runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "0",
		"local", "peer", "disable", "B", "--reason", "maintenance",
	)
	if code != 0 {
		t.Fatalf("disable quarantined peer failed: %s", out)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || !configstore.IsPeerCredentialQuarantined(loaded.Peers[0]) {
		t.Fatalf("disable cleared CLI quarantine: revision=%d peer=%+v", loaded.Revision, loaded.Peers[0])
	}

	code, out = runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "1",
		"local", "peer", "set", "B", "--profile", "fresh",
	)
	if code != 0 {
		t.Fatalf("profile rotation failed: %s", out)
	}
	code, out = runDaemonCLI(t,
		"--offline", "--config", path, "--json", "--revision", "2",
		"local", "peer", "enable", "B",
	)
	if code != 0 {
		t.Fatalf("enable after rotation failed: %s", out)
	}

	loaded, err = configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 3 || !loaded.Peers[0].Enabled || loaded.Peers[0].XrayProfileID != "fresh" || loaded.Peers[0].DisabledCause != "" {
		t.Fatalf("CLI repair result = revision:%d peer:%+v", loaded.Revision, loaded.Peers[0])
	}
	if len(loaded.PeerCredentialQuarantines) != 1 {
		t.Fatalf("CLI repair removed durable revocation: %+v", loaded.PeerCredentialQuarantines)
	}
}

func quarantinedPeerCLIConfig(t *testing.T) configstore.Config {
	t.Helper()
	cfg := configstore.DefaultConfig()
	cfg.Node = node("A")
	cfg.XrayProfiles = runtimeTestProfiles()
	fingerprint, err := xraycredential.VLESSFingerprint(cfg.XrayProfiles["vless"].VLESS.UUID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Peers = []configstore.PeerConfig{{
		Name: "B", NodeID: "node-b", Direction: route.DirectionInbound,
		XrayProfileID: "vless", Enabled: false, DisabledCause: configstore.PeerCredentialQuarantineCause,
	}}
	cfg.XrayProfiles["fresh"] = configstore.XrayProfile{
		ID: "fresh", Kind: "vless", VLESS: &configstore.VLESSProfile{
			UUID: "16f5cc3e-8186-4751-b6cd-45cc70d4b4fe", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
		},
	}
	cfg.PeerCredentialQuarantines = []configstore.PeerCredentialQuarantine{{
		CredentialFingerprint: fingerprint,
		PeerNodeIDs:           []string{"node-b", "retired-node"},
		Reason:                configstore.PeerCredentialCollisionReason,
	}}
	return cfg
}

func runtimeTestProfiles() map[string]configstore.XrayProfile {
	return map[string]configstore.XrayProfile{
		"vless": {
			ID: "vless", Kind: "vless", VLESS: &configstore.VLESSProfile{
				UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			},
		},
		"vless-in": {
			ID: "vless-in", Kind: "vless", VLESS: &configstore.VLESSProfile{
				UUID: "f3c9805c-12ea-48f0-b762-5739f2365620", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			},
		},
	}
}

func TestStatusDoesNotReportPersistedRuntimeAsLive(t *testing.T) {
	cfg := configstore.Config{Node: node("A"), System: configstore.DefaultConfig().System}
	cfg.Node.RendrInstanceID = "persisted-runtime-id"
	path := seedConfig(t, cfg)
	code, out := runCLI(t, "--offline", "--config", path, "--json", "local", "status")
	if code != 0 {
		t.Fatalf("status failed: %s", out)
	}
	if strings.Contains(out, "persisted-runtime-id") || strings.Contains(out, "rendr_instance_id") {
		t.Fatalf("status presented config as runtime: %s", out)
	}
	got := decodeJSON(t, out)
	if got["status_source"] != "config_only" {
		t.Fatalf("status source=%v output=%s", got["status_source"], out)
	}
	runtimeStatus := objectField(t, got, "runtime")
	if runtimeStatus["available"] != false || runtimeStatus["source"] != "config_only" {
		t.Fatalf("unexpected runtime status: %s", out)
	}
}

func TestXrayProfilesOutputRedactsStoredCredentials(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.XrayProfiles = map[string]configstore.XrayProfile{
		"private-link": {
			ID:   "private-link",
			Kind: "vless",
			VLESS: &configstore.VLESSProfile{
				UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			},
		},
		"terminal": {
			ID:    "terminal",
			Kind:  "socks",
			SOCKS: &configstore.SOCKSProfile{Username: "operator", Password: "entry-secret-value"},
		},
	}
	path := seedConfig(t, cfg)
	code, out := runCLI(t, "--offline", "--config", path, "--json", "local", "xray", "profiles")
	if code != 0 {
		t.Fatalf("profiles failed: %s", out)
	}
	for _, forbidden := range []string{"66ad4540-b58c-4ad2-9926-ea63445a9b57", "entry-secret-value"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("profiles output exposed credential %q: %s", forbidden, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") || !strings.Contains(out, "operator") {
		t.Fatalf("profiles output did not preserve its safe shape: %s", out)
	}
	decoded := decodeJSON(t, out)
	profiles := objectField(t, decoded, "xray_profiles")
	privateLink := objectField(t, profiles, "private-link")
	if privateLink["id"] != "private-link" || privateLink["kind"] != "vless" {
		t.Fatalf("user-defined profile key corrupted response shape: %s", out)
	}
	vless := objectField(t, privateLink, "vless")
	if vless["uuid"] != "[REDACTED]" {
		t.Fatalf("VLESS credential was not redacted in place: %s", out)
	}
}

func TestMutationRevisionCASAllowsExactlyOneWrite(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 5, Node: node("A"), System: configstore.DefaultConfig().System})
	args := []string{"--offline", "--config", path, "--json", "--revision", "5", "local", "identity", "rename"}
	code, out := runDaemonCLI(t, append(args, "first")...)
	if code != 0 {
		t.Fatalf("first CAS failed: %s", out)
	}
	code, out = runDaemonCLI(t, append(args, "second")...)
	if code == 0 || jsonField(t, out, "error_code") != "config.revision_conflict" {
		t.Fatalf("stale CAS code=%d output=%s", code, out)
	}
	cfg, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 6 || cfg.Node.DisplayName != "first" {
		t.Fatalf("CAS wrote unexpected config: revision=%d name=%s", cfg.Revision, cfg.Node.DisplayName)
	}
}

func TestStaleIdentityInitDoesNotCreateBacking(t *testing.T) {
	path := seedConfig(t, configstore.Config{Revision: 4, System: configstore.DefaultConfig().System})
	code, out := runDaemonCLI(t, "--offline", "--config", path, "--json", "--revision", "3", "local", "identity", "init")
	if code == 0 || jsonField(t, out, "error_code") != "config.revision_conflict" {
		t.Fatalf("stale init code=%d output=%s", code, out)
	}
	if _, err := os.Stat(identitySeedPath(path)); !os.IsNotExist(err) {
		t.Fatalf("stale init created backing: %v", err)
	}
}

func TestOutputRedactsSensitiveMaterial(t *testing.T) {
	path := seedConfig(t, configstore.Config{
		System: configstore.DefaultConfig().System,
		XrayProfiles: map[string]configstore.XrayProfile{
			"sensitive": {
				ID:   "sensitive",
				Kind: "test",
				Options: map[string]string{
					"private_key": "private-value",
					"nodeSeed":    "seed-value",
					"password":    "password-value",
					"public_key":  "public-value",
				},
			},
		},
	})
	for _, jsonMode := range []bool{true, false} {
		args := []string{"--offline", "--config", path}
		if jsonMode {
			args = append(args, "--json")
		}
		code, out := runCLI(t, append(args, "local", "xray", "profiles")...)
		if code != 0 {
			t.Fatalf("profiles failed: %s", out)
		}
		for _, forbidden := range []string{"private-value", "seed-value", "password-value"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("output leaked %q: %s", forbidden, out)
			}
		}
		if strings.Contains(out, "public-value") || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("unexpected redacted output: %s", out)
		}
	}
}

func TestReadCredentialFileRequiresProtectedRegularFile(t *testing.T) {
	protected := filepath.Join(t.TempDir(), "protected.credential")
	want, err := controlapi.CreateToken(protected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readCredentialFile(protected)
	if err != nil {
		t.Fatalf("read protected credential: %v", err)
	}
	if got != want {
		t.Fatalf("credential = %q, want generated value", got)
	}

	insecure := filepath.Join(t.TempDir(), "insecure.credential")
	if err := os.WriteFile(insecure, []byte("not-protected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(insecure); err == nil {
		t.Fatal("world/inherited-readable credential file was accepted")
	}

	symlink := filepath.Join(t.TempDir(), "credential-link")
	if err := os.Symlink(protected, symlink); err == nil {
		if _, err := readCredentialFile(symlink); err == nil {
			t.Fatal("credential symlink was accepted")
		}
	}
}

func TestXrayProfileAddRejectsUnavailableOptions(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"vless reality", []string{"--kind", "vless", "--server-name", "example.test"}},
		{"socks transport", []string{"--kind", "socks", "--transport", "tcp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--offline", "--config", path, "--json", "--revision", "0", "local", "xray", "profile", "add", "test-profile"}
			code, out := runDaemonCLI(t, append(args, tc.args...)...)
			if code == 0 || jsonField(t, out, "error_code") != "config.profile_option_unavailable" {
				t.Fatalf("code=%d output=%s", code, out)
			}
		})
	}
}

func TestWebCredentialShowIsLocalOnlyAndExplicit(t *testing.T) {
	path := seedConfig(t, configstore.DefaultConfig())
	credential := createCLIStoreToken(t, path, statestore.WebToken)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--config", path, "--json", "web", "credential", "show"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	response := decodeJSON(t, stdout.String())
	if response["credential"] != credential || response["username"] != webbridge.BasicUsername {
		t.Fatalf("unexpected credential response: %#v", response)
	}

	stdout.Reset()
	stderr.Reset()
	if code := RunDaemon([]string{"--config", path, "--json", "web", "credential", "show"}, &stdout, &stderr); code == 0 {
		t.Fatalf("daemon exposed Web credential: stdout=%s", stdout.String())
	}
	if jsonField(t, stdout.String(), "error_code") != "web.credential_local_only" {
		t.Fatalf("unexpected daemon rejection: %s", stdout.String())
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

func runDaemonCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunDaemon(args, &stdout, &stderr)
	if stdout.Len() > 0 {
		return code, stdout.String()
	}
	return code, stderr.String()
}

func runStoreDaemonCLI(t *testing.T, store *statestore.Store, args ...string) (int, string) {
	t.Helper()
	full := []string{"--offline", "--config", store.ConfigPath()}
	full = append(full, args...)
	var stdout, stderr bytes.Buffer
	result := RunOwnedDaemonStoreContext(
		context.Background(), full, store.ConfigPath(), store, &stdout, &stderr,
	)
	if stdout.Len() > 0 {
		return result.ExitCode, stdout.String()
	}
	return result.ExitCode, stderr.String()
}

func openCLIStore(t *testing.T, configPath string) *statestore.Store {
	t.Helper()
	canonical, err := configstore.CanonicalPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createCLIStoreToken(t *testing.T, configPath string, object statestore.Object) string {
	t.Helper()
	store := openCLIStore(t, configPath)
	token, err := controlapi.CreateStoreToken(store, object)
	if err != nil {
		t.Fatal(err)
	}
	return token
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
	return configstore.NodeConfig{NodeID: legacyNodeID(id), DisplayName: id, RendrCapable: true, RendrInstanceID: "inst-" + id}
}

func legacyNodeID(label string) string {
	digest := sha256.Sum256([]byte(label))
	return identity.LegacyNodeIDPrefix + hex.EncodeToString(digest[:16])
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

func createIdentity(t *testing.T, path string) *identity.Identity {
	t.Helper()
	created, err := identity.Create(path)
	if err != nil {
		t.Fatalf("create identity at %s: %v", path, err)
	}
	return created
}

func setPublicIdentity(cfg *configstore.Config, public identity.PublicIdentity) {
	cfg.Node.NodeID = public.NodeID.String()
	cfg.Node.PublicKey = public.PublicKey
}

func decodeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatalf("json decode %q: %v", raw, err)
	}
	return object
}

func objectField(t *testing.T, object map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := object[name].(map[string]any)
	if !ok {
		t.Fatalf("field %s is not an object: %#v", name, object[name])
	}
	return value
}

func assertNoSensitiveKeys(t *testing.T, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			compact := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
			for _, marker := range []string{"seed", "private", "secret", "password", "passphrase"} {
				if strings.Contains(compact, marker) {
					t.Fatalf("output contains sensitive field %q", key)
				}
			}
			assertNoSensitiveKeys(t, child)
		}
	case []any:
		for _, child := range value {
			assertNoSensitiveKeys(t, child)
		}
	}
}

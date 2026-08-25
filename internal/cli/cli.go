package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/localview"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/settings"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/webbridge"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
)

type globals struct {
	configPath   string
	control      string
	offline      bool
	json         bool
	dryRun       bool
	revision     int64
	daemon       bool
	ownershipKey string
	stateStore   *statestore.Store
	ctx          context.Context
}

type ExecutionResult struct {
	ExitCode int
	Applied  bool
	Outcome  controlapi.MutationOutcome
}

type commandError struct {
	code string
	err  error
}

type appliedCommandError struct {
	err error
}

func (e appliedCommandError) Error() string { return e.err.Error() }
func (e appliedCommandError) Unwrap() error { return e.err }

func loadConfig(g globals) (configstore.Config, error) {
	if g.stateStore != nil {
		if g.daemon {
			return configstore.LoadStoreExisting(g.stateStore)
		}
		return configstore.LoadStore(g.stateStore)
	}
	if g.daemon {
		return configstore.LoadExisting(g.configPath)
	}
	return configstore.Load(g.configPath)
}

func (e commandError) Error() string {
	if e.err == nil {
		return e.code
	}
	return e.code + ": " + e.err.Error()
}

func Run(args []string, stdout, stderr io.Writer) int {
	return run(context.Background(), args, stdout, stderr, false, "", nil).ExitCode
}

// RunDaemon executes a command inside the long-running daemon. It is kept
// separate from Run so an offline CLI can never become a second config writer.
func RunDaemon(args []string, stdout, stderr io.Writer) int {
	return RunDaemonContext(context.Background(), args, stdout, stderr).ExitCode
}

func RunDaemonContext(ctx context.Context, args []string, stdout, stderr io.Writer) ExecutionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	return run(ctx, args, stdout, stderr, true, "", nil)
}

// RunOwnedDaemon executes inside xtierd against its pinned config path. The
// canonical key names the lifetime config ownership already held by xtierd.
func RunOwnedDaemon(args []string, ownershipKey string, stdout, stderr io.Writer) int {
	return RunOwnedDaemonContext(context.Background(), args, ownershipKey, stdout, stderr).ExitCode
}

func RunOwnedDaemonContext(ctx context.Context, args []string, ownershipKey string, stdout, stderr io.Writer) ExecutionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	return run(ctx, args, stdout, stderr, true, ownershipKey, nil)
}

// RunOwnedDaemonStoreContext executes a daemon command against the state store
// pinned by the daemon's lifetime lease. It is the production daemon path;
// RunOwnedDaemonContext remains for legacy compatibility tests.
func RunOwnedDaemonStoreContext(ctx context.Context, args []string, ownershipKey string, store *statestore.Store, stdout, stderr io.Writer) ExecutionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return ExecutionResult{ExitCode: writeCommandError(globals{}, stdout, stderr, commandError{
			"config.store_required", fmt.Errorf("daemon state store is required"),
		})}
	}
	return run(ctx, args, stdout, stderr, true, ownershipKey, store)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, daemon bool, ownershipKey string, store *statestore.Store) ExecutionResult {
	g, rest, err := parseGlobals(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExecutionResult{ExitCode: 2}
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "command is required")
		return ExecutionResult{ExitCode: 2}
	}
	g.daemon = daemon
	g.ownershipKey = ownershipKey
	g.stateStore = store
	g.ctx = ctx
	if store != nil {
		if !daemon || filepath.Clean(g.configPath) != store.ConfigPath() {
			return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, commandError{
				"config.store_mismatch", fmt.Errorf("daemon config does not match its state store"),
			})}
		}
		g.configPath = store.ConfigPath()
	}
	if !daemon {
		canonical, canonicalErr := configstore.CanonicalPath(g.configPath)
		if canonicalErr != nil {
			return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, canonicalErr)}
		}
		g.configPath = canonical
		if g.offline {
			offlineStore, openErr := statestore.Open(g.configPath)
			if openErr != nil {
				return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, commandError{
					"config.store_open", openErr,
				})}
			}
			defer offlineStore.Close()
			g.stateStore = offlineStore
		}
	}
	if isWebCredentialShow(rest) {
		if daemon {
			return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, commandError{
				"web.credential_local_only",
				fmt.Errorf("the Web credential can only be read by a local xtierctl process"),
			})}
		}
		return ExecutionResult{ExitCode: runWebCredentialShow(g, stdout, stderr)}
	}
	if isDaemonStatus(rest) {
		if daemon || g.offline {
			return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, commandError{
				"daemon.status_requires_control",
				fmt.Errorf("daemon status requires the live control API"),
			})}
		}
		return ExecutionResult{ExitCode: runDaemonStatus(g, stdout, stderr)}
	}
	if !daemon && !g.offline {
		return ExecutionResult{ExitCode: runViaDaemon(g, rest, stdout, stderr)}
	}
	if !daemon && commandMutates(rest) && !g.dryRun {
		return ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, commandError{
			"cli.offline_read_only",
			fmt.Errorf("configuration changes must be executed by xtierd"),
		})}
	}
	mutates := daemon && commandMutates(rest) && !g.dryRun
	if mutates && g.revision < 0 {
		return ExecutionResult{
			ExitCode: writeCommandError(g, stdout, stderr, commandError{
				"config.revision_required",
				fmt.Errorf("mutating daemon commands require an expected revision"),
			}),
			Outcome: controlapi.MutationOutcomeNotApplied,
		}
	}
	if mutates {
		if err := ctx.Err(); err != nil {
			return ExecutionResult{
				ExitCode: writeCommandError(g, stdout, stderr, publicerr.Wrap("control.mutation_timeout", err)),
				Outcome:  controlapi.MutationOutcomeNotApplied,
			}
		}
	}
	if err := dispatch(g, rest, stdout); err != nil {
		result := ExecutionResult{ExitCode: writeCommandError(g, stdout, stderr, err)}
		if mutates {
			result.Applied, result.Outcome = mutationErrorOutcome(err)
		}
		return result
	}
	result := ExecutionResult{}
	if mutates {
		result.Applied = true
		result.Outcome = controlapi.MutationOutcomeApplied
	}
	return result
}

func isWebCredentialShow(args []string) bool {
	return len(args) == 3 && args[0] == "web" && args[1] == "credential" && args[2] == "show"
}

func runWebCredentialShow(g globals, stdout, stderr io.Writer) int {
	var credential string
	var err error
	if g.stateStore != nil {
		credential, err = controlapi.ReadStoreToken(g.stateStore, statestore.WebToken)
	} else {
		credential, err = readConfigStoreToken(g.configPath, statestore.WebToken)
	}
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{
			"web.credential_unavailable",
			fmt.Errorf("Web credential is unavailable"),
		})
	}
	if g.json {
		err = json.NewEncoder(stdout).Encode(map[string]any{
			"ok": true, "username": webbridge.BasicUsername,
			"credential": credential, "credential_source": "private_state",
		})
	} else {
		_, err = fmt.Fprintf(stdout, "username=%s\ncredential=%s\n", webbridge.BasicUsername, credential)
	}
	if err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	return 0
}

func parseGlobals(args []string) (globals, []string, error) {
	fs := flag.NewFlagSet("xtierctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g := globals{configPath: os.Getenv("XTIER_CONFIG"), control: os.Getenv("XTIER_CONTROL_ADDR"), revision: -1}
	if g.configPath == "" {
		g.configPath = "xtier.json"
	}
	if g.control == "" {
		g.control = controlapi.DefaultAddr
	}
	fs.StringVar(&g.configPath, "config", g.configPath, "config path")
	fs.StringVar(&g.control, "control", g.control, "local daemon control address")
	fs.BoolVar(&g.offline, "offline", false, "execute against the config file without contacting the daemon")
	fs.BoolVar(&g.json, "json", false, "JSON output")
	fs.BoolVar(&g.dryRun, "dry-run", false, "do not write changes")
	fs.Int64Var(&g.revision, "revision", -1, "expected config revision")
	if err := fs.Parse(args); err != nil {
		return globals{}, nil, err
	}
	return g, fs.Args(), nil
}

func runViaDaemon(g globals, args []string, stdout, stderr io.Writer) int {
	requestID, err := controlapi.NewRequestID()
	if err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	token, err := readConfigStoreToken(g.configPath, statestore.ControlToken)
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{"control.token_unavailable", err})
	}
	resp, err := controlapi.ExecuteToken(g.control, token, controlapi.Request{
		Args:      args,
		JSON:      g.json,
		DryRun:    g.dryRun,
		Revision:  g.revision,
		RequestID: requestID,
	})
	mutates := commandMutates(args) && !g.dryRun
	if err != nil {
		if mutates {
			if controlapi.CommandMayHaveApplied(err) {
				return writeIndeterminateMutation(g, stdout, stderr, requestID, err)
			}
			return writeRejectedMutation(g, stdout, stderr, requestID, err)
		}
		message := sanitizeErrorMessage(err.Error())
		if g.json {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": false, "error_code": errorCode(err), "message": message})
		} else {
			fmt.Fprintln(stderr, message)
		}
		return 1
	}
	if mutates {
		if resp.Outcome == "" {
			return writeIndeterminateMutation(
				g,
				stdout,
				stderr,
				requestID,
				errors.New("daemon mutation response did not include an outcome"),
			)
		}
		return writeDaemonMutationResponse(g, stdout, stderr, requestID, resp)
	}
	if resp.Stdout != "" {
		_, _ = io.WriteString(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = io.WriteString(stderr, resp.Stderr)
	}
	return resp.ExitCode
}

func writeDaemonMutationResponse(g globals, stdout, stderr io.Writer, requestID string, resp controlapi.Response) int {
	exitCode := resp.ExitCode
	switch resp.Outcome {
	case controlapi.MutationOutcomeApplied:
		exitCode = 0
	case controlapi.MutationOutcomeIndeterminate:
		exitCode = controlapi.IndeterminateExitCode
	}
	if g.json {
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(resp.Stdout), &payload); err != nil || payload == nil {
			payload = map[string]any{
				"ok":         false,
				"error_code": "control.command_response_invalid",
				"message":    "daemon mutation response payload was invalid",
			}
			if exitCode == 0 && resp.Outcome != controlapi.MutationOutcomeApplied {
				exitCode = 1
			}
		}
		payload["applied"] = resp.Applied
		payload["outcome"] = resp.Outcome
		payload["request_id"] = requestID
		_ = json.NewEncoder(stdout).Encode(payload)
		return exitCode
	}
	if resp.Stdout != "" {
		_, _ = io.WriteString(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = io.WriteString(stderr, resp.Stderr)
	}
	_, _ = fmt.Fprintf(stderr, "applied=%t outcome=%s request_id=%s\n", resp.Applied, resp.Outcome, requestID)
	return exitCode
}

func writeIndeterminateMutation(g globals, stdout, stderr io.Writer, requestID string, err error) int {
	message := sanitizeErrorMessage(err.Error())
	if g.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"ok":         false,
			"error_code": "control.mutation_outcome_indeterminate",
			"message":    message,
			"applied":    false,
			"outcome":    controlapi.MutationOutcomeIndeterminate,
			"request_id": requestID,
		})
	} else {
		fmt.Fprintf(
			stderr,
			"%s: outcome=%s request_id=%s\n",
			message,
			controlapi.MutationOutcomeIndeterminate,
			requestID,
		)
	}
	return controlapi.IndeterminateExitCode
}

func writeRejectedMutation(g globals, stdout, stderr io.Writer, requestID string, err error) int {
	message := sanitizeErrorMessage(err.Error())
	if g.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"ok":         false,
			"error_code": "control.mutation_rejected",
			"message":    message,
			"applied":    false,
			"outcome":    controlapi.MutationOutcomeNotApplied,
			"request_id": requestID,
		})
	} else {
		fmt.Fprintf(
			stderr,
			"%s: applied=false outcome=%s request_id=%s\n",
			message,
			controlapi.MutationOutcomeNotApplied,
			requestID,
		)
	}
	return 1
}

func runDaemonStatus(g globals, stdout, stderr io.Writer) int {
	token, err := readConfigStoreToken(g.configPath, statestore.ControlToken)
	if err != nil {
		return writeCommandError(g, stdout, stderr, commandError{"control.token_unavailable", err})
	}
	status, err := controlapi.GetStatusToken(g.control, token)
	if err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	if err := writeOutput(g, stdout, map[string]any{"ok": true, "daemon": status}); err != nil {
		return writeCommandError(g, stdout, stderr, err)
	}
	return 0
}

func readConfigStoreToken(configPath string, object statestore.Object) (token string, err error) {
	store, err := statestore.Open(configPath)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	return controlapi.ReadStoreToken(store, object)
}

func writeCommandError(g globals, stdout, stderr io.Writer, err error) int {
	message := sanitizeErrorMessage(err.Error())
	if g.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": false, "error_code": errorCode(err), "message": message})
	} else {
		fmt.Fprintln(stderr, message)
	}
	return 1
}

func sanitizeErrorMessage(message string) string {
	lower := strings.ToLower(message)
	for _, marker := range []string{"seed", "private", "secret", "password", "passphrase", "token", "credential", "cookie", "psk"} {
		if strings.Contains(lower, marker) {
			return "sensitive error details were redacted"
		}
	}
	return message
}

func dispatch(g globals, args []string, out io.Writer) error {
	switch args[0] {
	case "local":
		return dispatchLocal(g, args[1:], out)
	case "path":
		return dispatchPath(g, args[1:], out)
	case "config":
		if len(args) > 1 && args[1] == "validate" {
			cfg, err := loadConfig(g)
			if err != nil {
				return err
			}
			return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision})
		}
	}
	return commandError{"cli.unknown_command", fmt.Errorf("%s", strings.Join(args, " "))}
}

func dispatchLocal(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("local subcommand is required")}
	}
	switch args[0] {
	case "status":
		return localStatus(g, out)
	case "identity":
		return localIdentity(g, args[1:], out)
	case "settings":
		return localSettings(g, args[1:], out)
	case "config":
		return localConfig(args[1:])
	case "inbound":
		return localInbound(g, args[1:], out)
	case "peers":
		return localPeers(g, out)
	case "peer":
		return localPeer(g, args[1:], out)
	case "xray":
		return localXray(g, args[1:], out)
	case "topology":
		return localTopology(g, args[1:], out)
	case "reload":
		return commandError{"service.reload_requires_control", fmt.Errorf("runtime reconciliation requires the live daemon control API")}
	}
	return commandError{"cli.unknown_command", fmt.Errorf("local %s", strings.Join(args, " "))}
}

func localConfig(args []string) error {
	if len(args) == 1 && args[0] == "restore-last-good" {
		return commandError{
			"config.restore_requires_control",
			fmt.Errorf("last-known-good restore requires the live daemon control API"),
		}
	}
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("config subcommand is required")}
	}
	return commandError{"cli.unknown_command", fmt.Errorf("config %s", strings.Join(args, " "))}
}

func localStatus(g globals, out io.Writer) error {
	cfg, err := loadConfig(g)
	if err != nil {
		return err
	}
	topo := localview.TopologyFromConfig(cfg)
	relations := route.PeerRelations(topo)
	local := relations[route.NodeID(cfg.Node.NodeID)]
	identityStatus, err := identityStateForGlobals(g, cfg)
	if err != nil {
		return err
	}
	return writeOutput(g, out, map[string]any{
		"ok":            true,
		"revision":      cfg.Revision,
		"status_source": "config_only",
		"identity":      identityStatus,
		"node":          configNodeView(cfg.Node),
		"display_name":  cfg.Node.DisplayName,
		"settings":      cfg.System,
		"runtime": map[string]any{
			"available": false,
			"source":    "config_only",
		},
		"peer_counts": map[string]int{
			"inbound":       len(local.Inbound),
			"outbound":      len(local.Outbound),
			"bidirectional": len(local.Bidirectional),
		},
		"inbounds": cfg.NodeInbound,
	})
}

func localIdentity(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("identity subcommand is required")}
	}
	switch args[0] {
	case "show":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		state, err := identityStateForGlobals(g, cfg)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{
			"ok":       true,
			"revision": cfg.Revision,
			"identity": state,
			"node":     configNodeView(cfg.Node),
		})
	case "init":
		fs := flag.NewFlagSet("identity init", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "display name")
		if flagPresent(args[1:], "role") {
			return commandError{"identity.role_removed", fmt.Errorf("--role is no longer supported; node role is legacy read-only metadata")}
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			observed, backing, err := inspectIdentityForGlobals(g, *cfg)
			if err != nil {
				return nil, err
			}

			id := backing
			created := false
			switch observed.State {
			case identityStateUninitialized:
				if g.dryRun {
					proposed := configNodeView(cfg.Node)
					proposed.DisplayName = *name
					proposed.RendrCapable = true
					return map[string]any{
						"identity":             observed,
						"node":                 proposed,
						"would_create_backing": true,
					}, nil
				}
				if g.stateStore != nil {
					id, err = identity.CreateStore(g.stateStore)
				} else {
					id, err = identity.Create(identitySeedPath(g.configPath))
				}
				created = err == nil
			case identityStateRecoverable:
				// A previous attempt may have persisted the seed before its config CAS
				// completed. Reusing it preserves the node identity on retry.
			case identityStateBacked:
				return nil, commandError{"identity.exists", fmt.Errorf("node identity is already initialized and backed")}
			case identityStateLegacyUnbacked:
				return nil, commandError{"identity.legacy_unbacked", fmt.Errorf("configured identity has no cryptographic backing")}
			case identityStateBackingMissing:
				return nil, commandError{"identity.backing_missing", fmt.Errorf("configured v2 identity is missing its seed backing")}
			case identityStateMismatch:
				return nil, commandError{"identity.config_mismatch", fmt.Errorf("configured identity does not match cryptographic backing")}
			}
			if err != nil {
				return nil, err
			}
			public := id.Public()
			cfg.Node.NodeID = public.NodeID.String()
			cfg.Node.PublicKey = public.PublicKey
			if *name != "" {
				cfg.Node.DisplayName = *name
			} else if cfg.Node.DisplayName == "" {
				cfg.Node.DisplayName = public.NodeID.String()
			}
			cfg.Node.RendrCapable = true
			return map[string]any{
				"identity":  identityViewFromPublic(identityStateBacked, public),
				"node":      configNodeView(cfg.Node),
				"created":   created,
				"recovered": !created,
			}, nil
		})
	case "rename":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("display name is required")}
		}
		name := args[1]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			cfg.Node.DisplayName = name
			return map[string]any{"node": configNodeView(cfg.Node)}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("identity %s", strings.Join(args, " "))}
}

func identitySeedPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "keystore", "node-seed.v1.json")
}

const (
	identityStateUninitialized  = "uninitialized"
	identityStateBacked         = "backed"
	identityStateRecoverable    = "recoverable"
	identityStateLegacyUnbacked = "legacy_unbacked"
	identityStateBackingMissing = "backing_missing"
	identityStateMismatch       = "mismatch"
)

type identityView struct {
	State                 string `json:"state"`
	Version               int    `json:"version,omitempty"`
	Algorithm             string `json:"algorithm,omitempty"`
	NodeID                string `json:"node_id,omitempty"`
	PublicKey             string `json:"public_key,omitempty"`
	BackingNodeID         string `json:"backing_node_id,omitempty"`
	BackingPublicKey      string `json:"backing_public_key,omitempty"`
	OSACLReleaseQualified bool   `json:"os_acl_release_qualified"`
}

func identityState(cfg configstore.Config, seedPath string) (identityView, error) {
	state, _, err := inspectIdentity(cfg, seedPath)
	return state, err
}

func identityStateForGlobals(g globals, cfg configstore.Config) (identityView, error) {
	state, _, err := inspectIdentityForGlobals(g, cfg)
	return state, err
}

func inspectIdentityForGlobals(g globals, cfg configstore.Config) (identityView, *identity.Identity, error) {
	if g.stateStore != nil {
		return inspectIdentityWithLoader(cfg, func() (*identity.Identity, error) {
			return identity.LoadStore(g.stateStore)
		})
	}
	return inspectIdentity(cfg, identitySeedPath(g.configPath))
}

func inspectIdentity(cfg configstore.Config, seedPath string) (identityView, *identity.Identity, error) {
	return inspectIdentityWithLoader(cfg, func() (*identity.Identity, error) {
		return identity.Load(seedPath)
	})
}

func inspectIdentityWithLoader(cfg configstore.Config, load func() (*identity.Identity, error)) (identityView, *identity.Identity, error) {
	id, err := load()
	if errors.Is(err, os.ErrNotExist) {
		configured, classifyErr := identity.ClassifyConfiguredIdentity(cfg.Node.NodeID, cfg.Node.PublicKey)
		if classifyErr != nil {
			return identityView{}, nil, classifyErr
		}
		state := identityStateUninitialized
		switch configured {
		case identity.ConfiguredIdentityLegacy:
			state = identityStateLegacyUnbacked
		case identity.ConfiguredIdentityV2:
			state = identityStateBackingMissing
		}
		return identityView{
			State:     state,
			NodeID:    cfg.Node.NodeID,
			PublicKey: cfg.Node.PublicKey,
		}, nil, nil
	}
	if err != nil {
		return identityView{}, nil, err
	}
	public := id.Public()
	state := identityStateBacked
	if cfg.Node.NodeID == "" && cfg.Node.PublicKey == "" {
		state = identityStateRecoverable
	} else if cfg.Node.NodeID != public.NodeID.String() || cfg.Node.PublicKey != public.PublicKey {
		return identityView{
			State:                 identityStateMismatch,
			NodeID:                cfg.Node.NodeID,
			PublicKey:             cfg.Node.PublicKey,
			BackingNodeID:         public.NodeID.String(),
			BackingPublicKey:      public.PublicKey,
			OSACLReleaseQualified: true,
		}, id, nil
	}
	return identityViewFromPublic(state, public), id, nil
}

func identityViewFromPublic(state string, public identity.PublicIdentity) identityView {
	return identityView{
		State:                 state,
		Version:               public.Version,
		Algorithm:             public.Algorithm,
		NodeID:                public.NodeID.String(),
		PublicKey:             public.PublicKey,
		OSACLReleaseQualified: true,
	}
}

func configNodeView(node configstore.NodeConfig) configstore.NodeConfig {
	// rendr_instance_id is runtime observation, never config-backed CLI status.
	node.RendrInstanceID = ""
	return node
}

func localSettings(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("settings subcommand is required")}
	}
	switch args[0] {
	case "show":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "settings": cfg.System})
	case "validate":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		if err := settings.Validate(cfg.System); err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision})
	case "set":
		fs := flag.NewFlagSet("settings set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		logLevel := fs.String("log-level", "", "")
		maxNestedDepth := fs.Int("max-nested-depth", 0, "")
		maxResponseNodes := fs.Int("max-response-nodes", 0, "")
		maxResponseBytes := fs.Int("max-response-bytes", 0, "")
		maxCacheEntries := fs.Int("max-cache-entries", 0, "")
		maxFetchFanOut := fs.Int("max-fetch-fan-out", 0, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		if len(visited) == 0 {
			return commandError{"settings.patch_empty", fmt.Errorf("at least one setting is required")}
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			if visited["log-level"] {
				cfg.System.LogLevel = *logLevel
			}
			if visited["max-nested-depth"] {
				cfg.System.MaxNestedDepth = *maxNestedDepth
			}
			if visited["max-response-nodes"] {
				cfg.System.MaxResponseNodes = *maxResponseNodes
			}
			if visited["max-response-bytes"] {
				cfg.System.MaxResponseBytes = *maxResponseBytes
			}
			if visited["max-cache-entries"] {
				cfg.System.MaxCacheEntries = *maxCacheEntries
			}
			if visited["max-fetch-fan-out"] {
				cfg.System.MaxFetchFanOut = *maxFetchFanOut
			}
			return map[string]any{"settings": cfg.System}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("settings %s", strings.Join(args, " "))}
}

func localInbound(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("inbound subcommand is required")}
	}
	switch args[0] {
	case "list":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "inbounds": cfg.NodeInbound})
	case "set":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("inbound kind is required")}
		}
		kind := args[1]
		fs := flag.NewFlagSet("inbound set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		listen := fs.String("listen", "", "")
		profile := fs.String("profile", "", "")
		purpose := fs.String("purpose", "", "")
		exitPeer := fs.String("exit-peer", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		if kind == "node-vless" && visited["profile"] {
			return commandError{"config.inbound_profile_forbidden", fmt.Errorf("node-vless credentials are configured on inbound peers")}
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			i := inboundIndex(cfg.NodeInbound, kind)
			in := configstore.InboundConfig{Kind: kind, Enabled: true}
			switch kind {
			case "socks":
				in.Purpose = "user"
			case "node-vless":
				in.Purpose = "node"
			}
			if i >= 0 {
				in = cfg.NodeInbound[i]
			}
			if kind == "node-vless" {
				in.XrayProfileID = ""
			}
			if visited["listen"] {
				in.Listen = *listen
			}
			if visited["profile"] {
				in.XrayProfileID = *profile
			}
			if visited["purpose"] {
				in.Purpose = *purpose
			}
			if visited["exit-peer"] {
				in.ExitPeer = *exitPeer
			}
			if i >= 0 {
				cfg.NodeInbound[i] = in
			} else {
				cfg.NodeInbound = append(cfg.NodeInbound, in)
			}
			return map[string]any{"inbound": in}, nil
		})
	case "enable", "disable":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("inbound kind is required")}
		}
		kind := args[1]
		reason := flagValue(args[2:], "reason")
		enable := args[0] == "enable"
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			i := inboundIndex(cfg.NodeInbound, kind)
			if i < 0 {
				return nil, commandError{"config.inbound_unknown", fmt.Errorf("%s", kind)}
			}
			cfg.NodeInbound[i].Enabled = enable
			if !enable {
				if reason == "" {
					reason = "disabled"
				}
				cfg.NodeInbound[i].DisabledCause = reason
			} else {
				cfg.NodeInbound[i].DisabledCause = ""
			}
			return map[string]any{"inbound": cfg.NodeInbound[i]}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("inbound %s", strings.Join(args, " "))}
}

func localPeers(g globals, out io.Writer) error {
	cfg, err := loadConfig(g)
	if err != nil {
		return err
	}
	return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "peers": cfg.Peers})
}

func localPeer(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("peer subcommand is required")}
	}
	if args[0] == "trust" {
		return localPeerTrust(g, args[1:], out)
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer name is required")}
		}
		name := args[1]
		fs := flag.NewFlagSet("peer add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		nodeID := fs.String("node-id", "", "")
		addr := fs.String("addr", "", "")
		direction := fs.String("direction", string(route.DirectionOutbound), "")
		profile := fs.String("profile", "", "")
		nested := fs.Bool("nested", false, "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *nodeID == "" {
			return commandError{"cli.argument_required", fmt.Errorf("--node-id is required")}
		}
		if route.Direction(*direction).CanDialOutbound() && *profile == "" {
			return commandError{"cli.argument_required", fmt.Errorf("--profile is required for an outbound peer")}
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			if _, _, ok := configstore.FindPeer(cfg.Peers, name); ok {
				return nil, commandError{"config.peer_exists", fmt.Errorf("%s", name)}
			}
			p := configstore.PeerConfig{
				Name:          name,
				NodeID:        *nodeID,
				DisplayName:   name,
				Addr:          *addr,
				GatewayAddr:   *addr,
				Direction:     route.Direction(*direction),
				XrayProfileID: *profile,
				NestedEnabled: *nested,
				Enabled:       true,
				RendrCapable:  true,
			}
			cfg.Peers = append(cfg.Peers, p)
			return map[string]any{"peer": p}, nil
		})
	case "set":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer name is required")}
		}
		name := args[1]
		fs := flag.NewFlagSet("peer set", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		direction := fs.String("direction", "", "")
		nested := fs.Bool("nested", false, "")
		addr := fs.String("addr", "", "")
		profile := fs.String("profile", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			p, i, ok := configstore.FindPeer(cfg.Peers, name)
			if !ok {
				return nil, commandError{"config.peer_unknown", fmt.Errorf("%s", name)}
			}
			if visited["direction"] {
				p.Direction = route.Direction(*direction)
			}
			if visited["nested"] {
				p.NestedEnabled = *nested
			}
			if visited["addr"] {
				p.Addr = *addr
				p.GatewayAddr = *addr
			}
			if visited["profile"] {
				p.XrayProfileID = *profile
			}
			cfg.Peers[i] = p
			return map[string]any{"peer": p}, nil
		})
	case "disable", "enable":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer name is required")}
		}
		name := args[1]
		reason := flagValue(args[2:], "reason")
		enable := args[0] == "enable"
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			p, i, ok := configstore.FindPeer(cfg.Peers, name)
			if !ok {
				return nil, commandError{"config.peer_unknown", fmt.Errorf("%s", name)}
			}
			p.Enabled = enable
			if enable {
				p.DisabledCause = ""
			} else {
				if reason == "" {
					reason = "disabled"
				}
				p.DisabledCause = reason
			}
			cfg.Peers[i] = p
			return map[string]any{"peer": p}, nil
		})
	case "remove":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("peer name is required")}
		}
		name := args[1]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			_, i, ok := configstore.FindPeer(cfg.Peers, name)
			if !ok {
				return nil, commandError{"config.peer_unknown", fmt.Errorf("%s", name)}
			}
			cfg.Peers = append(cfg.Peers[:i], cfg.Peers[i+1:]...)
			return map[string]any{"removed": name}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("peer %s", strings.Join(args, " "))}
}

func localPeerTrust(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("peer trust subcommand is required")}
	}
	switch args[0] {
	case "list":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "peer_trust": cfg.PeerTrust})
	case "set":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("trusted peer is required")}
		}
		peer := args[1]
		allowRaw := flagValue(args[2:], "allow")
		allow := splitCSV(allowRaw)
		if err := validateTrustScope(allow); err != nil {
			return err
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			grant := configstore.PeerTrustGrant{PeerNodeID: peer, Allow: allow, Audit: true}
			cfg.PeerTrust[peer] = grant
			return map[string]any{"peer_trust": grant}, nil
		})
	case "revoke":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("trusted peer is required")}
		}
		peer := args[1]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			delete(cfg.PeerTrust, peer)
			return map[string]any{"revoked": peer}, nil
		})
	case "explain":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("trusted peer is required")}
		}
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		grant, ok := cfg.PeerTrust[args[1]]
		return writeOutput(g, out, map[string]any{"ok": ok, "revision": cfg.Revision, "peer_trust": grant})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("peer trust %s", strings.Join(args, " "))}
}

func localXray(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("xray subcommand is required")}
	}
	if args[0] == "profiles" {
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "xray_profiles": cfg.XrayProfiles})
	}
	if args[0] != "profile" || len(args) < 2 {
		return commandError{"cli.unknown_command", fmt.Errorf("xray %s", strings.Join(args, " "))}
	}
	switch args[1] {
	case "add":
		if len(args) < 3 {
			return commandError{"cli.argument_required", fmt.Errorf("profile id is required")}
		}
		id := args[2]
		fs := flag.NewFlagSet("xray profile add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		kind := fs.String("kind", "", "")
		fs.String("server-name", "", "")
		fs.String("public-key", "", "")
		fs.String("short-id", "", "")
		fs.String("sni", "", "")
		credentialFile := fs.String("credential-file", "", "")
		username := fs.String("username", "", "")
		transport := fs.String("transport", "tcp", "")
		security := fs.String("security", "none", "")
		allowInsecure := fs.Bool("allow-insecure-plaintext", false, "")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		profile := configstore.XrayProfile{ID: id, Kind: *kind}
		switch *kind {
		case "vless":
			if option := firstVisited(visited, "server-name", "public-key", "short-id", "sni", "username"); option != "" {
				return commandError{"config.profile_option_unavailable", fmt.Errorf("--%s is unavailable for VLESS profiles", option)}
			}
			credential, err := readCredentialFile(*credentialFile)
			if err != nil {
				return commandError{"config.credential_file_invalid", err}
			}
			profile.VLESS = &configstore.VLESSProfile{
				UUID: credential, Transport: *transport, Security: *security, AllowInsecurePlaintext: *allowInsecure,
			}
		case "socks":
			if option := firstVisited(visited, "server-name", "public-key", "short-id", "sni", "transport", "security", "allow-insecure-plaintext"); option != "" {
				return commandError{"config.profile_option_unavailable", fmt.Errorf("--%s is unavailable for SOCKS profiles", option)}
			}
			var password string
			if *username != "" || *credentialFile != "" {
				var err error
				password, err = readCredentialFile(*credentialFile)
				if err != nil {
					return commandError{"config.credential_file_invalid", err}
				}
			}
			profile.SOCKS = &configstore.SOCKSProfile{Username: *username, Password: password}
		}
		if err := xrayconfig.CompileProfile(profile); err != nil {
			return commandError{"config.profile_invalid", err}
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			p := profile
			cfg.XrayProfiles[id] = p
			return map[string]any{"xray_profile": p}, nil
		})
	case "validate":
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		id := ""
		if len(args) > 2 {
			id = args[2]
		}
		if id != "" {
			profile, ok := cfg.XrayProfiles[id]
			if !ok {
				return commandError{"config.profile_unknown", fmt.Errorf("%s", id)}
			}
			if err := xrayconfig.CompileProfile(profile); err != nil {
				return commandError{"config.profile_invalid", err}
			}
		} else {
			for _, profile := range cfg.XrayProfiles {
				if err := xrayconfig.CompileProfile(profile); err != nil {
					return commandError{"config.profile_invalid", fmt.Errorf("%s: %w", profile.ID, err)}
				}
			}
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "profile": id})
	case "remove":
		if len(args) < 3 {
			return commandError{"cli.argument_required", fmt.Errorf("profile id is required")}
		}
		id := args[2]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			if profileInUse(*cfg, id) {
				return nil, commandError{"config.in_use", fmt.Errorf("%s", id)}
			}
			delete(cfg.XrayProfiles, id)
			return map[string]any{"removed": id}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("xray profile %s", strings.Join(args[1:], " "))}
}

func localTopology(g globals, args []string, out io.Writer) error {
	cfg, err := loadConfig(g)
	if err != nil {
		return err
	}
	topo := localview.TopologyFromConfig(cfg)
	return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "topology": localview.TopologyLines(topo)})
}

func dispatchPath(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("path subcommand is required")}
	}
	switch args[0] {
	case "compile", "explain":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("path expression is required")}
		}
		expr := args[1]
		fs := flag.NewFlagSet("path compile", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		strategy := fs.String("strategy", string(route.StrategySelector), "")
		endpoint := fs.String("endpoint", string(route.EndpointRendrStream), "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		topo := localview.TopologyFromConfig(cfg)
		intent := route.RouteIntent{Paths: splitCSV(expr), Strategy: route.Strategy(*strategy), EndpointKind: route.EndpointKind(*endpoint)}
		compiled, err := route.Compile(topo, intent)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "compiled": compiled})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("path %s", strings.Join(args, " "))}
}

func mutate(g globals, out io.Writer, fn func(*configstore.Config) (any, error)) error {
	if !g.daemon && !g.dryRun {
		return commandError{"cli.offline_read_only", fmt.Errorf("configuration changes must be executed by xtierd")}
	}
	if !g.dryRun && g.revision < 0 {
		return commandError{"config.revision_required", fmt.Errorf("mutating daemon commands require an expected revision")}
	}
	if g.dryRun {
		cfg, err := loadConfig(g)
		if err != nil {
			return err
		}
		before := cfg.Revision
		if err := configstore.ValidateRevision(cfg, g.revision); err != nil {
			return err
		}
		payload, err := fn(&cfg)
		if err != nil {
			return err
		}
		cfg.Revision = before
		if err := configstore.Validate(cfg); err != nil {
			return err
		}
		return writeOutput(g, out, mutationResponse(true, before, before, payload))
	}
	if g.ctx != nil {
		if err := g.ctx.Err(); err != nil {
			return publicerr.Wrap("control.mutation_timeout", err)
		}
	}

	var payload any
	update := configstore.UpdateCAS
	if g.stateStore != nil {
		update = func(_ string, revision int64, mutation func(*configstore.Config) error) (configstore.UpdateResult, error) {
			return configstore.UpdateStoreCAS(g.stateStore, revision, mutation)
		}
	} else if g.ownershipKey != "" {
		update = func(path string, revision int64, mutation func(*configstore.Config) error) (configstore.UpdateResult, error) {
			return configstore.UpdatePinnedCAS(path, g.ownershipKey, revision, mutation)
		}
	}
	result, err := update(g.configPath, g.revision, func(cfg *configstore.Config) error {
		if g.ctx != nil {
			if err := g.ctx.Err(); err != nil {
				return publicerr.Wrap("control.mutation_timeout", err)
			}
		}
		var err error
		payload, err = fn(cfg)
		if err != nil {
			return err
		}
		if g.ctx != nil {
			if err := g.ctx.Err(); err != nil {
				return publicerr.Wrap("control.mutation_timeout", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := writeOutput(g, out, mutationResponse(false, result.BeforeRevision, result.AfterRevision, payload)); err != nil {
		return appliedCommandError{err: err}
	}
	return nil
}

func mutationErrorOutcome(err error) (bool, controlapi.MutationOutcome) {
	var applied appliedCommandError
	if errors.As(err, &applied) || configstore.CommitVisible(err) {
		return true, controlapi.MutationOutcomeApplied
	}
	if errors.Is(err, configstore.ErrCommitOutcomeUnknown) {
		return false, controlapi.MutationOutcomeIndeterminate
	}
	return false, controlapi.MutationOutcomeNotApplied
}

func mutationResponse(dryRun bool, before, after int64, payload any) map[string]any {
	return map[string]any{
		"ok":              true,
		"changed":         true,
		"dry_run":         dryRun,
		"before_revision": before,
		"after_revision":  after,
		"result":          payload,
	}
}

func writeOutput(g globals, out io.Writer, v any) error {
	safe, err := sanitizeOutput(v)
	if err != nil {
		return err
	}
	if g.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(safe)
	}
	b, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

func sanitizeOutput(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var safe any
	if err := decoder.Decode(&safe); err != nil {
		return nil, err
	}
	redactSensitiveOutput(safe)
	return safe, nil
}

func redactSensitiveOutput(v any) {
	switch value := v.(type) {
	case map[string]any:
		for key, child := range value {
			if sensitiveOutputKey(key) || sensitiveOptionMapKey(key) {
				value[key] = "[REDACTED]"
				continue
			}
			redactSensitiveOutput(child)
		}
	case []any:
		for _, child := range value {
			redactSensitiveOutput(child)
		}
	}
}

func sensitiveOptionMapKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	if normalized != "options" && normalized != "headers" && normalized != "metadata" {
		return false
	}
	// Free-form maps cannot be redacted reliably by inspecting keys alone: a
	// credential may be named id, token, uuid, key, or an adapter-specific name.
	return true
}

func sensitiveOutputKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	// This walker also visits maps keyed by user-defined IDs. Broad substring
	// matching would turn an ID such as "private-link" into a redacted scalar
	// and corrupt the response shape. Free-form maps are handled wholesale by
	// sensitiveOptionMapKey; structured fields must be named explicitly here.
	switch normalized {
	case "seed", "node_seed", "nodeseed", "seed_material",
		"private_key", "privatekey", "secret", "shared_secret",
		"password", "passphrase", "credential", "credentials",
		"credential_value", "credential_file", "uuid", "psk",
		"pre_shared_key", "token", "access_token", "refresh_token", "cookie":
		return true
	}
	return false
}

func readCredentialFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("credential file is required")
	}
	content, err := configstore.ReadProtectedFile(path, 4096)
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", fmt.Errorf("credential file must contain between 1 and 4096 bytes")
	}
	credential := strings.TrimSpace(string(content))
	if credential == "" || strings.ContainsAny(credential, "\r\n\x00") {
		return "", fmt.Errorf("credential file must contain exactly one non-empty line")
	}
	return credential, nil
}

func errorCode(err error) string {
	var ce commandError
	if asCommandError(err, &ce) {
		return ce.code
	}
	var re *route.CompileError
	if asRouteError(err, &re) {
		return re.Code
	}
	return publicerr.Code(err, "operation.failed")
}

func asCommandError(err error, target *commandError) bool {
	if e, ok := err.(commandError); ok {
		*target = e
		return true
	}
	return false
}

func asRouteError(err error, target **route.CompileError) bool {
	if e, ok := err.(*route.CompileError); ok {
		*target = e
		return true
	}
	return false
}

func inboundIndex(inbounds []configstore.InboundConfig, kind string) int {
	for i, in := range inbounds {
		if in.Kind == kind {
			return i
		}
	}
	return -1
}

func flagValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
		prefix := "--" + name + "="
		if strings.HasPrefix(args[i], prefix) {
			return strings.TrimPrefix(args[i], prefix)
		}
	}
	return ""
}

func flagPresent(args []string, name string) bool {
	for _, arg := range args {
		if arg == "--"+name || strings.HasPrefix(arg, "--"+name+"=") {
			return true
		}
	}
	return false
}

func firstVisited(visited map[string]bool, names ...string) string {
	for _, name := range names {
		if visited[name] {
			return name
		}
	}
	return ""
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func commandMutates(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] != "local" || len(args) < 2 {
		return false
	}
	if args[1] == "reload" {
		return true
	}
	if len(args) < 3 {
		return false
	}
	subsystem, action := args[1], args[2]
	switch subsystem {
	case "identity":
		return action == "init" || action == "rename"
	case "settings":
		return action == "set"
	case "config":
		return action == "restore-last-good"
	case "inbound":
		return action == "set" || action == "enable" || action == "disable"
	case "peer":
		if action == "trust" && len(args) >= 4 {
			return args[3] == "set" || args[3] == "revoke"
		}
		return action == "add" || action == "set" || action == "enable" || action == "disable" || action == "remove"
	case "xray":
		return len(args) >= 4 && args[2] == "profile" && (args[3] == "add" || args[3] == "remove")
	default:
		return false
	}
}

// CommandMutates is used by the daemon control boundary to decide whether a
// request needs idempotency tracking. Authorization remains inside dispatch.
func CommandMutates(args []string) bool { return commandMutates(args) }

func isDaemonStatus(args []string) bool {
	return len(args) == 2 && args[0] == "daemon" && args[1] == "status"
}

var allowedTrustScopes = map[string]bool{
	"peer.read":             true,
	"peer.write":            true,
	"node_inbound.read":     true,
	"node_inbound.write":    true,
	"node_outbound.read":    true,
	"node_outbound.write":   true,
	"nested.write":          true,
	"disable.write":         true,
	"common_settings.read":  true,
	"common_settings.write": true,
	"service.reload":        true,
}

func validateTrustScope(scopes []string) error {
	for _, scope := range scopes {
		if !allowedTrustScopes[scope] {
			return commandError{"peer_trust.scope_forbidden", fmt.Errorf("%s belongs outside the node core plane", scope)}
		}
	}
	return nil
}

func profileInUse(cfg configstore.Config, id string) bool {
	return configstore.LocalProfileInUse(cfg, id)
}

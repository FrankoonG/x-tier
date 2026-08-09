package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/localview"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/settings"
)

type globals struct {
	configPath string
	control    string
	offline    bool
	json       bool
	dryRun     bool
	revision   int64
}

type commandError struct {
	code string
	err  error
}

func (e commandError) Error() string {
	if e.err == nil {
		return e.code
	}
	return e.code + ": " + e.err.Error()
}

func Run(args []string, stdout, stderr io.Writer) int {
	g, rest, err := parseGlobals(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "command is required")
		return 2
	}
	if !g.offline {
		return runViaDaemon(g, rest, stdout, stderr)
	}
	if err := dispatch(g, rest, stdout); err != nil {
		if g.json {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": false, "error_code": errorCode(err), "message": err.Error()})
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
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
	resp, err := controlapi.Execute(g.control, controlapi.Request{
		Args:     args,
		JSON:     g.json,
		DryRun:   g.dryRun,
		Revision: g.revision,
	})
	if err != nil {
		if g.json {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"ok": false, "error_code": errorCode(err), "message": err.Error()})
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	if resp.Stdout != "" {
		_, _ = io.WriteString(stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = io.WriteString(stderr, resp.Stderr)
	}
	return resp.ExitCode
}

func dispatch(g globals, args []string, out io.Writer) error {
	switch args[0] {
	case "local":
		return dispatchLocal(g, args[1:], out)
	case "path":
		return dispatchPath(g, args[1:], out)
	case "config":
		if len(args) > 1 && args[1] == "validate" {
			cfg, err := configstore.Load(g.configPath)
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
		cfg, err := configstore.Load(g.configPath)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "reloaded": false, "reason": "service control is not implemented in local CLI MVP"})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("local %s", strings.Join(args, " "))}
}

func localStatus(g globals, out io.Writer) error {
	cfg, err := configstore.Load(g.configPath)
	if err != nil {
		return err
	}
	topo := localview.TopologyFromConfig(cfg)
	relations := route.PeerRelations(topo)
	local := relations[route.NodeID(cfg.Node.NodeID)]
	return writeOutput(g, out, map[string]any{
		"ok":                true,
		"revision":          cfg.Revision,
		"node":              cfg.Node,
		"settings":          cfg.System,
		"rendr_instance_id": cfg.Node.RendrInstanceID,
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
		cfg, err := configstore.Load(g.configPath)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "node": cfg.Node})
	case "init":
		fs := flag.NewFlagSet("identity init", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := fs.String("name", "", "display name")
		role := fs.String("role", "thin", "role")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			if cfg.Node.NodeID != "" {
				return nil, commandError{"identity.exists", fmt.Errorf("node_id is already initialized")}
			}
			id, err := randomID()
			if err != nil {
				return nil, err
			}
			cfg.Node.NodeID = id
			cfg.Node.DisplayName = *name
			if cfg.Node.DisplayName == "" {
				cfg.Node.DisplayName = id
			}
			cfg.Node.Role = *role
			cfg.Node.RendrCapable = true
			return map[string]any{"node": cfg.Node}, nil
		})
	case "rename":
		if len(args) < 2 {
			return commandError{"cli.argument_required", fmt.Errorf("display name is required")}
		}
		name := args[1]
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			cfg.Node.DisplayName = name
			return map[string]any{"node": cfg.Node}, nil
		})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("identity %s", strings.Join(args, " "))}
}

func localSettings(g globals, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError{"cli.command_required", fmt.Errorf("settings subcommand is required")}
	}
	switch args[0] {
	case "show":
		cfg, err := configstore.Load(g.configPath)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "settings": cfg.System})
	case "validate":
		cfg, err := configstore.Load(g.configPath)
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
		cfg, err := configstore.Load(g.configPath)
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
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			i := inboundIndex(cfg.NodeInbound, kind)
			in := configstore.InboundConfig{Kind: kind, Enabled: true}
			if i >= 0 {
				in = cfg.NodeInbound[i]
			}
			if *listen != "" {
				in.Listen = *listen
			}
			if *profile != "" {
				in.XrayProfileID = *profile
			}
			in.Enabled = true
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
	cfg, err := configstore.Load(g.configPath)
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
		nodeID := fs.String("node-id", name, "")
		addr := fs.String("addr", "", "")
		direction := fs.String("direction", string(route.DirectionOutbound), "")
		profile := fs.String("profile", "", "")
		nested := fs.Bool("nested", false, "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
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
				InstanceID:    "inst-" + *nodeID,
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
		cfg, err := configstore.Load(g.configPath)
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
		cfg, err := configstore.Load(g.configPath)
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
		cfg, err := configstore.Load(g.configPath)
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
		serverName := fs.String("server-name", "", "")
		publicKey := fs.String("public-key", "", "")
		shortID := fs.String("short-id", "", "")
		sni := fs.String("sni", "", "")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		options := map[string]string{}
		if *serverName != "" {
			options["server_name"] = *serverName
		}
		if *publicKey != "" {
			options["public_key"] = *publicKey
		}
		if *shortID != "" {
			options["short_id"] = *shortID
		}
		if *sni != "" {
			options["sni"] = *sni
		}
		return mutate(g, out, func(cfg *configstore.Config) (any, error) {
			p := configstore.XrayProfile{ID: id, Kind: *kind, Options: options}
			cfg.XrayProfiles[id] = p
			return map[string]any{"xray_profile": p}, nil
		})
	case "validate":
		cfg, err := configstore.Load(g.configPath)
		if err != nil {
			return err
		}
		id := ""
		if len(args) > 2 {
			id = args[2]
		}
		if id != "" {
			if _, ok := cfg.XrayProfiles[id]; !ok {
				return commandError{"config.profile_unknown", fmt.Errorf("%s", id)}
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
	cfg, err := configstore.Load(g.configPath)
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
		cfg, err := configstore.Load(g.configPath)
		if err != nil {
			return err
		}
		topo := localview.TopologyFromConfig(cfg)
		intent := route.RouteIntent{Paths: splitCSV(expr), Strategy: route.Strategy(*strategy), EndpointKind: route.EndpointKind(*endpoint)}
		compiled, err := route.Compile(topo, intent)
		if err != nil {
			return err
		}
		return writeOutput(g, out, map[string]any{"ok": true, "revision": cfg.Revision, "compiled": compiled.CompiledRoute})
	}
	return commandError{"cli.unknown_command", fmt.Errorf("path %s", strings.Join(args, " "))}
}

func mutate(g globals, out io.Writer, fn func(*configstore.Config) (any, error)) error {
	var response any
	err := configstore.WithLock(g.configPath, func() error {
		cfg, err := configstore.Load(g.configPath)
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
		if err := configstore.Validate(cfg); err != nil {
			return err
		}
		if !g.dryRun {
			cfg.Revision++
			if err := configstore.Save(g.configPath, cfg); err != nil {
				return err
			}
		}
		after := cfg.Revision
		if g.dryRun {
			after = before
		}
		response = map[string]any{"ok": true, "changed": true, "dry_run": g.dryRun, "before_revision": before, "after_revision": after, "result": payload}
		return nil
	})
	if err != nil {
		return err
	}
	return writeOutput(g, out, response)
}

func writeOutput(g globals, out io.Writer, v any) error {
	if g.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	_, err := fmt.Fprintln(out, string(b))
	return err
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
	msg := err.Error()
	if i := strings.Index(msg, ":"); i > 0 {
		token := msg[:i]
		if strings.Contains(token, ".") {
			return token
		}
	}
	return "error"
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
	for _, in := range cfg.NodeInbound {
		if in.XrayProfileID == id {
			return true
		}
	}
	var walk func([]configstore.PeerConfig) bool
	walk = func(peers []configstore.PeerConfig) bool {
		for _, p := range peers {
			if p.XrayProfileID == id {
				return true
			}
			if walk(p.Children) {
				return true
			}
		}
		return false
	}
	return walk(cfg.Peers)
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "node-" + hex.EncodeToString(b[:]), nil
}

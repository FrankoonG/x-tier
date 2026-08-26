/* ===========================================================================
 * BACKEND SHAPES
 *
 * Transcribed from the Go source, field for field, including the JSON tags.
 * Where a field is optional in Go it is optional here; where the backend can
 * only ever produce one value, the type says so rather than leaving room for
 * values that will never arrive.
 * ======================================================================== */

/* -- internal/route/types.go:64-70 ---------------------------------------- */
export type Strategy = 'selector' | 'race' | 'bond' | 'peak';

/** Who may dial whom. NOT a description of traffic, and not a live state. */
export type Direction = 'inbound' | 'outbound' | 'bidirectional';

/*
 * The three the daemon actually accepts.
 *
 * `EndpointKind.SessionKind()` (route/types.go:53-62) switches on exactly
 * rendr_stream, rendr_packet and egress; anything else falls to `default` and
 * the compile is refused with route.endpoint_unknown. This union previously
 * carried `legacy_stream`, which the daemon has never known — so the one option
 * the panel offered beyond the two rendr kinds could not compile, while the
 * guidance shown on failure named it as valid.
 *
 * `egress` maps to the STREAM session kind, not to a third kind, and it is the
 * only endpoint that does not require the terminal to advertise rendr.
 */
export type EndpointKind = 'rendr_stream' | 'rendr_packet' | 'egress';

/* -- internal/controlserver/domain.go ------------------------------------- */
export type IdentityState =
  | 'uninitialized'
  | 'backed'
  | 'recoverable'
  /* The three below are dead ends: no domain operation remediates them. The UI must say
   * so rather than offering a fix that does not exist. */
  | 'legacy_unbacked'
  | 'backing_missing'
  | 'mismatch';

export interface IdentityView {
  state: IdentityState;
  version?: number;
  algorithm?: string;
  node_id?: string;
  public_key?: string;
  /** Present only in `mismatch`: what the seed file actually backs. */
  backing_node_id?: string;
  backing_public_key?: string;
  os_acl_release_qualified: boolean;
}

/* -- internal/configstore/config.go:32-42 --------------------------------- */
export interface NodeConfig {
  node_id?: string;
  display_name?: string;
  /** Read-only legacy metadata. It is absent from domain mutations. */
  role?: string;
  public_key?: string;
  rendr_capable: boolean;
  disabled?: boolean;
  disabled_cause?: string;
}

/* -- internal/configstore/config.go:52-66 ---------------------------------
 * Note what is NOT here: a public key. A peer is identified by an opaque
 * string that is never parsed or validated against the identity package
 * (config.go:421-423). Peers are not cryptographically identified, and the UI
 * must not imply otherwise. */
export interface PeerConfig {
  /** Primary key, unique. Also what `peer set/enable/disable/remove` accept. */
  name: string;
  /** Arbitrary string. Path expressions resolve hops by THIS, never by name. */
  node_id: string;
  display_name?: string;
  addr?: string;
  gateway_addr?: string;
  direction: Direction;
  xray_profile_id?: string;
  /** Permission to be a NON-FINAL hop. False is still fine as a direct hop. */
  nested_enabled: boolean;
  /** Administrative intent, typed by a human. Never derived from a dial. */
  enabled: boolean;
  disabled_cause?: string;
  rendr_capable: boolean;
  rendr_instance_id?: string;
  children?: PeerConfig[];
}

/* -- internal/configstore/config.go:44-50 ---------------------------------
 * `kind` is the PRIMARY KEY. `inboundIndex` resolves by it, so there is at
 * most one listener per kind and `inbound set socks` rewrites the existing
 * socks row rather than adding a second one. */
export interface InboundConfig {
  kind: string;
  purpose?: string;
  listen: string;
  enabled: boolean;
  xray_profile_id?: string;
  exit_peer?: string;
  disabled_cause?: string;
}

/* -- internal/settings/settings.go:20-28 -----------------------------------
 * The complete set. There is no `control_addr` here — that is the daemon's
 * bind address — and no strict-outbound flag: those are RUNTIME readings on
 * `DaemonStatus.xray`, not settings anyone can write. */
export interface SystemSettings {
  data_dir?: string;
  log_level?: string;
  max_nested_depth?: number;
  max_response_nodes?: number;
  max_response_bytes?: number;
  max_cache_entries?: number;
  max_fetch_fan_out?: number;
}

/* Every configuration mutation returns the revision transition and its typed
 * resource result. A dry run reports `before === after` because nothing moved. */
export interface MutationResponse<T = unknown> {
  api_version: 1;
  ok: true;
  changed: boolean;
  dry_run: boolean;
  /** Present only for a non-dry-run mutation. */
  applied?: true;
  /** Present only for a non-dry-run mutation. */
  outcome?: 'applied';
  before_revision: number;
  after_revision: number;
  result: T;
}

export interface LocalStatus {
  api_version: 1;
  ok: boolean;
  revision: number;
  /** Hardcoded `"config_only"`; runtime observations come from `/v1/status`. */
  status_source: 'config_only';
  identity: IdentityView;
  node: NodeConfig;
  display_name?: string;
  settings: SystemSettings;
  /** `available` is hardcoded false — "cannot observe", not "is down". */
  runtime: { available: boolean; source: string };
  peer_counts: { inbound: number; outbound: number; bidirectional: number };
  /**
   * `null` when no listener is configured.
   *
   * Go marshals a nil slice as `null`, and `normalize()` initialises the config
   * maps but not the slices. So an empty listener set arrives as `null`, not
   * `[]` — and code that assumes an array crashes the page on a perfectly
   * healthy node that simply has no inbounds.
   */
  inbounds: InboundConfig[] | null;
}

export interface PeersResponse {
  api_version: 1;
  ok: true;
  revision: number;
  /** The node the directions are relative to. */
  target_local_node_id: string;
  /** `null` for an empty address book — see the note on `LocalStatus.inbounds`. */
  peers: PeerConfig[] | null;
}

export interface EgressPortRange {
  /** Inclusive first TCP destination port. */
  from: number;
  /** Inclusive last TCP destination port. */
  to: number;
}

/** Complete source-bound permission for using this node as a TCP exit. */
export interface NodeEgressGrant {
  source_node_id: string;
  network: 'tcp';
  allow_cidrs: string[];
  allow_private_cidrs: string[];
  deny_cidrs: string[];
  allow_ports: EgressPortRange[];
}

export interface NodeEgressGrantsResponse {
  api_version: 1;
  ok: true;
  revision: number;
  /** The local node enforcing every grant in this response. */
  target_local_node_id: string;
  /** Keyed by the authenticated source peer node ID. */
  node_egress_grants: Record<string, NodeEgressGrant>;
}

export interface InboundsResponse {
  api_version: 1;
  ok: true;
  revision: number;
  target_local_node_id: string;
  /** `null` when none are configured. */
  inbounds: InboundConfig[] | null;
}

export interface SettingsResponse {
  api_version: 1;
  ok: true;
  revision: number;
  settings: SystemSettings;
}

/* Profile reads expose identity and kind only. Credentials and free-form
 * options never cross the domain API boundary. */
export interface XrayProfile {
  id: string;
  kind: string;
}

export interface XrayProfilesResponse {
  api_version: 1;
  ok: true;
  revision: number;
  /** Omitted entirely when the map is empty — `json:"xray_profiles,omitempty"`. */
  xray_profiles?: Record<string, XrayProfile>;
}

/* -- internal/controlapi/control.go:35-96 --------------------------------- */
export type DaemonState = 'starting' | 'running' | 'degraded' | 'stopping' | 'stopped';
export type RuntimeState =
  | 'unavailable'
  | 'starting'
  | 'running'
  | 'stopping'
  | 'stopped'
  | 'failed';

export interface XrayGenerationStatus {
  generation: number;
  ref_count: number;
  draining: boolean;
  cleanup_error?: string;
}

export type XrayInboundState = 'bound' | 'missing' | 'unexpected' | 'unavailable';

export interface XrayInboundStatus {
  tag: string;
  listen?: string;
  state: XrayInboundState;
}

export interface RuntimeStatus {
  state: RuntimeState;
  instance_id?: string;
  instance_id_source?: string;
  active_client_sessions?: number;
  active_accepted_sessions?: number;
  accepted_flow_ids?: number;
  total_client_sessions?: number;
  total_accepted_sessions?: number;
  last_error?: string;
  observed_at?: string;
  stream_factory?: string;
  stream_carrier?: string;
  mobility_mode?: string;
  endpoint_owned: boolean;
  packet_supported: boolean;
}

export type ReconcileState = 'pending' | 'applied' | 'failed';

export interface ReconcileStatus {
  state: ReconcileState;
  applied_revision: number;
  attempted_revision: number;
  configuration_published: boolean;
  last_error?: string;
  last_error_code?: string;
  observed_at: string;
  observation_fresh: boolean;
  consecutive_failures?: number;
  first_failure_at?: string;
  next_retry_at?: string;
}

export interface ConfigurationStatus {
  schema_version: number;
  migrated_at_startup: boolean;
  last_known_good_revision: number;
  last_known_good_error?:
    | 'lastgood.persist_failed'
    | 'lastgood.revision_ahead_of_applied';
  startup_rollback?: {
    configured_revision: number;
    applied_revision: number;
    error_code: string;
  };
}

export interface XrayStatus {
  state: RuntimeState;
  fail_stopped: boolean;
  current?: XrayGenerationStatus;
  draining: XrayGenerationStatus[];
  strict_stream_outbound: boolean;
  strict_packet_outbound: boolean;
  /** Config revision from which the active immutable authorization was compiled. */
  egress_authorization_revision: number;
  /** Lowercase SHA-256 digest; empty only when no Xray runtime is available. */
  egress_authorization_digest: string;
  /** Number of authenticated source peers granted by the active snapshot. */
  egress_authorization_sources: number;
  inbounds: XrayInboundStatus[];
}

export interface DaemonStatus {
  api_version: number;
  boot_id: string;
  state: DaemonState;
  revision: number;
  reconcile: ReconcileStatus;
  config_path: string;
  control_addr: string;
  web_addr?: string;
  started_at: string;
  idempotency: {
    scope: string;
    restart_persistent: boolean;
    provisional: boolean;
  };
  control: {
    command_ingress: number;
    command_executions: number;
    domain_ingress: number;
    domain_executions: number;
  };
  configuration: ConfigurationStatus;
  rendr: RuntimeStatus;
  xray: XrayStatus;
}

/* -- internal/route/types.go:83-168 --------------------------------------- */
export interface Edge {
  from: string;
  to: string;
  peer_name?: string;
  direction: Direction;
  xray_profile_id?: string;
  gateway_addr?: string;
  nested_enabled: boolean;
  enabled: boolean;
  disabled_cause?: string;
}

export interface ResolvedPath {
  /** Derived from the expression by route.PathID, not assigned. Not unique. */
  id: string;
  expression: string;
  /** `hops[0]` is ALWAYS the local node. A one-hop expression yields two entries. */
  hops: string[];
  final_peer: string;
  /** Identical to `final_peer`. Two names for the last hop. */
  rendr_terminal: string;
  expected_terminal_runtime_instance_id?: string;
  endpoint_kind: EndpointKind;
  session_kind: SessionKind;
  /** The constant `"xtier-chain"`. */
  leaf_transport: string;
  /** `direct` for one written hop, `relay_chain` for two or more. */
  legacy_carrier_kind?: 'direct' | 'relay_chain';
  legacy_carrier_entry?: string;
  legacy_dialable?: boolean;
  disabled_reason?: string;
  edges: Edge[];
}

export type SessionKind = 'stream' | 'packet';

/** One leaf of the target tree — a single resolved path, ready for a runtime. */
export interface RouteLeafDescriptor {
  id: string;
  logical_path_id: string;
  logical_path: ResolvedPath;
  terminal_node_id: string;
  expected_runtime_instance_id?: string;
  session_kind: SessionKind;
}

/**
 * The group tree. x-tier's compiler emits EXACTLY one level — a root whose kind
 * is the strategy, over one `path` leaf per resolved path — so `children` is
 * never itself a group in this build (compiler.go:85-103).
 */
export interface TargetSummary {
  name: string;
  kind: 'path' | 'selector' | 'race' | 'bond' | 'peak';
  children?: TargetSummary[];
  descriptor?: RouteLeafDescriptor;
}

export interface CompileResult {
  api_version: 1;
  ok: boolean;
  revision: number;
  /**
   * route.CompiledRoute, verbatim.
   *
   * The field is `resolved_paths`, not `resolved` — this type said `resolved`
   * and had no `leaves`, `session_kind` or `target` at all, which is how the
   * panel came to have no way of showing the group it had just built.
   *
   * `intent.primary_path` is omitted deliberately: the field exists on the Go
   * struct and NOTHING in the daemon ever assigns it, so any UI keyed on it is
   * permanently dead.
   */
  compiled: {
    intent: {
      paths: string[];
      strategy: Strategy;
      endpoint_kind: EndpointKind;
    };
    resolved_paths: ResolvedPath[];
    leaves: RouteLeafDescriptor[];
    session_kind: SessionKind;
    target: TargetSummary;
  };
}

/** Browser control client for the versioned JSON domain API. */
import type {
  CompileResult,
  DaemonStatus,
  Direction,
  EndpointKind,
  EgressPortRange,
  InboundsResponse,
  LocalStatus,
  NodeEgressGrantsResponse,
  PeersResponse,
  SettingsResponse,
  Strategy,
  SystemSettings,
  XrayProfilesResponse,
} from './types';

export function newRequestId(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
}

export type MutationOutcome = 'applied' | 'not_applied' | 'indeterminate';

export interface MutationPreparation {
  kind: string;
  state: string;
  node_id?: string;
}

/** A typed domain operation was rejected by the daemon. */
export class CommandFailure extends Error {
  readonly code: string;
  readonly detail: string;
  readonly exitCode: number;
  readonly applied: boolean;
  readonly outcome?: Exclude<MutationOutcome, 'indeterminate'>;
  readonly preparations: readonly MutationPreparation[];

  constructor(
    code: string,
    detail: string,
    exitCode = 1,
    outcome?: Exclude<MutationOutcome, 'indeterminate'>,
    preparations: readonly MutationPreparation[] = [],
  ) {
    super(code === detail || !detail ? code : `${code}: ${detail}`);
    this.name = 'CommandFailure';
    this.code = code;
    this.detail = detail;
    this.exitCode = exitCode;
    this.outcome = outcome;
    this.applied = outcome === 'applied';
    this.preparations = preparations.map((preparation) => ({ ...preparation }));
  }

  get isConflict(): boolean {
    return this.code === 'config.revision_conflict';
  }

  get isAppliedDespiteError(): boolean {
    return this.applied;
  }

  get isUnimplemented(): boolean {
    return this.code.endsWith('_unimplemented');
  }
}

export class TransportFailure extends Error {
  readonly outcomeUnknown: boolean;

  constructor(message: string, outcomeUnknown = false) {
    super(message);
    this.name = 'TransportFailure';
    this.outcomeUnknown = outcomeUnknown;
  }
}

const API_VERSION = 1;
const CSRF_HEADER = 'X-XTier-CSRF-Token';
const RETRYABLE_SESSION_ERRORS = new Set([
  'webbridge.session_invalid',
  'webbridge.csrf_invalid',
]);

const ROUTES = {
  health: '/v1/health',
  status: '/v1/status',
  local: '/v1/domain/local',
  identity: '/v1/domain/identity',
  identityInit: '/v1/domain/identity/init',
  settings: '/v1/domain/settings',
  peers: '/v1/domain/peers',
  peerState: '/v1/domain/peers/state',
  nodeEgressGrants: '/v1/domain/node-egress-grants',
  inbounds: '/v1/domain/inbounds',
  inboundState: '/v1/domain/inbounds/state',
  profiles: '/v1/domain/xray-profiles',
  profileValidate: '/v1/domain/xray-profiles/validate',
  pathCompile: '/v1/domain/paths/compile',
  runtimeReload: '/v1/domain/runtime/reload',
  configRestoreLastGood: '/v1/domain/config/restore-last-good',
} as const;

let csrfToken: string | null = null;
let sessionRead: Promise<string> | null = null;

function captureOptionalCSRF(response: Response) {
  const token = response.headers.get(CSRF_HEADER)?.trim();
  if (token) csrfToken = token;
}

export function resetControlSession() {
  csrfToken = null;
  sessionRead = null;
}

async function establishSession(force = false): Promise<string> {
  if (!force && csrfToken) return csrfToken;
  if (sessionRead) return sessionRead;
  if (force) csrfToken = null;

  const read = (async () => {
    let response: Response;
    try {
      response = await fetch(ROUTES.health, { credentials: 'same-origin' });
    } catch (error) {
      throw new TransportFailure(error instanceof Error ? error.message : String(error));
    }
    const token = response.headers.get(CSRF_HEADER)?.trim();
    if (token) {
      csrfToken = token;
      return token;
    }
    const body = await response.text().catch(() => '');
    const detail = `${response.status} ${body.trim()}`.trim();
    throw new TransportFailure(`control.http_status: ${detail}`);
  })();

  sessionRead = read;
  try {
    return await read;
  } finally {
    if (sessionRead === read) sessionRead = null;
  }
}

function bridgeErrorCode(body: string): string | null {
  try {
    const parsed = JSON.parse(body) as { error?: unknown; error_code?: unknown };
    if (typeof parsed.error_code === 'string') return parsed.error_code;
    return typeof parsed.error === 'string' ? parsed.error : null;
  } catch {
    return null;
  }
}

export interface JournalEntry {
  id: number;
  at: Date;
  /** Redacted CLI equivalent retained only for diagnostics and copy/paste. */
  argv: string[];
  /** The actual typed HTTP request made by Web. */
  method: HTTPMethod;
  path: string;
  revision: number;
  dryRun: boolean;
  mutating: boolean;
  requestId: string;
  durationMs?: number;
  outcome: 'pending' | 'ok' | 'failed' | 'applied_with_error' | 'unreachable' | 'unknown';
  exitCode?: number;
  errorCode?: string;
  errorDetail?: string;
}

export type CommandShell = 'posix' | 'powershell';

export interface CommandTarget {
  configPath: string;
  controlAddr: string;
}

type JournalListener = (entries: readonly JournalEntry[]) => void;

const journal: JournalEntry[] = [];
const listeners = new Set<JournalListener>();
const JOURNAL_LIMIT = 200;
let nextId = 1;

export const SECRET_OPTION_NAMES = ['credential'] as const;
const secretOptionNames = new Set<string>(SECRET_OPTION_NAMES);
const REDACTED_ARGUMENT = '<redacted>';

export function redactSecretArguments(argv: readonly string[]): string[] {
  const redacted: string[] = [];
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index]!;
    if (!token.startsWith('--') || token === '--') {
      redacted.push(token);
      continue;
    }
    const option = token.slice(2);
    const equals = option.indexOf('=');
    const name = equals >= 0 ? option.slice(0, equals) : option;
    if (!secretOptionNames.has(name)) {
      redacted.push(token);
      continue;
    }
    if (equals >= 0) {
      redacted.push(`--${name}=${REDACTED_ARGUMENT}`);
      continue;
    }
    redacted.push(token);
    if (index + 1 < argv.length) {
      redacted.push(REDACTED_ARGUMENT);
      index += 1;
    }
  }
  return redacted;
}

function redactSecretValues(detail: string, values: readonly string[] | undefined): string {
  let redacted = detail;
  for (const value of values ?? []) {
    if (value) redacted = redacted.replaceAll(value, REDACTED_ARGUMENT);
  }
  return redacted;
}

function redactThrownError(error: unknown, values: readonly string[] | undefined): Error {
  if (error instanceof CommandFailure) {
    return new CommandFailure(
      error.code,
      redactSecretValues(error.detail, values),
      error.exitCode,
      error.outcome,
      error.preparations,
    );
  }
  if (error instanceof TransportFailure) {
    return new TransportFailure(redactSecretValues(error.message, values), error.outcomeUnknown);
  }
  return new Error(redactSecretValues(error instanceof Error ? error.message : String(error), values));
}

function emit() {
  const snapshot = journal.slice();
  for (const listener of listeners) listener(snapshot);
}

export function subscribeJournal(listener: JournalListener): () => void {
  listeners.add(listener);
  listener(journal.slice());
  return () => listeners.delete(listener);
}

export function getJournal(): readonly JournalEntry[] {
  return journal.slice();
}

export function clearJournal() {
  journal.length = 0;
  emit();
}

function openJournal(
  argv: string[],
  method: HTTPMethod,
  path: string,
  revision: number,
  dryRun: boolean,
  requestId: string,
  mutating: boolean,
): JournalEntry {
  const entry: JournalEntry = {
    id: nextId++,
    at: new Date(),
    argv: redactSecretArguments(argv),
    method,
    path,
    revision,
    dryRun,
    mutating,
    requestId,
    outcome: 'pending',
  };
  journal.unshift(entry);
  if (journal.length > JOURNAL_LIMIT) journal.length = JOURNAL_LIMIT;
  emit();
  return entry;
}

function closeJournal(entry: JournalEntry, patch: Partial<JournalEntry>, startedAt: number) {
  Object.assign(entry, patch, { durationMs: Math.round(performance.now() - startedAt) });
  emit();
}

export function quoteShellToken(token: string, shell: CommandShell): string {
  const safe = shell === 'powershell'
    ? /^[A-Za-z0-9._:/=+-]+$/
    : /^[A-Za-z0-9._:/@=+-]+$/;
  if (safe.test(token)) return token;
  if (shell === 'powershell') return `'${token.replaceAll("'", "''")}'`;
  return `'${token.replaceAll("'", `'"'"'`)}'`;
}

/** Render an independent CLI equivalent; this string is never executed by Web. */
export function formatCommand(
  argv: string[],
  opts: {
    shell: CommandShell;
    target: CommandTarget;
    revision?: number;
    dryRun?: boolean;
    json?: boolean;
  },
): string {
  const parts = ['xtierctl', '--config', opts.target.configPath, '--control', opts.target.controlAddr];
  if (opts.json !== false) parts.push('--json');
  if (opts.dryRun) parts.push('--dry-run');
  if (opts.revision != null) parts.push('--revision', String(opts.revision));
  parts.push(...redactSecretArguments(argv));
  return parts.map((part) => quoteShellToken(part, opts.shell)).join(' ');
}

type HTTPMethod = 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';

interface DomainCall {
  path: string;
  method: HTTPMethod;
  body?: Record<string, unknown>;
  mutating: boolean;
  dryRun: boolean;
  secretValues?: readonly string[];
}

interface DomainErrorEnvelope {
  api_version: number;
  ok: false;
  error_code: string;
  message: string;
  applied?: boolean;
  outcome?: MutationOutcome;
  preparations?: MutationPreparation[];
}

interface MutationMeta {
  api_version: number;
  revision: number;
  dry_run: boolean;
  request_id: string;
}

export interface MutationOptions {
  revision?: number;
  dryRun?: boolean;
  requestId?: string;
}

function mutationMeta(options: MutationOptions, requestId: string): MutationMeta {
  return {
    api_version: API_VERSION,
    revision: options.revision ?? 0,
    dry_run: options.dryRun ?? false,
    request_id: requestId,
  };
}

/** A typed Web write plus a CLI equivalent used only by the local journal. */
export interface DomainMutation {
  readonly cliEquivalent: readonly string[];
  readonly path: string;
  readonly method: Exclude<HTTPMethod, 'GET'>;
  readonly body: Readonly<Record<string, unknown>>;
  readonly secretValues?: readonly string[];
}

function mutation(
  cliEquivalent: readonly string[],
  path: string,
  method: Exclude<HTTPMethod, 'GET'>,
  body: Readonly<Record<string, unknown>>,
  secretValues?: readonly string[],
): DomainMutation {
  return { cliEquivalent: [...cliEquivalent], path, method, body, secretValues };
}

function option(argv: string[], name: string, value: string | undefined) {
  if (value !== undefined) {
    argv.push(`--${name}`, value);
  }
}

export interface InboundPutInput {
  kind: string;
  listen?: string;
  purpose?: string;
  xrayProfileId?: string;
  exitPeer?: string;
}

export interface PeerCreateInput {
  name: string;
  nodeId: string;
  addr?: string;
  direction: Direction;
  xrayProfileId?: string;
  nestedEnabled: boolean;
}

export interface PeerPatchInput {
  addr?: string;
  direction?: Direction;
  xrayProfileId?: string;
  nestedEnabled?: boolean;
  /** Required when a direction patch removes inbound permission from a granted source. */
  revokeNodeEgressGrant?: boolean;
}

/** Full replacement. Omitted fields are intentionally replaced by empty lists. */
export interface NodeEgressGrantPutInput {
  sourceNodeId: string;
  network: 'tcp';
  allowCIDRs: string[];
  allowPrivateCIDRs: string[];
  denyCIDRs: string[];
  allowPorts: EgressPortRange[];
}

export interface XrayProfilePutInput {
  id: string;
  kind: string;
  credential: string;
  username?: string;
  transport?: string;
  security?: string;
  allowInsecurePlaintext?: boolean;
}

export type SettingsPatchInput = Partial<Pick<
  SystemSettings,
  | 'log_level'
  | 'max_nested_depth'
  | 'max_response_nodes'
  | 'max_response_bytes'
  | 'max_cache_entries'
  | 'max_fetch_fan_out'
>>;

const settingFlags: Record<keyof SettingsPatchInput, string> = {
  log_level: 'log-level',
  max_nested_depth: 'max-nested-depth',
  max_response_nodes: 'max-response-nodes',
  max_response_bytes: 'max-response-bytes',
  max_cache_entries: 'max-cache-entries',
  max_fetch_fan_out: 'max-fetch-fan-out',
};

export const mutations = {
  identityInit(name = ''): DomainMutation {
    const argv = ['local', 'identity', 'init'];
    if (name) option(argv, 'name', name);
    return mutation(argv, ROUTES.identityInit, 'POST', { name });
  },

  identityRename(name: string): DomainMutation {
    return mutation(['local', 'identity', 'rename', name], ROUTES.identity, 'PATCH', { name });
  },

  settingsUpdate(settings: SettingsPatchInput): DomainMutation {
    const argv = ['local', 'settings', 'set'];
    for (const [field, value] of Object.entries(settings)) {
      option(argv, settingFlags[field as keyof SettingsPatchInput], String(value));
    }
    return mutation(argv, ROUTES.settings, 'PATCH', { settings });
  },

  inboundPut(input: InboundPutInput): DomainMutation {
    const argv = ['local', 'inbound', 'set', input.kind];
    option(argv, 'listen', input.listen);
    option(argv, 'purpose', input.purpose);
    option(argv, 'profile', input.xrayProfileId);
    option(argv, 'exit-peer', input.exitPeer);
    return mutation(argv, ROUTES.inbounds, 'PUT', {
      kind: input.kind,
      listen: input.listen,
      purpose: input.purpose,
      xray_profile_id: input.xrayProfileId,
      exit_peer: input.exitPeer,
    });
  },

  inboundState(kind: string, enabled: boolean, reason = ''): DomainMutation {
    const argv = ['local', 'inbound', enabled ? 'enable' : 'disable', kind];
    if (reason) option(argv, 'reason', reason);
    return mutation(argv, ROUTES.inboundState, 'PATCH', { kind, enabled, reason });
  },

  peerCreate(input: PeerCreateInput): DomainMutation {
    const argv = ['local', 'peer', 'add', input.name, '--node-id', input.nodeId];
    option(argv, 'addr', input.addr);
    option(argv, 'direction', input.direction);
    option(argv, 'profile', input.xrayProfileId);
    if (input.nestedEnabled) argv.push('--nested');
    return mutation(argv, ROUTES.peers, 'POST', {
      name: input.name,
      node_id: input.nodeId,
      addr: input.addr,
      direction: input.direction,
      xray_profile_id: input.xrayProfileId,
      nested_enabled: input.nestedEnabled,
    });
  },

  peerUpdate(name: string, patch: PeerPatchInput): DomainMutation {
    const argv = ['local', 'peer', 'set', name];
    option(argv, 'addr', patch.addr);
    option(argv, 'direction', patch.direction);
    option(argv, 'profile', patch.xrayProfileId);
    if (patch.nestedEnabled !== undefined) {
      argv.push(patch.nestedEnabled ? '--nested' : '--nested=false');
    }
    if (patch.revokeNodeEgressGrant) argv.push('--revoke-egress-grant');
    return mutation(argv, ROUTES.peers, 'PATCH', {
      name,
      patch: {
        addr: patch.addr,
        direction: patch.direction,
        xray_profile_id: patch.xrayProfileId,
        nested_enabled: patch.nestedEnabled,
        revoke_node_egress_grant: patch.revokeNodeEgressGrant,
      },
    });
  },

  peerState(name: string, enabled: boolean, reason = ''): DomainMutation {
    const argv = ['local', 'peer', enabled ? 'enable' : 'disable', name];
    if (reason) option(argv, 'reason', reason);
    return mutation(argv, ROUTES.peerState, 'PATCH', { name, enabled, reason });
  },

  peerRemove(name: string): DomainMutation {
    return mutation(['local', 'peer', 'remove', name], ROUTES.peers, 'DELETE', { name });
  },

  nodeEgressGrantPut(peerName: string, input: NodeEgressGrantPutInput): DomainMutation {
    const argv = ['local', 'peer', 'egress', 'set', peerName, '--network', input.network];
    option(argv, 'allow-cidrs', input.allowCIDRs.join(','));
    option(argv, 'allow-private-cidrs', input.allowPrivateCIDRs.join(','));
    option(argv, 'deny-cidrs', input.denyCIDRs.join(','));
    option(
      argv,
      'allow-ports',
      input.allowPorts.map((range) => (
        range.from === range.to ? String(range.from) : `${range.from}-${range.to}`
      )).join(','),
    );
    return mutation(argv, ROUTES.nodeEgressGrants, 'PUT', {
      source_node_id: input.sourceNodeId,
      network: input.network,
      allow_cidrs: [...input.allowCIDRs],
      allow_private_cidrs: [...input.allowPrivateCIDRs],
      deny_cidrs: [...input.denyCIDRs],
      allow_ports: input.allowPorts.map((range) => ({ ...range })),
    });
  },

  nodeEgressGrantRevoke(peerName: string, sourceNodeId: string): DomainMutation {
    return mutation(
      ['local', 'peer', 'egress', 'revoke', peerName],
      ROUTES.nodeEgressGrants,
      'DELETE',
      { source_node_id: sourceNodeId },
    );
  },

  xrayProfilePut(input: XrayProfilePutInput): DomainMutation {
    const argv = ['local', 'xray', 'profile', 'add', input.id, '--kind', input.kind];
    option(argv, 'credential', input.credential);
    option(argv, 'username', input.username);
    option(argv, 'transport', input.transport);
    option(argv, 'security', input.security);
    if (input.allowInsecurePlaintext) argv.push('--allow-insecure-plaintext');
    return mutation(argv, ROUTES.profiles, 'PUT', {
      id: input.id,
      kind: input.kind,
      credential: input.credential,
      username: input.username,
      transport: input.transport,
      security: input.security,
      allow_insecure_plaintext: input.allowInsecurePlaintext ?? false,
    }, [input.credential]);
  },

  xrayProfileRemove(id: string): DomainMutation {
    return mutation(['local', 'xray', 'profile', 'remove', id], ROUTES.profiles, 'DELETE', { id });
  },

  runtimeReload(): DomainMutation {
    return mutation(['local', 'reload'], ROUTES.runtimeReload, 'POST', {});
  },

  configRestoreLastGood(): DomainMutation {
    return mutation(
      ['local', 'config', 'restore-last-good'],
      ROUTES.configRestoreLastGood,
      'POST',
      {},
    );
  },
} as const;

function decodeJSON(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown;
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new Error('response is not a JSON object');
    }
    return parsed as Record<string, unknown>;
  } catch (error) {
    throw new Error(error instanceof Error ? error.message : String(error));
  }
}

function hasOwn(value: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function typedPreparations(value: unknown): MutationPreparation[] {
  if (!Array.isArray(value)) throw new Error('preparations must be an array');
  return value.map((entry) => {
    if (typeof entry !== 'object' || entry === null || Array.isArray(entry)) {
      throw new Error('preparation must be an object');
    }
    const item = entry as Record<string, unknown>;
    if (typeof item.kind !== 'string' || !item.kind || typeof item.state !== 'string' || !item.state) {
      throw new Error('preparation kind and state are required');
    }
    if (item.node_id !== undefined && typeof item.node_id !== 'string') {
      throw new Error('preparation node_id must be a string');
    }
    return {
      kind: item.kind,
      state: item.state,
      ...(typeof item.node_id === 'string' ? { node_id: item.node_id } : {}),
    };
  });
}

function mutationFacts(
  value: Record<string, unknown>,
  call: DomainCall,
  success: boolean,
): Pick<DomainErrorEnvelope, 'applied' | 'outcome' | 'preparations'> {
  const hasApplied = hasOwn(value, 'applied');
  const hasOutcome = hasOwn(value, 'outcome');
  const hasPreparations = hasOwn(value, 'preparations');
  const expectsFacts = call.mutating && !call.dryRun;
  if (!expectsFacts) {
    if (hasApplied || hasOutcome || hasPreparations) {
      throw new Error('non-mutating response contains mutation outcome');
    }
    return {};
  }
  if (!hasApplied || !hasOutcome || typeof value.applied !== 'boolean') {
    throw new Error('mutation response is missing applied/outcome');
  }
  const outcome = value.outcome;
  if (outcome !== 'applied' && outcome !== 'not_applied' && outcome !== 'indeterminate') {
    throw new Error('mutation outcome is invalid');
  }
  if ((outcome === 'applied') !== value.applied) {
    throw new Error('mutation applied/outcome tuple is inconsistent');
  }
  if (success && outcome !== 'applied') {
    throw new Error('successful mutation was not confirmed applied');
  }
  if (hasPreparations && outcome !== 'not_applied') {
    throw new Error('preparations require a not_applied outcome');
  }
  const preparations = hasPreparations ? typedPreparations(value.preparations) : undefined;
  return { applied: value.applied, outcome, ...(preparations ? { preparations } : {}) };
}

function validateMutationSuccess(value: Record<string, unknown>, call: DomainCall): void {
  if (!call.mutating) return;

  const requestedRevision = call.body?.revision;
  if (typeof requestedRevision !== 'number' || !Number.isSafeInteger(requestedRevision) || requestedRevision < 0) {
    throw new Error('mutation request revision is invalid');
  }
  if (value.dry_run !== call.dryRun) {
    throw new Error('mutation response dry_run does not match the request');
  }
  if (typeof value.changed !== 'boolean') {
    throw new Error('mutation response is missing changed');
  }
  if (typeof value.before_revision !== 'number' || !Number.isSafeInteger(value.before_revision) || value.before_revision < 0) {
    throw new Error('mutation response before_revision is invalid');
  }
  if (typeof value.after_revision !== 'number' || !Number.isSafeInteger(value.after_revision) || value.after_revision < 0) {
    throw new Error('mutation response after_revision is invalid');
  }
  if (value.before_revision !== requestedRevision) {
    throw new Error('mutation response revision does not match the request');
  }
  const expectedAfter = call.path === ROUTES.runtimeReload
    ? requestedRevision
    : requestedRevision + 1;
  if (!Number.isSafeInteger(expectedAfter) || value.after_revision !== expectedAfter) {
    throw new Error('mutation response revision transition is invalid');
  }
}

function recordField(value: Record<string, unknown>, key: string): Record<string, unknown> {
  const field = value[key];
  if (typeof field !== 'object' || field === null || Array.isArray(field)) {
    throw new Error(`daemon status ${key} is invalid`);
  }
  return field as Record<string, unknown>;
}

function requireStatusFields(
  value: Record<string, unknown>,
  strings: readonly string[],
  booleans: readonly string[],
  integers: readonly string[],
): void {
  for (const key of strings) {
    if (typeof value[key] !== 'string') throw new Error(`daemon status ${key} is invalid`);
  }
  for (const key of booleans) {
    if (typeof value[key] !== 'boolean') throw new Error(`daemon status ${key} is invalid`);
  }
  for (const key of integers) {
    const field = value[key];
    if (typeof field !== 'number' || !Number.isSafeInteger(field) || field < 0) {
      throw new Error(`daemon status ${key} is invalid`);
    }
  }
}

const DAEMON_STATES = new Set(['starting', 'running', 'degraded', 'stopping', 'stopped']);
const RUNTIME_STATES = new Set(['unavailable', 'starting', 'running', 'stopping', 'stopped', 'failed']);
const RECONCILE_STATES = new Set(['pending', 'applied', 'failed']);
const XRAY_INBOUND_STATES = new Set(['bound', 'missing', 'unexpected', 'unavailable']);
const PUBLIC_ERROR_NAMESPACES = new Set([
  'config', 'control', 'daemon', 'dataplane', 'domain', 'identity', 'lastgood', 'node',
  'path', 'peer', 'rendradapter', 'route', 'runtime', 'service', 'settings', 'topology',
  'webbridge', 'xrayconfig', 'xrayrt',
]);

function requireEnum(value: Record<string, unknown>, key: string, allowed: ReadonlySet<string>): string {
  const field = value[key];
  if (typeof field !== 'string' || !allowed.has(field)) {
    throw new Error(`daemon status ${key} is invalid`);
  }
  return field;
}

function optionalString(value: Record<string, unknown>, key: string): string | undefined {
  const field = value[key];
  if (field === undefined) return undefined;
  if (typeof field !== 'string') throw new Error(`daemon status ${key} is invalid`);
  return field;
}

function optionalInteger(value: Record<string, unknown>, key: string): number | undefined {
  const field = value[key];
  if (field === undefined) return undefined;
  if (typeof field !== 'number' || !Number.isSafeInteger(field) || field < 0) {
    throw new Error(`daemon status ${key} is invalid`);
  }
  return field;
}

function validPublicErrorCode(value: string): boolean {
  const match = /^([a-z][a-z0-9_]*)(?:\.[a-z][a-z0-9_]*)+$/.exec(value);
  return match !== null && PUBLIC_ERROR_NAMESPACES.has(match[1]!);
}

function validateGeneration(value: unknown): void {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('daemon status xray generation is invalid');
  }
  const generation = value as Record<string, unknown>;
  requireStatusFields(generation, [], ['draining'], ['generation', 'ref_count']);
  optionalString(generation, 'cleanup_error');
}

function validateNodeEgressGrant(value: unknown, expectedSource?: string): void {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('node egress grant is invalid');
  }
  const grant = value as Record<string, unknown>;
  if (typeof grant.source_node_id !== 'string' || grant.source_node_id.length === 0
    || (expectedSource !== undefined && grant.source_node_id !== expectedSource)
    || grant.network !== 'tcp') {
    throw new Error('node egress grant identity is invalid');
  }
  for (const key of ['allow_cidrs', 'allow_private_cidrs', 'deny_cidrs']) {
    if (!Array.isArray(grant[key]) || !grant[key].every((entry) => typeof entry === 'string')) {
      throw new Error(`node egress grant ${key} is invalid`);
    }
  }
  if (!Array.isArray(grant.allow_ports)) {
    throw new Error('node egress grant allow_ports is invalid');
  }
  for (const value of grant.allow_ports) {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
      throw new Error('node egress grant port range is invalid');
    }
    const portRange = value as Record<string, unknown>;
    if (typeof portRange.from !== 'number' || !Number.isSafeInteger(portRange.from)
      || typeof portRange.to !== 'number' || !Number.isSafeInteger(portRange.to)
      || portRange.from < 1 || portRange.to > 65535 || portRange.from > portRange.to) {
      throw new Error('node egress grant port range is invalid');
    }
  }
}

function validateNodeEgressGrantsResponse(value: Record<string, unknown>): void {
  if (value.ok !== true || typeof value.revision !== 'number'
    || !Number.isSafeInteger(value.revision) || value.revision < 0
    || typeof value.target_local_node_id !== 'string'
    || typeof value.node_egress_grants !== 'object' || value.node_egress_grants === null
    || Array.isArray(value.node_egress_grants)) {
    throw new Error('node egress grants response is invalid');
  }
  for (const [source, grant] of Object.entries(value.node_egress_grants)) {
    if (!source) throw new Error('node egress grant key is invalid');
    validateNodeEgressGrant(grant, source);
  }
}

function validateDaemonStatus(value: Record<string, unknown>): void {
  requireStatusFields(value, ['boot_id', 'config_path', 'control_addr', 'started_at'], [], ['revision']);
  const daemonState = requireEnum(value, 'state', DAEMON_STATES);
  optionalString(value, 'web_addr');

  const reconcile = recordField(value, 'reconcile');
  requireStatusFields(
    reconcile,
    ['observed_at'],
    ['observation_fresh', 'configuration_published'],
    ['applied_revision', 'attempted_revision'],
  );
  const reconcileState = requireEnum(reconcile, 'state', RECONCILE_STATES);
  const reconcileError = optionalString(reconcile, 'last_error');
  const reconcileErrorCode = optionalString(reconcile, 'last_error_code');
  optionalInteger(reconcile, 'consecutive_failures');
  optionalString(reconcile, 'first_failure_at');
  optionalString(reconcile, 'next_retry_at');
  if (reconcileErrorCode !== undefined && (!reconcileError || !validPublicErrorCode(reconcileErrorCode))) {
    throw new Error('daemon status reconcile error is invalid');
  }
  if (reconcileState === 'applied' && reconcile.configuration_published !== true) {
    throw new Error('daemon status applied reconcile is not published');
  }

  const idempotency = recordField(value, 'idempotency');
  requireStatusFields(idempotency, ['scope'], ['restart_persistent', 'provisional'], []);
  if (idempotency.scope !== 'process_memory' || idempotency.restart_persistent !== false || idempotency.provisional !== true) {
    throw new Error('daemon status idempotency is invalid');
  }

  const control = recordField(value, 'control');
  requireStatusFields(control, [], [], ['command_ingress', 'command_executions', 'domain_ingress', 'domain_executions']);

  const configuration = recordField(value, 'configuration');
  requireStatusFields(configuration, [], ['migrated_at_startup'], ['schema_version', 'last_known_good_revision']);
  if ((configuration.schema_version as number) < 1) {
    throw new Error('daemon status configuration is invalid');
  }
  const lastGoodError = optionalString(configuration, 'last_known_good_error');
  if (lastGoodError !== undefined && lastGoodError !== 'lastgood.persist_failed'
    && lastGoodError !== 'lastgood.revision_ahead_of_applied') {
    throw new Error('daemon status last-known-good error is invalid');
  }
  if (configuration.startup_rollback !== undefined) {
    const rollback = recordField(configuration, 'startup_rollback');
    requireStatusFields(rollback, ['error_code'], [], ['applied_revision']);
    const configuredRevision = rollback.configured_revision;
    const rollbackCode = rollback.error_code as string;
    const configuredRevisionValid = rollbackCode === 'config.startup_content_invalid'
      ? configuredRevision === -1
      : typeof configuredRevision === 'number' && Number.isSafeInteger(configuredRevision) && configuredRevision >= 0;
    if (!configuredRevisionValid
      || !['degraded', 'stopping', 'stopped'].includes(daemonState)
      || (rollback.applied_revision as number) < (configuration.last_known_good_revision as number)
      || (rollback.applied_revision as number) > (reconcile.applied_revision as number)
      || !validPublicErrorCode(rollbackCode)) {
      throw new Error('daemon status startup rollback is invalid');
    }
  }

  const rendr = recordField(value, 'rendr');
  requireStatusFields(rendr, [], ['endpoint_owned', 'packet_supported'], []);
  const rendrState = requireEnum(rendr, 'state', RUNTIME_STATES);
  const instanceID = optionalString(rendr, 'instance_id');
  for (const key of ['instance_id_source', 'last_error', 'observed_at', 'stream_factory', 'stream_carrier', 'mobility_mode']) {
    optionalString(rendr, key);
  }
  for (const key of ['active_client_sessions', 'active_accepted_sessions', 'accepted_flow_ids', 'total_client_sessions', 'total_accepted_sessions']) {
    optionalInteger(rendr, key);
  }
  if (instanceID && rendrState !== 'running') {
    throw new Error('daemon status rendr instance is invalid');
  }
  if (rendrState !== 'unavailable' && (rendr.stream_factory !== 'xray-stream'
    || rendr.stream_carrier !== 'unknown' || rendr.mobility_mode !== 'redial_attach'
    || rendr.endpoint_owned !== false || rendr.packet_supported !== false)) {
    throw new Error('daemon status rendr capability is invalid');
  }

  const xray = recordField(value, 'xray');
  requireStatusFields(
    xray,
    ['egress_authorization_digest'],
    ['fail_stopped', 'strict_stream_outbound', 'strict_packet_outbound'],
    ['egress_authorization_sources', 'egress_authorization_denials'],
  );
  const xrayState = requireEnum(xray, 'state', RUNTIME_STATES);
  const authorizationRevision = xray.egress_authorization_revision;
  const authorizationDigest = xray.egress_authorization_digest as string;
  const authorizationDigestValid = authorizationDigest === ''
    || /^[0-9a-f]{64}$/.test(authorizationDigest);
  const authorizationFieldsValid = typeof authorizationRevision === 'number'
    && Number.isSafeInteger(authorizationRevision)
    && authorizationRevision >= -1
    && authorizationDigestValid;
  const authorizationFailStopped = xray.fail_stopped === true
    && authorizationRevision === -1
    && /^[0-9a-f]{64}$/.test(authorizationDigest)
    && xray.egress_authorization_sources === 0;
  const authorizationRunning = xrayState === 'running'
    && xray.fail_stopped === false
    && typeof authorizationRevision === 'number'
    && Number.isSafeInteger(authorizationRevision)
    && authorizationRevision >= 0
    && authorizationRevision === reconcile.applied_revision
    && /^[0-9a-f]{64}$/.test(authorizationDigest);
  if (!authorizationFieldsValid
    || (xray.fail_stopped === true && !authorizationFailStopped)
    || (xrayState === 'running' && !authorizationRunning)) {
    throw new Error('daemon status xray egress authorization is invalid');
  }
  if (!Array.isArray(xray.draining) || !Array.isArray(xray.inbounds)) {
    throw new Error('daemon status xray collections are invalid');
  }
  if (xray.current !== undefined) validateGeneration(xray.current);
  xray.draining.forEach(validateGeneration);
  for (const item of xray.inbounds) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      throw new Error('daemon status xray inbound is invalid');
    }
    const inbound = item as Record<string, unknown>;
    if (typeof inbound.tag !== 'string' || inbound.tag.length === 0) {
      throw new Error('daemon status xray inbound tag is invalid');
    }
    optionalString(inbound, 'listen');
    requireEnum(inbound, 'state', XRAY_INBOUND_STATES);
  }
  if (xray.strict_packet_outbound !== false
    || (xray.fail_stopped === true && xrayState !== 'failed')
    || (xray.current !== undefined && xrayState !== 'running' && xrayState !== 'stopping'
      && !(xray.fail_stopped === true && xrayState === 'failed'))) {
    throw new Error('daemon status xray runtime is invalid');
  }

  if (daemonState === 'running' && (lastGoodError !== undefined
    || configuration.startup_rollback !== undefined
    || configuration.last_known_good_revision !== reconcile.applied_revision
    || reconcileState !== 'applied'
    || reconcile.configuration_published !== true
    || value.revision !== reconcile.applied_revision)) {
    throw new Error('daemon status running state is inconsistent');
  }
}

function typedDomainError(value: Record<string, unknown>, call: DomainCall): DomainErrorEnvelope {
  if (value.ok !== false || typeof value.error_code !== 'string' || typeof value.message !== 'string') {
    throw new Error('missing typed domain error');
  }
  const facts = mutationFacts(value, call, false);
  return {
    api_version: API_VERSION,
    ok: false,
    error_code: value.error_code,
    message: value.message,
    ...facts,
  };
}

async function fetchJSON<T>(call: DomainCall, expectDomainOK: boolean): Promise<T> {
  const serialized = call.body === undefined ? undefined : JSON.stringify(call.body);
  let response: Response;
  let responseText: string | null = null;
  let requestMayHaveRun = false;

  try {
    if (call.method === 'GET') {
      response = await fetch(call.path, { credentials: 'same-origin' });
    } else {
      let token = await establishSession();
      requestMayHaveRun = true;
      response = await fetch(call.path, {
        method: call.method,
        credentials: 'same-origin',
        headers: { 'content-type': 'application/json', [CSRF_HEADER]: token },
        body: serialized,
      });
      if (response.status === 403) {
        requestMayHaveRun = false;
        responseText = await response.text().catch(() => '');
        const code = bridgeErrorCode(responseText);
        if (code && RETRYABLE_SESSION_ERRORS.has(code)) {
          token = await establishSession(true);
          requestMayHaveRun = true;
          responseText = null;
          response = await fetch(call.path, {
            method: call.method,
            credentials: 'same-origin',
            headers: { 'content-type': 'application/json', [CSRF_HEADER]: token },
            body: serialized,
          });
        }
      }
    }
  } catch (error) {
    const unknown = requestMayHaveRun && call.mutating && !call.dryRun;
    throw new TransportFailure(error instanceof Error ? error.message : String(error), unknown);
  }

  captureOptionalCSRF(response);
  const text = responseText ?? await response.text().catch(() => '');
  let parsed: Record<string, unknown> | null = null;
  if (text) {
    try {
      parsed = decodeJSON(text);
    } catch (error) {
      if (response.ok) {
        const unknown = call.mutating && !call.dryRun;
        throw new TransportFailure(
          `control.response_invalid: ${error instanceof Error ? error.message : String(error)}`,
          unknown,
        );
      }
    }
  }

  if (!parsed) {
    const unknown = call.mutating && !call.dryRun;
    const detail = response.ok
      ? 'empty JSON response'
      : `non-JSON HTTP ${response.status}`;
    throw new TransportFailure(`control.response_invalid: ${detail}`, unknown);
  }
  if (parsed.api_version !== API_VERSION) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure('control.response_invalid: unsupported api_version', unknown);
  }
  if (parsed.ok === false) {
    let failure: DomainErrorEnvelope;
    try {
      failure = typedDomainError(parsed, call);
    } catch (error) {
      const unknown = call.mutating && !call.dryRun;
      throw new TransportFailure(
        `control.response_invalid: ${error instanceof Error ? error.message : String(error)}`,
        unknown,
      );
    }
    if (failure.outcome === 'applied' && !response.ok) {
      throw new TransportFailure('control.response_invalid: applied error requires HTTP success', true);
    }
    if (failure.outcome !== 'applied' && response.ok) {
      throw new TransportFailure(
        'control.response_invalid: unapplied error requires HTTP failure',
        call.mutating && !call.dryRun,
      );
    }
    if (failure.outcome === 'indeterminate') {
      throw new TransportFailure(`${failure.error_code}: ${failure.message}`, true);
    }
    throw new CommandFailure(
      failure.error_code,
      failure.message,
      1,
      failure.outcome,
      failure.preparations,
    );
  }
  if (!response.ok) {
    const detail = `${response.status} ${text.trim()}`.trim();
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure(`control.response_invalid: HTTP failure lacks typed error: ${detail}`, unknown);
  }
  if (expectDomainOK && parsed.ok !== true) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure('control.response_invalid: missing typed operation outcome', unknown);
  }
  try {
    mutationFacts(parsed, call, true);
    validateMutationSuccess(parsed, call);
    if (call.path === ROUTES.status) validateDaemonStatus(parsed);
    if (call.path === ROUTES.nodeEgressGrants && call.method === 'GET') {
      validateNodeEgressGrantsResponse(parsed);
    }
  } catch (error) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure(
      `control.response_invalid: ${error instanceof Error ? error.message : String(error)}`,
      unknown,
    );
  }
  return parsed as T;
}

async function executeJournaled<T>(entry: JournalEntry, call: DomainCall, startedAt: number, expectDomainOK = true): Promise<T> {
  try {
    const result = await fetchJSON<T>(call, expectDomainOK);
    closeJournal(entry, { outcome: 'ok', exitCode: 0 }, startedAt);
    return result;
  } catch (error) {
    const safeError = redactThrownError(error, call.secretValues);
    if (safeError instanceof CommandFailure) {
      closeJournal(entry, {
        outcome: safeError.applied ? 'applied_with_error' : 'failed',
        exitCode: safeError.exitCode,
        errorCode: safeError.code,
        errorDetail: safeError.detail,
      }, startedAt);
    } else if (safeError instanceof TransportFailure) {
      closeJournal(entry, {
        outcome: safeError.outcomeUnknown ? 'unknown' : 'unreachable',
        errorCode: safeError.message.startsWith('control.response_invalid')
          ? 'control.response_invalid'
          : 'control.unreachable',
        errorDetail: safeError.message,
      }, startedAt);
    } else {
      closeJournal(entry, {
        outcome: 'failed',
        errorCode: 'web.client_failed',
        errorDetail: safeError.message,
      }, startedAt);
    }
    throw safeError;
  }
}

export async function executeMutation<T>(
  operation: DomainMutation,
  options: MutationOptions = {},
): Promise<T> {
  const requestId = options.requestId ?? newRequestId();
  const revision = options.revision ?? 0;
  const dryRun = options.dryRun ?? false;
  const startedAt = performance.now();
  const entry = openJournal(
    [...operation.cliEquivalent],
    operation.method,
    operation.path,
    revision,
    dryRun,
    requestId,
    true,
  );
  const call: DomainCall = {
    path: operation.path,
    method: operation.method,
    body: { ...mutationMeta(options, requestId), ...operation.body },
    mutating: true,
    dryRun,
    secretValues: operation.secretValues,
  };
  return executeJournaled<T>(entry, call, startedAt);
}

async function executeRead<T>(
  cliEquivalent: readonly string[],
  call: DomainCall,
  expectDomainOK = true,
): Promise<T> {
  const startedAt = performance.now();
  const entry = openJournal([...cliEquivalent], call.method, call.path, 0, false, '-', false);
  return executeJournaled<T>(entry, call, startedAt, expectDomainOK);
}

function getDomain<T>(cliEquivalent: readonly string[], path: string): Promise<T> {
  return executeRead<T>(cliEquivalent, {
    path,
    method: 'GET',
    mutating: false,
    dryRun: false,
  });
}

export function getLocalStatus(): Promise<LocalStatus> {
  return getDomain(['local', 'status'], ROUTES.local);
}

export function getSettings(): Promise<SettingsResponse> {
  return getDomain(['local', 'settings', 'show'], ROUTES.settings);
}

export function getPeers(): Promise<PeersResponse> {
  return getDomain(['local', 'peers'], ROUTES.peers);
}

export function getNodeEgressGrants(): Promise<NodeEgressGrantsResponse> {
  return getDomain(['local', 'peer', 'egress', 'list'], ROUTES.nodeEgressGrants);
}

export function getInbounds(): Promise<InboundsResponse> {
  return getDomain(['local', 'inbound', 'list'], ROUTES.inbounds);
}

export function getXrayProfiles(): Promise<XrayProfilesResponse> {
  return getDomain(['local', 'xray', 'profiles'], ROUTES.profiles);
}

export interface ProfileValidationResponse {
  api_version: 1;
  ok: true;
  revision: number;
  profile: string;
}

export function validateXrayProfile(id: string): Promise<ProfileValidationResponse> {
  return executeRead(['local', 'xray', 'profile', 'validate', id], {
    path: ROUTES.profileValidate,
    method: 'POST',
    body: { api_version: API_VERSION, id },
    mutating: false,
    dryRun: false,
  });
}

export function compilePath(
  expression: string,
  strategy: Strategy = 'selector',
  endpointKind: EndpointKind = 'rendr_stream',
): Promise<CompileResult> {
  return executeRead([
    'path', 'compile', expression,
    `--strategy=${strategy}`,
    `--endpoint=${endpointKind}`,
  ], {
    path: ROUTES.pathCompile,
    method: 'POST',
    body: {
      api_version: API_VERSION,
      expression,
      strategy,
      endpoint_kind: endpointKind,
    },
    mutating: false,
    dryRun: false,
  });
}

export async function getDaemonStatus(): Promise<DaemonStatus> {
  return executeRead<DaemonStatus>(['daemon', 'status'], {
    path: ROUTES.status,
    method: 'GET',
    mutating: false,
    dryRun: false,
  }, false);
}

/** Browser control client for the versioned JSON domain API. */
import type {
  CompileResult,
  DaemonStatus,
  Direction,
  EndpointKind,
  InboundsResponse,
  LocalStatus,
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

/** A typed domain operation was rejected by the daemon. */
export class CommandFailure extends Error {
  readonly code: string;
  readonly detail: string;
  readonly exitCode: number;
  readonly applied: boolean;

  constructor(code: string, detail: string, exitCode = 1, applied = false) {
    super(code === detail || !detail ? code : `${code}: ${detail}`);
    this.name = 'CommandFailure';
    this.code = code;
    this.detail = detail;
    this.exitCode = exitCode;
    this.applied = applied;
  }

  get isConflict(): boolean {
    return this.code === 'config.revision_conflict';
  }

  get isAppliedDespiteError(): boolean {
    return this.applied || this.code === 'config.commit_visible_and_resynced';
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
    const parsed = JSON.parse(body) as { error?: unknown };
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
  outcome: 'pending' | 'ok' | 'failed' | 'unreachable' | 'unknown';
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
      error.applied,
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
  outcome?: 'applied' | 'not_applied' | 'indeterminate';
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
    return mutation(argv, ROUTES.peers, 'PATCH', {
      name,
      patch: {
        addr: patch.addr,
        direction: patch.direction,
        xray_profile_id: patch.xrayProfileId,
        nested_enabled: patch.nestedEnabled,
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

function typedDomainError(value: Record<string, unknown>): DomainErrorEnvelope | null {
  if (value.ok !== false || typeof value.error_code !== 'string' || typeof value.message !== 'string') {
    return null;
  }
  return value as unknown as DomainErrorEnvelope;
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

  if (parsed) {
    const failure = typedDomainError(parsed);
    if (failure?.outcome === 'indeterminate') {
      throw new TransportFailure(`${failure.error_code}: ${failure.message}`, true);
    }
    if (failure) {
      if (call.mutating && !call.dryRun && response.status >= 500 && failure.outcome === undefined) {
        throw new TransportFailure(`${failure.error_code}: ${failure.message}`, true);
      }
      throw new CommandFailure(
        failure.error_code,
        failure.message,
        1,
        failure.outcome === 'applied' || failure.applied === true,
      );
    }
  }
  if (!response.ok) {
    const detail = `${response.status} ${text.trim()}`.trim();
    const unknown = response.status >= 500 && call.mutating && !call.dryRun;
    throw new TransportFailure(`control.http_status: ${detail}`, unknown);
  }
  if (!parsed) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure('control.response_invalid: empty JSON response', unknown);
  }
  if (parsed.api_version !== API_VERSION) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure('control.response_invalid: unsupported api_version', unknown);
  }
  if (expectDomainOK && parsed.ok !== true) {
    const unknown = call.mutating && !call.dryRun;
    throw new TransportFailure('control.response_invalid: missing typed operation outcome', unknown);
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
        outcome: 'failed',
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

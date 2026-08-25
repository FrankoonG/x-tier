import assert from 'node:assert/strict';
import test from 'node:test';
import {
  CommandFailure,
  TransportFailure,
  clearJournal,
  compilePath,
  executeMutation,
  formatCommand,
  getDaemonStatus,
  getInbounds,
  getJournal,
  getLocalStatus,
  getPeers,
  getSettings,
  getXrayProfiles,
  mutations,
  redactSecretArguments,
  resetControlSession,
  validateXrayProfile,
  type DomainMutation,
} from './control.ts';
import { isConfigContentInvalid } from './runtimeStatus.ts';

const target = {
  configPath: `C:\\state dir\\x'tier$(ignored)\nconfig.json`,
  controlAddr: '127.0.0.1:19090',
};
const renameArgv = ['local', 'identity', 'rename', `a'b\n$(should-not-run)`];

function validDaemonStatus(): Record<string, unknown> {
  return {
    api_version: 1,
    boot_id: 'test-boot',
    state: 'running',
    revision: 9,
    reconcile: {
      state: 'applied', applied_revision: 9, attempted_revision: 9,
      configuration_published: true,
      observed_at: '2026-08-26T00:00:00Z', observation_fresh: true,
    },
    config_path: '/tmp/xtier.json',
    control_addr: '127.0.0.1:19090',
    started_at: '2026-08-26T00:00:00Z',
    idempotency: { scope: 'process_memory', restart_persistent: false, provisional: true },
    control: { command_ingress: 0, command_executions: 0, domain_ingress: 0, domain_executions: 0 },
    configuration: { schema_version: 1, migrated_at_startup: false, last_known_good_revision: 9 },
    rendr: {
      state: 'running', stream_factory: 'xray-stream', stream_carrier: 'unknown',
      mobility_mode: 'redial_attach', endpoint_owned: false, packet_supported: false,
    },
    xray: {
      state: 'running', fail_stopped: false, draining: [], inbounds: [],
      strict_stream_outbound: true, strict_packet_outbound: false,
    },
  };
}

test('config recovery is gated by the structured reconcile error code', () => {
  assert.equal(isConfigContentInvalid({ last_error_code: 'config.content_invalid' }), true);
  assert.equal(isConfigContentInvalid({}), false);
});

test('formats an injection-safe independent CLI equivalent', () => {
  assert.equal(
    formatCommand(renameArgv, { shell: 'powershell', target, revision: 0 }),
    "xtierctl --config 'C:\\state dir\\x''tier$(ignored)\nconfig.json' --control 127.0.0.1:19090 --json --revision 0 local identity rename 'a''b\n$(should-not-run)'",
  );
  assert.equal(
    formatCommand(renameArgv, { shell: 'posix', target, revision: 0 }),
    `xtierctl --config 'C:\\state dir\\x'"'"'tier$(ignored)\nconfig.json' --control 127.0.0.1:19090 --json --revision 0 local identity rename 'a'"'"'b\n$(should-not-run)'`,
  );
});

test('secret values are redacted from CLI equivalents, thrown errors, and journals', async () => {
  const secret = 'browser-secret-never-journal';
  const operation = mutations.xrayProfilePut({
    id: 'secret-profile', kind: 'vless', credential: secret,
    transport: 'tcp', security: 'none', allowInsecurePlaintext: true,
  });
  assert.equal(redactSecretArguments([...operation.cliEquivalent]).includes(secret), false);
  assert.equal(
    formatCommand([...operation.cliEquivalent], { shell: 'posix', target }).includes(secret),
    false,
  );

  const originalFetch = globalThis.fetch;
  resetControlSession();
  clearJournal();
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'config.profile_rejected',
      message: `rejected credential ${secret}`,
      applied: false,
      outcome: 'not_applied',
    }, { status: 400 });
  };

  try {
    await assert.rejects(
      executeMutation(operation, { revision: 0 }),
      (error: unknown) => error instanceof CommandFailure
        && error.code === 'config.profile_rejected'
        && !error.message.includes(secret)
        && !error.detail.includes(secret)
        && error.detail.includes('<redacted>'),
    );
    const journal = JSON.stringify(getJournal());
    assert.equal(journal.includes(secret), false);
    assert.equal(
      formatCommand(getJournal()[0]!.argv, { shell: 'posix', target }).includes(secret),
      false,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a typed mutation sends no CLI envelope and returns the domain object directly', async () => {
  clearJournal();
  resetControlSession();
  const requestId = '0123456789abcdef0123456789abcdef';
  const originalFetch = globalThis.fetch;
  const requests: Array<{ url: string; method: string; body: string }> = [];
  globalThis.fetch = async (input, init) => {
    const url = String(input);
    requests.push({ url, method: init?.method ?? 'GET', body: String(init?.body ?? '') });
    if (url === '/v1/health') {
      return new Response('{"ok":true}', {
        status: 200,
        headers: { 'X-XTier-CSRF-Token': 'csrf-token' },
      });
    }
    return Response.json({
      api_version: 1, ok: true, changed: true, dry_run: false,
      applied: true, outcome: 'applied',
      before_revision: 7, after_revision: 8,
      result: { node: { display_name: renameArgv[3] } },
    });
  };

  try {
    const response = await executeMutation<{ after_revision: number }>(
      mutations.identityRename(renameArgv[3]!),
      { revision: 7, requestId },
    );
    assert.equal(response.after_revision, 8);
    assert.deepEqual(requests.map(({ url, method }) => ({ url, method })), [
      { url: '/v1/health', method: 'GET' },
      { url: '/v1/domain/identity', method: 'PATCH' },
    ]);
    const body = JSON.parse(requests[1]!.body) as Record<string, unknown>;
    assert.deepEqual(body, {
      api_version: 1, revision: 7, dry_run: false, request_id: requestId,
      name: renameArgv[3],
    });
    for (const forbidden of ['args', 'argv', 'json', 'stdout', 'stderr', 'exit_code']) {
      assert.equal(Object.hasOwn(body, forbidden), false, `request contains ${forbidden}`);
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a truncated typed mutation response is unknown and retains its request id', async () => {
  clearJournal();
  resetControlSession();
  const requestId = '1123456789abcdef0123456789abcdef';
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return new Response('{"api_version":1,"ok":true', {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };

  try {
    await assert.rejects(
      executeMutation(mutations.identityRename('A'), { revision: 7, requestId }),
      (error: unknown) => error instanceof TransportFailure && error.outcomeUnknown === true,
    );
    const [entry] = getJournal();
    assert.equal(entry?.requestId, requestId);
    assert.equal(entry?.outcome, 'unknown');
    assert.equal(entry?.errorCode, 'control.response_invalid');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('an indeterminate typed domain result is treated as an unknown transport outcome', async () => {
  clearJournal();
  resetControlSession();
  const requestId = '1923456789abcdef0123456789abcdef';
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'domain.execution_indeterminate',
      message: 'operation failed (domain.execution_indeterminate)',
      applied: false,
      outcome: 'indeterminate',
    }, { status: 500 });
  };

  try {
    await assert.rejects(
      executeMutation(mutations.identityRename('A'), { revision: 7, requestId }),
      (error: unknown) => error instanceof TransportFailure && error.outcomeUnknown === true,
    );
    const [entry] = getJournal();
    assert.equal(entry?.requestId, requestId);
    assert.equal(entry?.outcome, 'unknown');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('a typed 5xx mutation failure without outcome is never reported as not applied', async () => {
  clearJournal();
  resetControlSession();
  const requestId = '2023456789abcdef0123456789abcdef';
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'domain.failed',
      message: 'operation failed (domain.failed)',
    }, { status: 500 });
  };

  try {
    await assert.rejects(
      executeMutation(mutations.identityRename('A'), { revision: 7, requestId }),
      (error: unknown) => error instanceof TransportFailure && error.outcomeUnknown === true,
    );
    const [entry] = getJournal();
    assert.equal(entry?.requestId, requestId);
    assert.equal(entry?.outcome, 'unknown');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('typed error_code is consumed directly and stdout is never parsed', async () => {
  clearJournal();
  resetControlSession();
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'config.revision_conflict',
      message: 'have 8 want 7',
      applied: false,
      outcome: 'not_applied',
      stdout: '{"ok":true,"after_revision":999}',
    }, { status: 409 });
  };

  try {
    await assert.rejects(
      executeMutation(mutations.identityRename('A'), {
        revision: 7,
        requestId: '2123456789abcdef0123456789abcdef',
      }),
      (error: unknown) => error instanceof CommandFailure
        && error.code === 'config.revision_conflict'
        && error.detail === 'have 8 want 7',
    );
    assert.equal(getJournal()[0]?.outcome, 'failed');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('an applied error is journaled as applied_with_error from the outcome tuple', async () => {
  clearJournal();
  resetControlSession();
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'config.commit_visible_and_resynced',
      message: 'the committed configuration was re-read',
      applied: true,
      outcome: 'applied',
    }, { status: 200 });
  };

  try {
    await assert.rejects(
      executeMutation(mutations.identityRename('A'), {
        revision: 7,
        requestId: '2223456789abcdef0123456789abcdef',
      }),
      (error: unknown) => error instanceof CommandFailure
        && error.applied
        && error.outcome === 'applied',
    );
    assert.equal(getJournal()[0]?.outcome, 'applied_with_error');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('response facts are rejected before an untrusted error code is consumed', async () => {
  for (const [index, envelope] of [
    {
      api_version: 2,
      ok: false,
      error_code: 'config.revision_conflict',
      message: 'untrusted version',
      applied: false,
      outcome: 'not_applied',
    },
    {
      api_version: 1,
      ok: false,
      error_code: 'config.revision_conflict',
      message: 'inconsistent tuple',
      applied: false,
      outcome: 'applied',
    },
  ].entries()) {
    clearJournal();
    resetControlSession();
    const originalFetch = globalThis.fetch;
    let calls = 0;
    globalThis.fetch = async () => {
      calls += 1;
      if (calls === 1) {
        return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
      }
      return Response.json(envelope, { status: 409 });
    };
    try {
      await assert.rejects(
        executeMutation(mutations.identityRename('A'), {
          revision: 7,
          requestId: `${index + 23}`.padStart(32, '0'),
        }),
        (error: unknown) => error instanceof TransportFailure
          && error.outcomeUnknown
          && error.message.startsWith('control.response_invalid'),
      );
    } finally {
      globalThis.fetch = originalFetch;
    }
  }
});

test('dry-run success requires matching revision evidence before Apply can be enabled', async () => {
  for (const [index, envelope] of [
    { api_version: 1, ok: true },
    {
      api_version: 1, ok: true, changed: true, dry_run: false,
      before_revision: 7, after_revision: 8,
    },
    {
      api_version: 1, ok: true, changed: true, dry_run: true,
      before_revision: 6, after_revision: 7,
    },
    {
      api_version: 1, ok: true, changed: true, dry_run: true,
      before_revision: 7, after_revision: 7,
    },
    {
      api_version: 1, ok: true, changed: true, dry_run: true,
      before_revision: 7, after_revision: 9,
    },
  ].entries()) {
    clearJournal();
    resetControlSession();
    const originalFetch = globalThis.fetch;
    let calls = 0;
    globalThis.fetch = async () => {
      calls += 1;
      if (calls === 1) {
        return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
      }
      return Response.json(envelope);
    };
    try {
      await assert.rejects(
        executeMutation(mutations.identityRename('A'), {
          revision: 7,
          dryRun: true,
          requestId: `${index + 26}`.padStart(32, '0'),
        }),
        (error: unknown) => error instanceof TransportFailure
          && !error.outcomeUnknown
          && error.message.startsWith('control.response_invalid'),
      );
      assert.notEqual(getJournal()[0]?.outcome, 'ok');
    } finally {
      globalThis.fetch = originalFetch;
    }
  }
});

test('config and reload dry runs accept only their own revision transition', async () => {
  for (const [operation, afterRevision] of [
    [mutations.identityRename('A'), 8],
    [mutations.runtimeReload(), 7],
  ] as const) {
    clearJournal();
    resetControlSession();
    const originalFetch = globalThis.fetch;
    let calls = 0;
    globalThis.fetch = async () => {
      calls += 1;
      if (calls === 1) {
        return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
      }
      return Response.json({
        api_version: 1, ok: true, changed: true, dry_run: true,
        before_revision: 7, after_revision: afterRevision,
      });
    };
    try {
      const result = await executeMutation<{ after_revision: number }>(operation, {
        revision: 7,
        dryRun: true,
      });
      assert.equal(result.after_revision, afterRevision);
    } finally {
      globalThis.fetch = originalFetch;
    }
  }
});

test('an incomplete daemon status is rejected at the API boundary', async () => {
  clearJournal();
  resetControlSession();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ api_version: 1, state: 'running', revision: 0 });
  try {
    await assert.rejects(
      getDaemonStatus(),
      (error: unknown) => error instanceof TransportFailure
        && !error.outcomeUnknown
        && error.message.startsWith('control.response_invalid'),
    );
    assert.equal(getJournal()[0]?.outcome, 'unreachable');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('illegal daemon status values are rejected before screens consume them', async () => {
  const mutations: Array<(status: Record<string, unknown>) => void> = [
    (status) => { status.state = 'bogus'; },
    (status) => { (status.reconcile as Record<string, unknown>).state = 'bogus'; },
    (status) => { (status.reconcile as Record<string, unknown>).configuration_published = false; },
    (status) => { (status.idempotency as Record<string, unknown>).restart_persistent = true; },
    (status) => { (status.rendr as Record<string, unknown>).endpoint_owned = true; },
    (status) => { (status.xray as Record<string, unknown>).inbounds = [{ tag: 'user/socks', state: 'bogus' }]; },
  ];
  const originalFetch = globalThis.fetch;
  try {
    for (const mutate of mutations) {
      clearJournal();
      resetControlSession();
      const status = validDaemonStatus();
      mutate(status);
      globalThis.fetch = async () => Response.json(status);
      await assert.rejects(
        getDaemonStatus(),
        (error: unknown) => error instanceof TransportFailure
          && !error.outcomeUnknown
          && error.message.startsWith('control.response_invalid'),
      );
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('not_applied identity preparations remain structured on CommandFailure', async () => {
  clearJournal();
  resetControlSession();
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
    }
    return Response.json({
      api_version: 1,
      ok: false,
      error_code: 'config.backup_prune',
      message: 'configuration was not committed',
      applied: false,
      outcome: 'not_applied',
      preparations: [{ kind: 'identity_backing', state: 'recoverable', node_id: 'node-a' }],
    }, { status: 422 });
  };
  try {
    await assert.rejects(
      executeMutation(mutations.identityInit('A'), {
        revision: 0,
        requestId: '2523456789abcdef0123456789abcdef',
      }),
      (error: unknown) => error instanceof CommandFailure
        && error.outcome === 'not_applied'
        && error.preparations[0]?.kind === 'identity_backing'
        && error.preparations[0]?.state === 'recoverable',
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

interface MappingCase {
  name: string;
  path: string;
  method: string;
  expected?: Record<string, unknown>;
  mutating: boolean;
  invoke: (requestId: string) => Promise<unknown>;
}

const mutationCase = (
  name: string,
  path: string,
  method: string,
  operation: DomainMutation,
  expected: Record<string, unknown>,
): MappingCase => ({
  name, path, method, expected, mutating: true,
  invoke: (requestId) => executeMutation(operation, { revision: 9, requestId }),
});

const mappings: MappingCase[] = [
  { name: 'daemon status', path: '/v1/status', method: 'GET', mutating: false, invoke: () => getDaemonStatus() },
  { name: 'local status', path: '/v1/domain/local', method: 'GET', mutating: false, invoke: () => getLocalStatus() },
  { name: 'settings read', path: '/v1/domain/settings', method: 'GET', mutating: false, invoke: () => getSettings() },
  { name: 'peers read', path: '/v1/domain/peers', method: 'GET', mutating: false, invoke: () => getPeers() },
  { name: 'inbounds read', path: '/v1/domain/inbounds', method: 'GET', mutating: false, invoke: () => getInbounds() },
  { name: 'profiles read', path: '/v1/domain/xray-profiles', method: 'GET', mutating: false, invoke: () => getXrayProfiles() },
  mutationCase('identity init', '/v1/domain/identity/init', 'POST', mutations.identityInit('A'), { name: 'A' }),
  mutationCase('identity rename', '/v1/domain/identity', 'PATCH', mutations.identityRename('A'), { name: 'A' }),
  mutationCase('settings update', '/v1/domain/settings', 'PATCH', mutations.settingsUpdate({ max_fetch_fan_out: 12 }), { settings: { max_fetch_fan_out: 12 } }),
  mutationCase('inbound put', '/v1/domain/inbounds', 'PUT', mutations.inboundPut({ kind: 'socks', listen: '127.0.0.1:1080', xrayProfileId: 'socks', exitPeer: 'B' }), { kind: 'socks', listen: '127.0.0.1:1080', xray_profile_id: 'socks', exit_peer: 'B' }),
  mutationCase('inbound state', '/v1/domain/inbounds/state', 'PATCH', mutations.inboundState('socks', false, 'maintenance'), { kind: 'socks', enabled: false, reason: 'maintenance' }),
  mutationCase('peer add', '/v1/domain/peers', 'POST', mutations.peerCreate({ name: 'B', nodeId: 'node-b', addr: '10.0.0.2:443', direction: 'outbound', xrayProfileId: 'vless', nestedEnabled: true }), { name: 'B', node_id: 'node-b', addr: '10.0.0.2:443', direction: 'outbound', xray_profile_id: 'vless', nested_enabled: true }),
  mutationCase('peer patch', '/v1/domain/peers', 'PATCH', mutations.peerUpdate('B', { nestedEnabled: false, addr: '10.0.0.3:443' }), { name: 'B', patch: { nested_enabled: false, addr: '10.0.0.3:443' } }),
  mutationCase('peer state', '/v1/domain/peers/state', 'PATCH', mutations.peerState('B', true), { name: 'B', enabled: true, reason: '' }),
  mutationCase('peer remove', '/v1/domain/peers', 'DELETE', mutations.peerRemove('B'), { name: 'B' }),
  mutationCase('profile put', '/v1/domain/xray-profiles', 'PUT', mutations.xrayProfilePut({ id: 'vless', kind: 'vless', credential: 'browser-secret', transport: 'tcp', security: 'none', allowInsecurePlaintext: true }), { id: 'vless', kind: 'vless', credential: 'browser-secret', transport: 'tcp', security: 'none', allow_insecure_plaintext: true }),
  mutationCase('profile remove', '/v1/domain/xray-profiles', 'DELETE', mutations.xrayProfileRemove('vless'), { id: 'vless' }),
  { name: 'profile validate', path: '/v1/domain/xray-profiles/validate', method: 'POST', expected: { api_version: 1, id: 'vless' }, mutating: false, invoke: () => validateXrayProfile('vless') },
  { name: 'path compile', path: '/v1/domain/paths/compile', method: 'POST', expected: { api_version: 1, expression: 'node-b', strategy: 'race', endpoint_kind: 'egress' }, mutating: false, invoke: () => compilePath('node-b', 'race', 'egress') },
  mutationCase('runtime reload', '/v1/domain/runtime/reload', 'POST', mutations.runtimeReload(), {}),
  mutationCase('config restore last good', '/v1/domain/config/restore-last-good', 'POST', mutations.configRestoreLastGood(), {}),
];

test('all screen operations call versioned domain routes without a CLI transport', async () => {
  const originalFetch = globalThis.fetch;
  try {
    for (const [index, mapping] of mappings.entries()) {
      await test(mapping.name, async () => {
        clearJournal();
        resetControlSession();
        const requests: Array<{ url: string; method: string; body: string }> = [];
        globalThis.fetch = async (input, init) => {
          const url = String(input);
          requests.push({ url, method: init?.method ?? 'GET', body: String(init?.body ?? '') });
          if (url === '/v1/health') {
            return new Response('', { headers: { 'X-XTier-CSRF-Token': 'csrf-token' } });
          }
          if (mapping.name === 'daemon status') {
            return Response.json(validDaemonStatus());
          }
          return Response.json({
            api_version: 1,
            ok: true,
            ...(mapping.mutating ? {
              changed: true,
              dry_run: false,
              before_revision: 9,
              after_revision: mapping.name === 'runtime reload' ? 9 : 10,
              applied: true,
              outcome: 'applied',
            } : {}),
          });
        };

        const requestId = index.toString(16).padStart(32, '0');
        await mapping.invoke(requestId);
        if (mapping.name === 'profile put') {
          const entry = getJournal()[0]!;
          assert.equal(JSON.stringify(entry).includes('browser-secret'), false);
          assert.equal(formatCommand(entry.argv, { shell: 'posix', target }).includes('browser-secret'), false);
        }
        assert.equal(requests.some((request) => request.url === '/v1/command'), false);
        const request = requests.at(-1)!;
        assert.equal(request.url, mapping.path);
        assert.equal(request.method, mapping.method);
        if (mapping.method === 'GET') {
          assert.equal(request.body, '');
          return;
        }
        const body = JSON.parse(request.body) as Record<string, unknown>;
        for (const forbidden of ['args', 'argv', 'json', 'stdout', 'stderr', 'exit_code']) {
          assert.equal(Object.hasOwn(body, forbidden), false, `${mapping.name} contains ${forbidden}`);
        }
        const common = mapping.mutating
          ? { api_version: 1, revision: 9, dry_run: false, request_id: requestId }
          : {};
        assert.deepEqual(body, { ...common, ...(mapping.expected ?? {}) });
      });
    }
  } finally {
    globalThis.fetch = originalFetch;
    resetControlSession();
  }
});

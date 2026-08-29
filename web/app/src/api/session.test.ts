/* ===========================================================================
 * SIGN-IN TRANSPORT
 *
 * The properties worth pinning down here are the ones that are invisible when
 * they break: a 200 that admits someone it should not, a 401 that is read as a
 * fault instead of an answer, a credential that survives in a failure message.
 * None of those show up as a broken screen — they show up as a panel that is
 * quietly wrong about who is holding it.
 * ======================================================================== */
import assert from 'node:assert/strict';
import test from 'node:test';
import {
  activateControlSession,
  currentCSRFToken,
  getLocalStatus,
  onControlSessionLost,
  resetControlSession,
} from './control.ts';
import {
  createSession,
  destroySession,
  isSessionFailure,
  parseRetryAfter,
  readSession,
} from './session.ts';
import {
  clearSessionLogoutPending,
  markSessionLogoutPending,
  SESSION_LOGOUT_PENDING_STORAGE_KEY,
  SESSION_PROOF_STORAGE_KEY,
  sessionLogoutPending,
} from './sessionProof.ts';

const SECRET = 'operator-secret-never-echoed';

class MemoryStorage implements Storage {
  readonly #values = new Map<string, string>();
  failSetKey: string | null = null;

  get length() { return this.#values.size; }
  clear() { this.#values.clear(); }
  getItem(key: string) { return this.#values.get(key) ?? null; }
  key(index: number) { return [...this.#values.keys()][index] ?? null; }
  removeItem(key: string) { this.#values.delete(key); }
  setItem(key: string, value: string) {
    if (key === this.failSetKey) throw new Error(`setItem refused for ${key}`);
    this.#values.set(key, String(value));
  }
}

const sessionStore = new MemoryStorage();
const localStore = new MemoryStorage();
Object.defineProperty(globalThis, 'sessionStorage', {
  configurable: true,
  value: sessionStore,
});
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: localStore,
});

interface Call {
  url: string;
  method: string;
  body: string;
  headers: Headers;
  signal: AbortSignal | null;
}

/**
 * Swaps `globalThis.fetch` for the duration of one test and restores it, so a
 * failing assertion cannot leak a stub into the next file.
 */
async function withFetch(
  handler: (call: Call) => Response | Promise<Response>,
  run: (calls: Call[]) => Promise<void>,
  proof: string | null = null,
): Promise<void> {
  const original = globalThis.fetch;
  const calls: Call[] = [];
  sessionStore.clear();
  sessionStore.failSetKey = null;
  localStore.clear();
  resetControlSession();
  clearSessionLogoutPending();
  if (proof) assert.equal(activateControlSession(activeSession(proof)), true);
  globalThis.fetch = async (input, init) => {
    const call: Call = {
      url: String(input),
      method: init?.method ?? 'GET',
      body: String(init?.body ?? ''),
      headers: new Headers(init?.headers ?? {}),
      signal: init?.signal ?? null,
    };
    calls.push(call);
    return handler(call);
  };
  try {
    await run(calls);
  } finally {
    globalThis.fetch = original;
    sessionStore.failSetKey = null;
    resetControlSession();
    clearSessionLogoutPending();
    sessionStore.clear();
    localStore.clear();
  }
}

function activeSession(token = 'csrf-token'): Response {
  return Response.json({ api_version: 1, authenticated: true }, {
    status: 200,
    headers: { 'X-XTier-CSRF-Token': token },
  });
}

function sessionError(code: string, status: number, message = 'Request refused.'): Response {
  return Response.json({ api_version: 1, ok: false, error_code: code, message }, { status });
}

/* -- Reading the session -------------------------------------------------- */

test('a missing local proof is anonymous and never replays the cookie', async () => {
  await withFetch(
    () => {
      throw new Error('a cookie-only probe must not reach the bridge');
    },
    async (calls) => {
      const probe = await readSession();
      assert.equal(probe.kind, 'anonymous');
      assert.equal(isSessionFailure(probe), false);
      assert.equal(calls.length, 0);
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), null);
    },
  );
});

test('a typed 401 clears a stored proof and becomes anonymous', async () => {
  await withFetch(
    () => sessionError('webbridge.session_invalid', 401, 'Sign in to continue.'),
    async (calls) => {
      const probe = await readSession();
      assert.equal(probe.kind, 'anonymous');
      assert.equal(calls[0]?.headers.get('X-XTier-CSRF-Token'), 'stored-proof');
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), null);
    },
    'stored-proof',
  );
});

test('a typed CSRF refusal clears a split cookie/proof pair and becomes anonymous', async () => {
  await withFetch(
    () => sessionError('webbridge.csrf_invalid', 403, 'The session check failed.'),
    async (calls) => {
      const probe = await readSession();
      assert.equal(probe.kind, 'anonymous');
      assert.equal(calls[0]?.headers.get('X-XTier-CSRF-Token'), 'stored-proof');
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), null);
    },
    'stored-proof',
  );
});

test('a probe distinguishes an unreachable bridge from a refusing one', async () => {
  await withFetch(
    () => {
      throw new TypeError('Failed to fetch');
    },
    async () => {
      const probe = await readSession();
      assert.equal(probe.kind, 'unreachable');
      assert.equal(probe.kind === 'unreachable' && probe.code, 'control.unreachable');
    },
    'stored-proof',
  );

  await withFetch(
    () => new Response('upstream down', { status: 502 }),
    async () => {
      const probe = await readSession();
      // A 5xx with no typed error is the bridge failing to reach the daemon,
      // not the daemon refusing anything.
      assert.equal(probe.kind, 'unreachable');
    },
    'stored-proof',
  );
});

test('a 200 that does not assert authentication admits nobody', async () => {
  // The exact shape a static index.html rewrite produces: HTTP 200, HTML body.
  await withFetch(
    () => new Response('<!doctype html><title>x-tier</title>', { status: 200 }),
    async () => {
      const probe = await readSession();
      assert.equal(probe.kind, 'malformed');
      assert.equal(probe.kind === 'malformed' && probe.code, 'control.response_invalid');
    },
    'stored-proof',
  );

  // Right protocol, wrong verdict: `authenticated` absent means not signed in,
  // and a 200 saying so is the bridge contradicting itself.
  await withFetch(
    () => Response.json({ api_version: 1 }, { status: 200 }),
    async () => {
      assert.equal((await readSession()).kind, 'malformed');
    },
    'stored-proof',
  );

  // A future bridge speaking a protocol this panel has not been taught.
  await withFetch(
    () => Response.json({ api_version: 2, authenticated: true }, { status: 200 }),
    async () => {
      assert.equal((await readSession()).kind, 'malformed');
    },
    'stored-proof',
  );

  await withFetch(
    () => activeSession(),
    async () => {
      assert.equal((await readSession()).kind, 'authenticated');
    },
    'stored-proof',
  );
});

/* -- Offering a credential ------------------------------------------------ */

test('the credential is sent once, in the body, and never comes back out', async () => {
  await withFetch(
    () =>
      Response.json(
        {
          api_version: 1,
          ok: false,
          error_code: 'webbridge.credential_invalid',
          // A bridge that echoes its input. The panel must not repeat this.
          message: `rejected credential ${SECRET}`,
        },
        { status: 401 },
      ),
    async (calls) => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'rejected');
      assert.equal(outcome.kind === 'rejected' && outcome.code, 'webbridge.credential_invalid');
      assert.equal(outcome.kind === 'rejected' && outcome.detail.includes(SECRET), false);

      assert.equal(calls.length, 1);
      assert.equal(calls[0]!.method, 'POST');
      assert.equal(calls[0]!.url, '/v1/web/session');
      assert.deepEqual(JSON.parse(calls[0]!.body), { credential: SECRET });
      // Never in the query string, where it would reach history and access logs.
      assert.equal(calls[0]!.url.includes(SECRET), false);
    },
  );
});

test('a failure the panel classifies itself never quotes the response body', async () => {
  // 400 rather than 401, so the reading goes through `failureFor` — the path
  // that has no bridge code to trust and would be tempted to quote the body.
  await withFetch(
    () => new Response(`bad request: credential=${SECRET}`, { status: 400 }),
    async () => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'refused');
      assert.equal(
        outcome.kind === 'refused' && outcome.detail.includes(SECRET),
        false,
        'the credential must not reach a message the panel renders',
      );
    },
  );
});

test('sign-in rejects a bare 401 that is not the session protocol', async () => {
  await withFetch(
    () => Response.json({ error: 'webbridge.credential_invalid' }, { status: 401 }),
    async () => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'malformed');
    },
  );
});

test('rate limiting is its own outcome and carries the wait when one is given', async () => {
  await withFetch(
    () =>
      Response.json(
        { api_version: 1, ok: false, error_code: 'webbridge.rate_limited', message: 'Too many attempts.' },
        { status: 429, headers: { 'retry-after': '45' } },
      ),
    async () => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'rate_limited');
      assert.equal(outcome.kind === 'rate_limited' && outcome.retryAfterSeconds, 45);
    },
  );

  // A 429 that is not the typed protocol is not accepted as a lockout verdict.
  await withFetch(
    () => new Response('', { status: 429 }),
    async () => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'malformed');
    },
  );
});

test('a 200 without the authenticated assertion does not sign anyone in', async () => {
  await withFetch(
    () => new Response('<!doctype html>', { status: 200 }),
    async () => {
      assert.equal((await createSession(SECRET)).kind, 'malformed');
    },
  );
});

test('sign-in persists proof only in sessionStorage and reload probes present it', async () => {
  await withFetch(
    () => activeSession('login-proof'),
    async (calls) => {
      assert.equal((await createSession(SECRET)).kind, 'authenticated');
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), 'login-proof');
      assert.equal(localStore.length, 0);

      assert.equal((await readSession()).kind, 'authenticated');
      assert.equal(calls[1]?.method, 'GET');
      assert.equal(calls[1]?.headers.get('X-XTier-CSRF-Token'), 'login-proof');
    },
  );
});

test('typed 409 session_changed uses local detail and never echoes the credential', async () => {
  await withFetch(
    () => sessionError(
      'webbridge.session_changed',
      409,
      `another login raced with ${SECRET}`,
    ),
    async () => {
      const outcome = await createSession(SECRET);
      assert.equal(outcome.kind, 'refused');
      assert.equal(outcome.kind === 'refused' && outcome.code, 'webbridge.session_changed');
      assert.equal(outcome.kind === 'refused' && outcome.detail.includes(SECRET), false);
      assert.equal(currentCSRFToken(), null);
    },
    'old-proof',
  );
});

/* -- Ending the session --------------------------------------------------- */

test('sign-out carries proof and retains it when the bridge refuses', async () => {
  await withFetch(
    (call) => {
      if (call.method === 'GET') return activeSession();
      return sessionError('webbridge.credential_unavailable', 503);
    },
    async (calls) => {
      assert.equal((await readSession()).kind, 'authenticated');
      const outcome = await destroySession();
      assert.equal(outcome.kind, 'refused');

      const del = calls.find((c) => c.method === 'DELETE');
      assert.ok(del, 'a DELETE was issued');
      assert.equal(del!.headers.get('X-XTier-CSRF-Token'), 'csrf-token');
      assert.equal(currentCSRFToken(), 'csrf-token');
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), 'csrf-token');
      assert.equal(sessionLogoutPending(), true);
      assert.equal(sessionStore.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY), '1');
    },
    'stored-proof',
  );
});

test('sign-out treats an already-dead session as success', async () => {
  await withFetch(
    (call) => call.method === 'GET'
      ? activeSession()
      : sessionError('webbridge.session_invalid', 401),
    async () => {
      assert.equal((await readSession()).kind, 'authenticated');
      assert.equal((await destroySession()).kind, 'ended');
      assert.equal(currentCSRFToken(), null);
      assert.equal(sessionLogoutPending(), false);
      assert.equal(sessionStore.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY), null);
    },
    'stored-proof',
  );
});

test('a terminal CSRF logout refusal ends only this tab without retrying', async () => {
  let deletes = 0;
  await withFetch(
    (call) => {
      if (call.method === 'GET') return activeSession();
      deletes += 1;
      return sessionError('webbridge.csrf_invalid', 403);
    },
    async () => {
      assert.equal((await readSession()).kind, 'authenticated');
      const outcome = await destroySession();
      assert.equal(outcome.kind, 'ended');
      assert.equal(deletes, 1);
      assert.equal(currentCSRFToken(), null);
      assert.equal(sessionLogoutPending(), false);
      assert.equal(sessionStore.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY), null);
    },
    'stored-proof',
  );

  await withFetch(
    (call) => call.method === 'GET'
      ? activeSession()
      : Response.json({ api_version: 1, authenticated: false }),
    async () => {
      assert.equal((await readSession()).kind, 'authenticated');
      assert.equal((await destroySession()).kind, 'ended');
    },
    'stored-proof',
  );
});

test('a 403 that is not about CSRF is reported rather than retried', async () => {
  let deletes = 0;
  await withFetch(
    (call) => {
      if (call.method === 'GET') return activeSession();
      deletes += 1;
      return sessionError('webbridge.origin_forbidden', 403);
    },
    async () => {
      assert.equal((await readSession()).kind, 'authenticated');
      const outcome = await destroySession();
      assert.equal(outcome.kind, 'refused');
      assert.equal(outcome.kind === 'refused' && outcome.code, 'webbridge.origin_forbidden');
      assert.equal(deletes, 1);
    },
    'stored-proof',
  );
});

test('failed sign-out can be retried with the same proof until confirmed', async () => {
  let deletes = 0;
  await withFetch(
    () => {
      deletes += 1;
      return deletes === 1
        ? sessionError('webbridge.session_unavailable', 503)
        : Response.json({ api_version: 1, authenticated: false });
    },
    async () => {
      assert.equal((await destroySession()).kind, 'refused');
      assert.equal(currentCSRFToken(), 'retry-proof');
      assert.equal(sessionLogoutPending(), true);
      assert.equal((await destroySession()).kind, 'ended');
      assert.equal(currentCSRFToken(), null);
      assert.equal(sessionLogoutPending(), false);
      assert.equal(deletes, 2);
    },
    'retry-proof',
  );
});

test('an aborted sign-out retains proof for an explicit retry', async () => {
  await withFetch(
    (call) => new Promise<Response>((_resolve, reject) => {
      call.signal?.addEventListener('abort', () => reject(new Error('sign-out aborted')), {
        once: true,
      });
    }),
    async () => {
      const controller = new AbortController();
      const pending = destroySession(controller.signal);
      controller.abort();
      assert.equal((await pending).kind, 'unreachable');
      assert.equal(currentCSRFToken(), 'timeout-proof');
      assert.equal(sessionLogoutPending(), true);
    },
    'timeout-proof',
  );
});

test('a pending sign-out marker survives a page-module reload', async () => {
  await withFetch(
    () => sessionError('webbridge.session_unavailable', 503),
    async () => {
      assert.equal((await destroySession()).kind, 'refused');
      const reloaded = await import(`./sessionProof.ts?logout-reload=${Date.now()}`);
      assert.equal(reloaded.sessionLogoutPending(), true);
      assert.equal(reloaded.currentSessionAuthority()?.proof, 'reload-proof');
    },
    'reload-proof',
  );
});

test('a pending-marker write failure removes proof recoverable by a reload', async () => {
  await withFetch(
    () => sessionError('webbridge.session_unavailable', 503),
    async () => {
      sessionStore.failSetKey = SESSION_LOGOUT_PENDING_STORAGE_KEY;
      assert.equal((await destroySession()).kind, 'refused');
      assert.equal(currentCSRFToken(), 'storage-failure-proof');
      assert.equal(sessionLogoutPending(), true);
      assert.equal(sessionStore.getItem(SESSION_PROOF_STORAGE_KEY), null);

      const reloaded = await import(`./sessionProof.ts?logout-storage-failure=${Date.now()}`);
      assert.equal(reloaded.sessionLogoutPending(), false);
      assert.equal(reloaded.currentSessionAuthority(), null);
    },
    'storage-failure-proof',
  );
});

test('a successful sign-in supersedes a stale pending sign-out intent', async () => {
  await withFetch(
    () => activeSession('new-login-proof'),
    async () => {
      assert.equal(markSessionLogoutPending(), true);
      assert.equal(sessionLogoutPending(), true);
      assert.equal((await createSession(SECRET)).kind, 'authenticated');
      assert.equal(sessionLogoutPending(), false);
      assert.equal(sessionStore.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY), null);
    },
    'old-proof',
  );
});

test('delayed session GET cannot clear a newer generation', async () => {
  let resolveOld: ((response: Response) => void) | undefined;
  await withFetch(
    () => new Promise<Response>((resolve) => {
      resolveOld = resolve;
    }),
    async (calls) => {
      const pending = readSession();
      assert.equal(calls[0]?.headers.get('X-XTier-CSRF-Token'), 'old-proof');
      assert.equal(activateControlSession(activeSession('new-proof')), true);
      resolveOld!(sessionError('webbridge.session_invalid', 401));
      assert.equal((await pending).kind, 'superseded');
      assert.equal(currentCSRFToken(), 'new-proof');
    },
    'old-proof',
  );
});

test('delayed login success cannot replace a newer generation', async () => {
  let resolveOld: ((response: Response) => void) | undefined;
  await withFetch(
    () => new Promise<Response>((resolve) => {
      resolveOld = resolve;
    }),
    async () => {
      const pending = createSession(SECRET);
      assert.equal(activateControlSession(activeSession('new-proof')), true);
      resolveOld!(activeSession('stale-login-proof'));
      assert.equal((await pending).kind, 'superseded');
      assert.equal(currentCSRFToken(), 'new-proof');
    },
  );
});

test('delayed sign-out success cannot clear a newer generation', async () => {
  let resolveOld: ((response: Response) => void) | undefined;
  await withFetch(
    () => new Promise<Response>((resolve) => {
      resolveOld = resolve;
    }),
    async () => {
      const pending = destroySession();
      assert.equal(activateControlSession(activeSession('new-proof')), true);
      resolveOld!(Response.json({ api_version: 1, authenticated: false }));
      assert.equal((await pending).kind, 'superseded');
      assert.equal(currentCSRFToken(), 'new-proof');
    },
    'old-proof',
  );
});

test('a delayed 401 from an old generation cannot evict a new session', async () => {
  const original = globalThis.fetch;
  resetControlSession();
  assert.equal(activateControlSession(activeSession('old-token')), true);
  let resolveOld: ((response: Response) => void) | undefined;
  globalThis.fetch = () => new Promise<Response>((resolve) => {
    resolveOld = resolve;
  });
  let fired = 0;
  const stop = onControlSessionLost(() => {
    fired += 1;
  });

  try {
    const oldRead = getLocalStatus();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.ok(resolveOld, 'old request did not start');
    assert.equal(activateControlSession(activeSession('new-token')), true);
    resolveOld!(new Response(
      JSON.stringify({
        api_version: 1,
        ok: false,
        error_code: 'webbridge.session_invalid',
        message: 'Sign in to continue.',
      }),
      { status: 401, headers: { 'X-XTier-CSRF-Token': 'stale-token' } },
    ));
    await assert.rejects(oldRead);
    assert.equal(fired, 0);
    assert.equal(currentCSRFToken(), 'new-token');
  } finally {
    stop();
    globalThis.fetch = original;
    resetControlSession();
  }
});

/* -- Sessions that end on their own --------------------------------------- */

test('a probe does not raise the session-lost signal for its own 401', async () => {
  let fired = 0;
  const stop = onControlSessionLost(() => {
    fired += 1;
  });
  try {
    await withFetch(
      () => new Response('', { status: 401 }),
      async () => {
        await readSession();
        assert.equal(fired, 0, 'the gate asked the question; it does not need telling twice');
      },
      'stored-proof',
    );
  } finally {
    stop();
  }
});

/* -- Retry-After ---------------------------------------------------------- */

test('Retry-After is read in both of its legal forms', () => {
  assert.equal(parseRetryAfter(null), null);
  assert.equal(parseRetryAfter('  '), null);
  assert.equal(parseRetryAfter('30'), 30);
  assert.equal(parseRetryAfter('0'), 0);
  assert.equal(parseRetryAfter('not-a-date'), null);
  // A date already past is not a wait.
  assert.equal(parseRetryAfter('Wed, 21 Oct 2015 07:28:00 GMT'), null);

  const soon = new Date(Date.now() + 60_000).toUTCString();
  const seconds = parseRetryAfter(soon);
  assert.ok(seconds !== null && seconds > 50 && seconds <= 61, `got ${seconds}`);
});

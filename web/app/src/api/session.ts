/* ===========================================================================
 * THE WEB SESSION
 *
 * Three calls against one route:
 *
 *   GET    /v1/web/session   is the operator signed in?
 *   POST   /v1/web/session   sign in with a credential
 *   DELETE /v1/web/session   sign out
 *
 * This deliberately does not go through `fetchJSON`. That function serves the
 * domain API, where every answer is a typed envelope and any HTTP failure that
 * is not one is a broken daemon. Authentication does not fit: a 401 is a
 * perfectly good answer to "am I signed in?", and treating it as a malformed
 * response would turn the ordinary anonymous state into an alarm.
 *
 * The same status also means two different things depending on the verb. On
 * GET, 401 is "not signed in" — expected, unremarkable, the state every fresh
 * browser starts in. On POST, 401 is "that credential is wrong" — a verdict on
 * something the operator just typed. Only the caller's own verb disambiguates
 * them, so classification lives here rather than in shared transport code.
 *
 * ON THE CREDENTIAL
 *
 * It is passed in, serialized, sent, and dropped. It is never stored in a
 * module variable, never logged, never journalled, and never included in a
 * failure detail. Nothing in this file retains it after the call returns.
 *
 * Login requests are also absent from the diagnostics journal on purpose. The
 * journal's contract is a redacted CLI-equivalent argv, and signing into the
 * web bridge has no CLI equivalent — there is no command to show. Inventing an
 * argv for the display would be a lie about a screen whose whole value is that
 * it is not.
 * ======================================================================== */

import {
  API_VERSION,
  CSRF_HEADER,
  activateControlSession,
  controlSessionGeneration,
  controlSessionGenerationMatches,
  currentControlSession,
  resetControlSession,
} from './control.ts';
import {
  clearSessionLogoutPending,
  markSessionLogoutPending,
  sessionLogoutPending,
} from './sessionProof.ts';

export const SESSION_ROUTE = '/v1/web/session';

/* -- Outcomes --------------------------------------------------------------
 * Every failure carries a `code` that the error catalogue can advise on, so
 * the login screen presents them through the same FailureNotice the rest of
 * the panel uses rather than growing a private vocabulary of its own.
 *
 * The three failure kinds are kept apart because the operator's next move
 * differs: `unreachable` means nothing answered and the thing to do is wait;
 * `refused` means the bridge answered and said no for a nameable reason; and
 * `malformed` means something answered but not with the protocol, which is a
 * bug rather than a condition. Collapsing them would be easier and would lose
 * exactly the distinction the operator needs.
 * ----------------------------------------------------------------------- */

export interface SessionFailureBase {
  readonly code: string;
  readonly detail: string;
}

export type SessionFailure =
  | (SessionFailureBase & { readonly kind: 'unreachable' })
  | (SessionFailureBase & { readonly kind: 'refused' })
  | (SessionFailureBase & { readonly kind: 'malformed' });

export interface SessionSuperseded {
  readonly kind: 'superseded';
}

/** The answer to "is this browser signed in?". */
export type SessionProbe =
  | { readonly kind: 'authenticated' }
  | { readonly kind: 'anonymous' }
  | SessionSuperseded
  | SessionFailure;

/** The answer to "is this credential accepted?". */
export type LoginOutcome =
  | { readonly kind: 'authenticated' }
  | SessionSuperseded
  | (SessionFailureBase & { readonly kind: 'rejected' })
  | (SessionFailureBase & {
      readonly kind: 'rate_limited';
      /** Seconds to wait, when the bridge said. */
      readonly retryAfterSeconds: number | null;
    })
  | SessionFailure;

/** The result of signing out. `ended` covers "was already signed out". */
export type LogoutOutcome = { readonly kind: 'ended' } | SessionSuperseded | SessionFailure;

export function isSessionFailure(
  value: SessionProbe | LoginOutcome | LogoutOutcome,
): value is SessionFailure {
  return value.kind === 'unreachable' || value.kind === 'refused' || value.kind === 'malformed';
}

export function isSessionSuperseded(
  value: SessionProbe | LoginOutcome | LogoutOutcome,
): value is SessionSuperseded {
  return value.kind === 'superseded';
}

/* -- Reading the bridge's answers ---------------------------------------- */

interface SessionBody {
  api_version?: unknown;
  authenticated?: unknown;
  ok?: unknown;
  error_code?: unknown;
  message?: unknown;
}

function decode(text: string): SessionBody | null {
  if (!text.trim()) return null;
  try {
    const parsed: unknown = JSON.parse(text);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;
    return parsed as SessionBody;
  } catch {
    return null;
  }
}

function typedErrorCode(body: SessionBody | null): string | null {
  if (!body || body.api_version !== API_VERSION || body.ok !== false
    || typeof body.error_code !== 'string' || !body.error_code
    || typeof body.message !== 'string' || !body.message) return null;
  return body.error_code;
}

/**
 * Classifies an HTTP failure the caller has no specific reading for.
 *
 * The response body is never quoted back. On a login attempt the request body
 * held the credential, and a bridge that echoed its input into an error message
 * would put the secret straight into the panel's own UI. The status and the
 * typed code are enough to act on. The server's message is deliberately never
 * displayed because a compromised or buggy bridge could reflect the submitted
 * credential there.
 */
function failureFor(response: Response, body: SessionBody | null): SessionFailure {
  const code = typedErrorCode(body);
  if (code) {
    return { kind: 'refused', code, detail: `The web bridge refused the request (HTTP ${response.status}).` };
  }
  if (response.status >= 500) {
    return {
      kind: 'unreachable',
      code: 'control.unreachable',
      detail: `the web bridge answered HTTP ${response.status}`,
    };
  }
  return {
    kind: 'refused',
    code: 'control.http_status',
    detail: `HTTP ${response.status} without a valid typed error`,
  };
}

function unreachable(error: unknown): SessionFailure {
  return {
    kind: 'unreachable',
    code: 'control.unreachable',
    detail: error instanceof Error ? error.message : String(error),
  };
}

function malformed(detail: string): SessionFailure {
  return { kind: 'malformed', code: 'control.response_invalid', detail };
}

/**
 * Validates a 200 that claims a session.
 *
 * A 200 alone is not enough to admit anyone. If the body is not the protocol —
 * wrong `api_version`, missing `authenticated`, or a static index.html served
 * where JSON was expected — then whatever answered is not the control plane,
 * and letting the operator through on its say-so would open the panel on the
 * strength of a response nobody has authenticated.
 */
function readAuthenticated(
  response: Response,
  body: SessionBody | null,
  text: string,
  expectedGeneration: number,
  advanceGeneration = false,
): { readonly kind: 'authenticated' } | SessionFailure {
  if (!body) {
    return malformed(text.trim() ? 'response body is not JSON' : 'empty response body');
  }
  if (body.api_version !== API_VERSION) {
    return malformed(`unsupported api_version: ${JSON.stringify(body.api_version) ?? 'absent'}`);
  }
  if (body.authenticated !== true) {
    return malformed('HTTP 200 without "authenticated": true');
  }
  if (!activateControlSession(response, expectedGeneration, advanceGeneration)) {
    return malformed(`HTTP 200 without ${CSRF_HEADER}`);
  }
  return { kind: 'authenticated' };
}

function hasTypedError(body: SessionBody | null, expected: string): boolean {
  return typedErrorCode(body) === expected;
}

function readEnded(body: SessionBody | null, text: string): LogoutOutcome {
  if (!body) return malformed(text.trim() ? 'response body is not JSON' : 'empty response body');
  if (body.api_version !== API_VERSION) {
    return malformed(`unsupported api_version: ${JSON.stringify(body.api_version) ?? 'absent'}`);
  }
  if (body.authenticated !== false) {
    return malformed('HTTP 200 without "authenticated": false');
  }
  return { kind: 'ended' };
}

/* -- The three calls ------------------------------------------------------ */

/**
 * Asks whether this browser already holds a session.
 *
 * A 401 here resolves to `anonymous` and does NOT raise the session-lost
 * signal. The caller is the gate, which is already asking the question; firing
 * the notification at it would be an answer arriving twice.
 */
export async function readSession(signal?: AbortSignal): Promise<SessionProbe> {
  const authority = currentControlSession();
  if (!authority) {
    resetControlSession(controlSessionGeneration());
    return { kind: 'anonymous' };
  }

  let response: Response;
  try {
    response = await fetch(SESSION_ROUTE, {
      credentials: 'same-origin',
      headers: { accept: 'application/json', [CSRF_HEADER]: authority.proof },
      signal,
    });
  } catch (error) {
    return unreachable(error);
  }

  const text = await response.text().catch(() => '');
  if (!controlSessionGenerationMatches(authority.generation)) return { kind: 'superseded' };
  const body = decode(text);

  if (response.status === 401) {
    if (!hasTypedError(body, 'webbridge.session_invalid')) {
      return malformed('HTTP 401 without webbridge.session_invalid');
    }
    if (!resetControlSession(authority.generation)) return { kind: 'superseded' };
    return { kind: 'anonymous' };
  }
  if (response.status === 403 && hasTypedError(body, 'webbridge.csrf_invalid')) {
    if (!resetControlSession(authority.generation)) return { kind: 'superseded' };
    return { kind: 'anonymous' };
  }
  if (!response.ok) return failureFor(response, body);
  return readAuthenticated(response, body, text, authority.generation);
}

/**
 * Offers a credential.
 *
 * The CSRF header rides along only when a previous response happened to issue a
 * token. Sign-in cannot require one — there is no session yet to mint it
 * against — but sending a held token costs nothing and satisfies a bridge that
 * checks the header on every mutation.
 */
export async function createSession(
  credential: string,
  signal?: AbortSignal,
): Promise<LoginOutcome> {
  const expectedGeneration = controlSessionGeneration();
  const token = currentControlSession()?.proof;
  let response: Response;
  try {
    response = await fetch(SESSION_ROUTE, {
      method: 'POST',
      credentials: 'same-origin',
      redirect: 'error',
      headers: {
        'content-type': 'application/json',
        accept: 'application/json',
        ...(token ? { [CSRF_HEADER]: token } : null),
      },
      body: JSON.stringify({ credential }),
      signal,
    });
  } catch (error) {
    return unreachable(error);
  }

  const text = await response.text().catch(() => '');
  if (!controlSessionGenerationMatches(expectedGeneration)) return { kind: 'superseded' };
  const body = decode(text);

  if (response.status === 401) {
    if (!hasTypedError(body, 'webbridge.credential_invalid')) {
      return malformed('HTTP 401 without webbridge.credential_invalid');
    }
    return {
      kind: 'rejected',
      code: 'webbridge.credential_invalid',
      detail: 'The credential was not accepted.',
    };
  }

  if (response.status === 429) {
    if (!hasTypedError(body, 'webbridge.rate_limited')) {
      return malformed('HTTP 429 without webbridge.rate_limited');
    }
    return {
      kind: 'rate_limited',
      code: 'webbridge.rate_limited',
      detail: 'Too many sign-in attempts.',
      retryAfterSeconds: parseRetryAfter(response.headers.get('retry-after')),
    };
  }

  if (response.status === 409) {
    if (!hasTypedError(body, 'webbridge.session_changed')) {
      return malformed('HTTP 409 without webbridge.session_changed');
    }
    if (!resetControlSession(expectedGeneration)) return { kind: 'superseded' };
    return {
      kind: 'refused',
      code: 'webbridge.session_changed',
      detail: 'The browser session changed during sign-in. Enter the credential again.',
    };
  }

  if (!response.ok) return failureFor(response, body);

  const probe = readAuthenticated(response, body, text, expectedGeneration, true);
  return probe.kind === 'authenticated' ? { kind: 'authenticated' } : probe;
}

/**
 * Ends the session.
 *
 * A 401 counts as success: the session this was meant to destroy is already
 * gone, which is the state the caller asked for. Anything else is reported and
 * retains the local proof so the caller can make an explicit retry instead of
 * claiming that an indeterminate server-side logout succeeded.
 */
export async function destroySession(signal?: AbortSignal): Promise<LogoutOutcome> {
  const authority = currentControlSession();
  if (!authority) {
    resetControlSession(controlSessionGeneration());
    clearSessionLogoutPending();
    return { kind: 'ended' };
  }
  markSessionLogoutPending(authority.generation);
  let response: Response;
  try {
    response = await sendDelete(authority.proof, signal);
  } catch (error) {
    return unreachable(error);
  }

  const text = await response.text().catch(() => '');
  if (!controlSessionGenerationMatches(authority.generation)) return { kind: 'superseded' };
  const body = decode(text);
  if (response.status === 401) {
    if (!hasTypedError(body, 'webbridge.session_invalid')) {
      return malformed('HTTP 401 without webbridge.session_invalid');
    }
    if (!resetControlSession(authority.generation)) return { kind: 'superseded' };
    clearSessionLogoutPending();
    return { kind: 'ended' };
  }
  if (response.status === 403 && hasTypedError(body, 'webbridge.csrf_invalid')) {
    // The shared cookie may now belong to a newer login in another tab. This
    // proof can never authorize a retry, and the newer cookie must be left
    // alone, so end only this tab's stale local authority.
    if (!resetControlSession(authority.generation)) return { kind: 'superseded' };
    clearSessionLogoutPending();
    return { kind: 'ended' };
  }
  if (!response.ok) return failureFor(response, body);
  const ended = readEnded(body, text);
  if (ended.kind !== 'ended') return ended;
  if (!resetControlSession(authority.generation)) return { kind: 'superseded' };
  clearSessionLogoutPending();
  return ended;
}

function sendDelete(token: string, signal?: AbortSignal): Promise<Response> {
  return fetch(SESSION_ROUTE, {
    method: 'DELETE',
    credentials: 'same-origin',
    headers: { accept: 'application/json', [CSRF_HEADER]: token },
    signal,
  });
}

/**
 * Reads `Retry-After`, which RFC 9110 allows to be either a delay in seconds or
 * an HTTP date. Anything else, including a date in the past, yields null and
 * the screen simply omits the wait.
 */
export function parseRetryAfter(header: string | null): number | null {
  if (!header) return null;
  const raw = header.trim();
  if (!raw) return null;

  if (/^\d+$/.test(raw)) {
    const seconds = Number.parseInt(raw, 10);
    return Number.isFinite(seconds) && seconds >= 0 ? seconds : null;
  }

  const at = Date.parse(raw);
  if (Number.isNaN(at)) return null;
  const seconds = Math.ceil((at - Date.now()) / 1000);
  return seconds > 0 ? seconds : null;
}

/**
 * Announces that a session ended for a reason other than the operator asking.
 *
 * Re-exported here so the gate has one import for everything session-shaped,
 * and so the domain transport keeps owning the detection.
 */
export { onControlSessionLost, notifyControlSessionLost } from './control.ts';
export { clearSessionLogoutPending, sessionLogoutPending } from './sessionProof.ts';

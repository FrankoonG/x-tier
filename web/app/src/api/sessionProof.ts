export const SESSION_PROOF_STORAGE_KEY = 'xtier.web.session-proof.v1';
export const SESSION_LOGOUT_PENDING_STORAGE_KEY = 'xtier.web.logout-pending.v1';

export interface SessionAuthority {
  readonly generation: number;
  readonly proof: string;
}

let proof: string | null = null;
let logoutPending = false;
let hydrated = false;
let generation = 0;

function storage(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    return null;
  }
}

function validProof(value: string | null): value is string {
  return value !== null
    && value.length > 0
    && value.length <= 256
    && value === value.trim()
    && /^[A-Za-z0-9_-]+$/.test(value);
}

function hydrate() {
  if (hydrated) return;
  hydrated = true;
  generation += 1;

  const area = storage();
  if (!area) return;
  try {
    const stored = area.getItem(SESSION_PROOF_STORAGE_KEY);
    if (validProof(stored)) {
      proof = stored;
    } else {
      area.removeItem(SESSION_PROOF_STORAGE_KEY);
    }
    logoutPending = area.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY) === '1';
    if (!logoutPending) area.removeItem(SESSION_LOGOUT_PENDING_STORAGE_KEY);
  } catch {
    proof = null;
    logoutPending = false;
  }
}

export function currentSessionAuthority(): SessionAuthority | null {
  hydrate();
  return proof === null ? null : { generation, proof };
}

export function currentSessionGeneration(): number {
  hydrate();
  return generation;
}

export function sessionGenerationMatches(expected: number): boolean {
  hydrate();
  return generation === expected;
}

export function sessionLogoutPending(): boolean {
  hydrate();
  return logoutPending;
}

export function markSessionLogoutPending(
  expectedGeneration = currentSessionGeneration(),
): boolean {
  hydrate();
  if (generation !== expectedGeneration) return false;

  logoutPending = true;
  const area = storage();
  if (!area) return false;
  try {
    area.setItem(SESSION_LOGOUT_PENDING_STORAGE_KEY, '1');
    if (area.getItem(SESSION_LOGOUT_PENDING_STORAGE_KEY) === '1') return true;
  } catch {
    // Fall through to the fail-closed cleanup below.
  }

  // Keep the in-memory proof long enough for the current DELETE and an
  // explicit retry, but make sure a reload cannot recover active authority
  // without the logout intent that was supposed to accompany it.
  try {
    area.removeItem(SESSION_PROOF_STORAGE_KEY);
  } catch {
    // If storage itself became unavailable, a reload cannot read the proof
    // either. The current document still remains in the signing-out state.
  }
  return false;
}

export function clearSessionLogoutPending(expectedGeneration?: number): boolean {
  hydrate();
  if (expectedGeneration !== undefined && generation !== expectedGeneration) return false;

  logoutPending = false;
  const area = storage();
  if (area) {
    try {
      area.removeItem(SESSION_LOGOUT_PENDING_STORAGE_KEY);
    } catch {
      // The server-side logout is already authoritative. A stale marker can
      // only keep a later reload on the fail-closed retry screen.
    }
  }
  return true;
}

export function adoptSessionProof(
  next: string,
  expectedGeneration = currentSessionGeneration(),
  advanceGeneration = false,
): SessionAuthority | null {
  hydrate();
  if (generation !== expectedGeneration || !validProof(next)) return null;

  const area = storage();
  if (!area) return null;
  try {
    area.setItem(SESSION_PROOF_STORAGE_KEY, next);
    if (area.getItem(SESSION_PROOF_STORAGE_KEY) !== next) return null;
  } catch {
    return null;
  }

  if (proof !== next || advanceGeneration) {
    proof = next;
    generation += 1;
  }
  if (advanceGeneration) clearSessionLogoutPending(generation);
  return { generation, proof: next };
}

export function clearSessionAuthority(expectedGeneration?: number): boolean {
  hydrate();
  if (expectedGeneration !== undefined && generation !== expectedGeneration) return false;

  const heldProof = proof !== null;
  proof = null;
  if (heldProof) generation += 1;
  const area = storage();
  if (area) {
    try {
      area.removeItem(SESSION_PROOF_STORAGE_KEY);
    } catch {
      // The in-memory authority is still cleared. A later page load will fail
      // closed if storage remains unavailable.
    }
  }
  return true;
}

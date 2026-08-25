import type { DaemonState, ReconcileStatus, RuntimeState } from './types';

export const CONFIG_CONTENT_INVALID = 'config.content_invalid';

export function isConfigContentInvalid(
  reconcile: Pick<ReconcileStatus, 'last_error_code'> | null | undefined,
) {
  return reconcile?.last_error_code === CONFIG_CONTENT_INVALID;
}

/**
 * `RuntimeState` -> presentation.
 *
 * `stopped` and `failed` are things the daemon SAID. Collapsing them into
 * `unknown` alongside `unavailable` — which really is an absence — throws away
 * the only negative readings this API produces. The three are kept apart.
 */
export function runtimeStatus(state: RuntimeState | DaemonState | undefined) {
  switch (state) {
    case 'running':
      return 'ok' as const;
    case 'failed':
      return 'failed' as const;
    case 'degraded':
      return 'degraded' as const;
    case 'stopped':
    case 'stopping':
      return 'inactive' as const;
    case 'starting':
      return 'info' as const;
    // `unavailable` is the daemon declining to report, which is genuinely
    // not-observed — and so is `undefined`.
    default:
      return 'unknown' as const;
  }
}

import { adviseCode, type FailureView } from '../api/errors.ts';
import type { SessionFailure } from '../api/session.ts';

/**
 * Presents a session failure the way every other failure in the panel is
 * presented.
 *
 * Sign-in has its own transport, but it has no business having its own idea of
 * what a failure looks like. Routing through the same catalogue means an
 * operator who has learned to read `control.unreachable` on the Overview reads
 * the identical banner, with the identical code, on the login screen.
 *
 * `applied: false` is always right here: none of these outcomes changed
 * anything on the node.
 */
export function describeSessionFailure(failure: SessionFailure): FailureView {
  return {
    ...adviseCode(failure.code),
    code: failure.code,
    detail: failure.detail,
    applied: false,
  };
}

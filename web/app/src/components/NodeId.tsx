/* ===========================================================================
 * THE LOCAL NODE ID
 *
 * One truncation, used everywhere it appears.
 *
 * `Identifier` exists because the ENDS of an id are what a human compares, and
 * two screens showing two different windows onto the same value defeats
 * exactly that: an operator checking the Overview against the Identity screen
 * is comparing different substrings and cannot tell whether they match.
 *
 * Reserved for the LOCAL node id, which is cryptographically derived and
 * canonically validated. A peer's `node_id` is an arbitrary string the backend
 * never parses, so it gets `Code` — dressing it in a fingerprint affordance
 * would claim a verification nobody performed.
 * ======================================================================== */
import { Identifier } from '@stratum/ui';

export function NodeId({ value, full }: { value: string | null | undefined; full?: boolean }) {
  return <Identifier value={value} head={20} tail={8} full={full} />;
}

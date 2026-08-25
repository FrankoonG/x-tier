/* ===========================================================================
 * THE ABSENT VALUE
 *
 * One marker for "there is no value here", used everywhere instead of eight
 * hand-rolled em-dashes with eight slightly different inline styles.
 *
 * The glyph is the framework's own `UNOBSERVED`, so a bare cell here and a
 * `<Count value={null}>` next to it render the same character rather than two
 * different dashes.
 *
 * It is deliberately quiet: an absent value is not an error, and giving it a
 * warning colour would train the operator to read every blank cell as a
 * problem. When absence IS meaningful — not observed rather than not set — say
 * so in words via the `children`, which is why they exist.
 * ======================================================================== */
import { UNOBSERVED } from '@stratum/ui';
import type { ReactNode } from 'react';

export function Absent({ children }: { children?: ReactNode }) {
  return (
    <span
      style={{
        color: 'var(--stratum-text-subtle)',
        fontStyle: children ? 'italic' : undefined,
        fontSize: children ? 'var(--stratum-text-xs)' : undefined,
      }}
    >
      {children ?? UNOBSERVED}
    </span>
  );
}

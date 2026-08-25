/* ===========================================================================
 * SECTION LABEL
 *
 * Small supporting typography used to label and explain compact sections.
 * These are standalone counterparts to the label and description owned by a
 * form Field.
 * ======================================================================== */
import type { ReactNode } from 'react';

export interface SectionLabelProps {
  children: ReactNode;
  id?: string;
}

export function SectionLabel({ children, id }: SectionLabelProps) {
  return (
    <span
      id={id}
      style={{
        fontSize: 'var(--stratum-text-2xs)',
        textTransform: 'uppercase',
        letterSpacing: '0.04em',
        color: 'var(--stratum-text-subtle)',
      }}
    >
      {children}
    </span>
  );
}

export interface HintProps {
  children: ReactNode;
}

/** A muted one-line hint. The standalone counterpart to `Field.description`. */
export function Hint({ children }: HintProps) {
  return (
    <span style={{ fontSize: 'var(--stratum-text-xs)', color: 'var(--stratum-text-muted)' }}>
      {children}
    </span>
  );
}

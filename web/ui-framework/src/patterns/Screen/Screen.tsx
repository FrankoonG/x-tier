/* ===========================================================================
 * SCREEN
 *
 * Page framing shared by operational screens. PageHeader owns its horizontal
 * gutter, so the header remains full-bleed while the content below uses the
 * same spacing token. Keeping that relationship here prevents consumers from
 * accidentally applying the gutter twice.
 * ======================================================================== */
import type { CSSProperties, ReactNode } from 'react';

/** Must equal `PageHeader`'s horizontal gutter at size md. */
const GUTTER = 'var(--stratum-space-10)';

export interface ScreenProps {
  /** A `<PageHeader>`. Rendered full-bleed; do not pad it yourself. */
  header: ReactNode;
  /** A full-bleed strip directly below the header. */
  notice?: ReactNode;
  /** Gives the remaining block size to a child that requests it with flex. */
  fill?: boolean;
  children: ReactNode;
}

export function Screen({ header, notice, fill = false, children }: ScreenProps) {
  const frame: CSSProperties = fill
    ? {
        display: 'flex',
        flexDirection: 'column',
        blockSize: '100%',
        minBlockSize: 0,
        minInlineSize: 0,
      }
    : { display: 'flex', flexDirection: 'column', minHeight: '100%', minInlineSize: 0 };

  return (
    <div style={frame}>
      {header}
      {notice}
      <div
        style={{
          ...(fill
            ? { display: 'flex', flexDirection: 'column', minBlockSize: 0 }
            : { display: 'grid', alignContent: 'start' }),
          gap: 'var(--stratum-space-12)',
          minInlineSize: 0,
          padding: GUTTER,
          paddingBlockStart: 'var(--stratum-space-8)',
          flex: 1,
        }}
      >
        {children}
      </div>
    </div>
  );
}

export interface RowProps {
  children: ReactNode;
  gap?: string;
  align?: 'center' | 'flex-start' | 'flex-end' | 'baseline';
  wrap?: boolean;
  grow?: boolean;
}

/** A consistently spaced row of related controls or actions. */
export function Row({
  children,
  gap = 'var(--stratum-space-6)',
  align = 'center',
  wrap = true,
  grow,
}: RowProps) {
  return (
    <div
      style={{
        display: 'flex',
        gap,
        alignItems: align,
        flexWrap: wrap ? 'wrap' : 'nowrap',
        ...(grow ? { flex: 1 } : null),
      }}
    >
      {children}
    </div>
  );
}

export interface ColumnsProps {
  children: ReactNode;
  min?: string;
}

/** Responsive columns that collapse without overflowing a narrow container. */
export function Columns({ children, min = '26rem' }: ColumnsProps) {
  return (
    <div
      style={{
        display: 'grid',
        gap: 'var(--stratum-space-12)',
        gridTemplateColumns: `repeat(auto-fit, minmax(min(100%, ${min}), 1fr))`,
        alignItems: 'start',
      }}
    >
      {children}
    </div>
  );
}

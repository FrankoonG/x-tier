/* ---------------------------------------------------------------------------
 * Shared table parts.
 *
 * Internal to the data layer — Table, DataTable and TreeTable all render the
 * same checkbox, the same sort affordance and the same disclosure triangle, so
 * they live here rather than being reimplemented three times with three
 * slightly different hit targets.
 *
 * Styles for everything in this file live in `Table.css`, which every table in
 * the family imports.
 * ------------------------------------------------------------------------- */
import { useEffect, useRef } from 'react';
import type { ChangeEvent, ReactNode, SyntheticEvent } from 'react';

/**
 * Width of the leading checkbox column, in px.
 *
 * Lives in TypeScript rather than only in CSS because sticky column offsets
 * are summed in JS; two sources of truth for this number would drift and the
 * symptom — a pinned column overlapping its neighbour by a few pixels — is
 * maddening to trace. The components publish it back to CSS as a custom
 * property so the stylesheet still never hardcodes it.
 */
export const SELECT_COLUMN_WIDTH = 36;

export interface SelectionCheckboxProps {
  checked: boolean;
  /**
   * Renders the mixed state. Applied through a ref because `indeterminate` is
   * a DOM property with no corresponding HTML attribute — React will not set
   * it from JSX, and every table that forgets this ships a "select all" box
   * that lies about partial selections.
   */
  indeterminate?: boolean;
  disabled?: boolean;
  /** Accessible name. Required: this control has no visible label. */
  label: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
}

export function SelectionCheckbox({
  checked,
  indeterminate = false,
  disabled = false,
  label,
  onChange,
}: SelectionCheckboxProps) {
  const ref = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    // "Some" only reads as mixed while it is not also "all".
    node.indeterminate = indeterminate && !checked;
  }, [indeterminate, checked]);

  return (
    <input
      ref={ref}
      type="checkbox"
      className="stratum-table__checkbox"
      checked={checked}
      disabled={disabled}
      aria-label={label}
      onChange={onChange}
    />
  );
}

export type SortDirection = 'asc' | 'desc';

export interface SortGlyphProps {
  /** `false` when the column is sortable but not currently sorted. */
  direction: SortDirection | false;
  /** 1-based position in a multi-column sort. Rendered only when > 1. */
  order?: number;
}

/**
 * Sort affordance. Three distinct SHAPES, not one arrow at three opacities:
 * an unsorted column has to be visibly different from a sorted one for someone
 * who cannot rely on a subtle contrast difference (WCAG 1.4.1), and the
 * direction has to be readable at 10px.
 */
export function SortGlyph({ direction, order }: SortGlyphProps) {
  return (
    <span className="stratum-table__sort-glyph" data-direction={direction || 'none'} aria-hidden="true">
      <svg viewBox="0 0 10 12" width="10" height="12" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" focusable="false">
        {direction === 'asc' && <path d="M5 10.5V2.2M1.9 5.3 5 2.1l3.1 3.2" />}
        {direction === 'desc' && <path d="M5 1.5v8.3M1.9 6.7 5 9.9l3.1-3.2" />}
        {direction === false && <path d="M2.2 4.9 5 2.1l2.8 2.8M2.2 7.1 5 9.9l2.8-2.8" />}
      </svg>
      {order != null && order > 1 && (
        <span className="stratum-table__sort-order">{order}</span>
      )}
    </span>
  );
}

export interface DisclosureGlyphProps {
  expanded: boolean;
}

/**
 * Expand/collapse triangle. Rotation is a transform on a tiny chrome element,
 * never a layout animation, so it costs nothing during a virtualised scroll.
 */
export function DisclosureGlyph({ expanded }: DisclosureGlyphProps) {
  return (
    <span className="stratum-table__disclosure-glyph" data-expanded={expanded || undefined} aria-hidden="true">
      <svg viewBox="0 0 12 12" width="10" height="10" fill="currentColor" focusable="false">
        <path d="M4 2.2 8.6 6 4 9.8Z" />
      </svg>
    </span>
  );
}

export interface SkeletonRowsProps {
  rows: number;
  /** Total number of columns, INCLUDING the selection column when present. */
  columns: number;
  /** Widths as percentages, cycled across columns so the block looks organic. */
  widths?: readonly number[];
}

const SKELETON_WIDTHS = [72, 46, 88, 34, 60, 52] as const;

/**
 * Placeholder rows shown while data loads.
 *
 * Hidden from assistive technology: the table itself carries `aria-busy` and a
 * status label, which is the announcement that matters. Reading out six rows
 * of nothing is worse than silence.
 */
export function SkeletonRows({ rows, columns, widths = SKELETON_WIDTHS }: SkeletonRowsProps): ReactNode {
  return Array.from({ length: rows }, (_, row) => (
    <tr key={`skeleton-${row}`} className="stratum-table__row stratum-table__row--skeleton" aria-hidden="true">
      {Array.from({ length: columns }, (_, column) => (
        <td key={column} className="stratum-table__cell">
          <span
            className="stratum-table__skeleton"
            style={{ inlineSize: `${widths[(row + column) % widths.length] ?? 60}%` }}
          />
        </td>
      ))}
    </tr>
  ));
}

/**
 * Resolves a column width to px for sticky-offset arithmetic.
 * Returns `null` for widths that cannot be summed (`'40%'`, `'8rem'`).
 */
export function resolvePxWidth(width: number | string | undefined): number | null {
  if (typeof width === 'number') return Number.isFinite(width) ? width : null;
  if (typeof width !== 'string') return null;
  const match = /^\s*(-?[\d.]+)px\s*$/.exec(width);
  const captured = match?.[1];
  if (captured === undefined) return null;
  const value = Number(captured);
  return Number.isFinite(value) ? value : null;
}

/**
 * Keeps a click inside the checkbox cell from reaching the row handler.
 *
 * Both `click` and `mousedown` are stopped: `mousedown` because a row that
 * begins a drag or a range gesture would otherwise start one from inside the
 * checkbox, and `click` because that is what actually reaches the row handler.
 */
export function stopRowPropagation(event: SyntheticEvent): void {
  event.stopPropagation();
}

/* ---------------------------------------------------------------------------
 * ROW-LEVEL ROVING TABINDEX
 *
 * Internal. Shared by Table, DataTable and TreeTable.
 *
 * WHY ROWS AND NOT CELLS
 * ----------------------
 * ARIA's grid pattern allows focus to live on rows rather than on individual
 * cells, and for an operations table that is the right choice: the unit an
 * operator acts on is the row, and cell-by-cell traversal of a 9-column table
 * turns "go to the next node" into nine keypresses.
 *
 * WHY ROVING AND NOT tabIndex=0 EVERYWHERE
 * ----------------------------------------
 * A 10 000-row table with every row in the tab order is not a table, it is a
 * trap. Exactly one row carries `tabIndex=0`; the arrow keys move that, and a
 * single Tab leaves the table entirely.
 *
 * FOCUS AFTER RENDER, NOT DURING
 * ------------------------------
 * Moving to a row that is not rendered yet — the virtualised case — cannot
 * focus synchronously. A request is recorded, the scroll is asked for, and the
 * focus attempt is retried across a few frames until the row exists. Bounded,
 * so a request for a row that never materialises dies quietly instead of
 * spinning.
 * ------------------------------------------------------------------------- */
import { useEffect, useRef, useState } from 'react';
import { useEventCallback } from '../hooks/useEventCallback';

/** Portable read-only ref shape: matches React 18 and 19 `useRef` results. */
export interface ReadonlyElementRef {
  readonly current: HTMLElement | null;
}

export interface UseRowNavigationOptions {
  /** Roving is only installed when the table is actually interactive. */
  enabled: boolean;
  /** Number of addressable rows. */
  count: number;
  /** Element whose DIRECT children are the rows. */
  containerRef: ReadonlyElementRef;
  /** Called before focusing, so a virtualiser can bring the row into view. */
  onBeforeFocus?: ((index: number) => void) | undefined;
}

export interface RowNavigation {
  /** Row that currently owns `tabIndex=0`. Clamped into range. */
  activeIndex: number;
  /** Records focus that arrived by pointer or Tab, without moving anything. */
  setActiveIndex: (index: number) => void;
  /** Moves the roving index and focuses that row once it exists. */
  focusRow: (index: number) => void;
}

/** Rows are found by attribute so no per-row ref bookkeeping is needed. */
export function closestRow(
  target: EventTarget | null,
  container: HTMLElement | null,
): HTMLTableRowElement | null {
  const element = target instanceof Element ? target : null;
  if (!element || !container || !container.contains(element)) return null;
  const row = element.closest<HTMLTableRowElement>('tr[data-row-index]');
  return row && container.contains(row) ? row : null;
}

/** Reads the numeric row index off a row element, or `-1`. */
export function rowIndexOf(row: HTMLElement | null): number {
  const raw = row?.dataset['rowIndex'];
  if (raw === undefined) return -1;
  const parsed = Number.parseInt(raw, 10);
  return Number.isNaN(parsed) ? -1 : parsed;
}

/** How many frames to wait for a virtualised row to appear before giving up. */
const FOCUS_ATTEMPTS = 4;

export function useRowNavigation({
  enabled,
  count,
  containerRef,
  onBeforeFocus,
}: UseRowNavigationOptions): RowNavigation {
  const [rawIndex, setRawIndex] = useState(0);
  const pending = useRef<number | null>(null);

  const activeIndex = count === 0 ? -1 : Math.min(Math.max(rawIndex, 0), count - 1);

  // No dependency array on purpose: a focus request can be raised by any
  // render, and the ref guard makes the common case a single comparison.
  useEffect(() => {
    const index = pending.current;
    if (index === null || !enabled) {
      pending.current = null;
      return;
    }

    let frame = 0;
    let handle = 0;
    const attempt = () => {
      const node = containerRef.current?.querySelector<HTMLElement>(
        `:scope > [data-row-index="${index}"]`,
      );
      if (node) {
        pending.current = null;
        node.focus();
        return;
      }
      if (frame < FOCUS_ATTEMPTS) {
        frame += 1;
        handle = requestAnimationFrame(attempt);
      } else {
        pending.current = null;
      }
    };
    attempt();

    return () => {
      if (handle) cancelAnimationFrame(handle);
    };
  });

  const focusRow = useEventCallback((index: number) => {
    if (count === 0) return;
    const target = Math.min(Math.max(index, 0), count - 1);
    setRawIndex(target);
    pending.current = target;
    onBeforeFocus?.(target);
  });

  const setActiveIndex = useEventCallback((index: number) => {
    if (index < 0) return;
    setRawIndex(index);
  });

  return { activeIndex, setActiveIndex, focusRow };
}

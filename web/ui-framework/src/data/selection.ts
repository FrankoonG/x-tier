/* ---------------------------------------------------------------------------
 * THE SELECTION MODEL
 *
 * Every table in this library shares one selection discipline, and it is not
 * the obvious one. It comes out of watching operators use dense tables, where
 * two different intentions collide on the same row:
 *
 *   "I want to act on THESE five things"   -> additive, cumulative
 *   "I want to look at THIS thing"         -> exclusive, replaces
 *
 * Libraries that pick one and apply it everywhere get complaints in both
 * directions: additive-only makes "show me just this one" a chore of clearing
 * first, and exclusive-only makes multi-select impossible without a modifier
 * key nobody discovers.
 *
 * So the two intentions get two different targets on the same row:
 *
 *   LEADING CHECKBOX -> additive. Toggles one id, leaves the rest alone.
 *   ROW BODY         -> exclusive. Replaces the whole selection with this row.
 *   ROW BODY, when this row is ALREADY the sole selection -> deselects.
 *
 * That last rule is what makes the row body safe to click: the gesture is its
 * own undo, so nothing is ever stranded selected because the operator had to
 * hunt for a way out.
 *
 * Two implementation details are load-bearing and are enforced by the
 * components rather than left to the caller:
 *
 *   1. The whole checkbox CELL is the hit target, padding included, and it
 *      stops propagation. A 14px checkbox inside a 36px cell means a quarter
 *      of the clicks aimed at it land on the cell instead — and if that falls
 *      through to the row handler, the click that was meant to ADD to the
 *      selection instead REPLACES it. That is a data-loss-shaped bug: the
 *      operator loses four selected rows and does not always notice.
 *
 *   2. `isInteractiveDescendant` guards the row handler. A row with an inline
 *      "Edit" button must not also change the selection when that button is
 *      pressed; the button is a different intention entirely.
 * ------------------------------------------------------------------------- */
import { useMemo } from 'react';
import { useControllableState } from '../hooks/useControllableState';
import { useEventCallback } from '../hooks/useEventCallback';

/**
 * Shared empty default. Never mutated — every write in this module produces a
 * new `Set` — so one instance can safely back every uncontrolled table.
 */
const EMPTY_SELECTION: Set<string> = new Set();

/**
 * Elements that own their own click. Walking up from the click target to the
 * row and finding any of these means the click was aimed at a control, not at
 * the row.
 *
 * `label` is in the list because the checkbox cell wraps its input in one, and
 * because a label click is really a click on the control it names.
 */
const INTERACTIVE_SELECTOR =
  'button, a, input, select, textarea, label, ' +
  '[contenteditable=""], [contenteditable="true"], [data-stratum-ignore-row-click]';

/**
 * True when `target` sits inside an interactive control that is itself inside
 * `row`.
 *
 * Walks ancestors from the click target up to — but not past — the row, so a
 * control OUTSIDE the row (an ancestor of the whole table, say) never
 * suppresses row activation.
 *
 * Opt an arbitrary subtree out of row clicks with
 * `data-stratum-ignore-row-click`, which is the escape hatch for custom
 * controls that are not built from a native interactive element.
 */
export function isInteractiveDescendant(
  target: EventTarget | null,
  row: Element | null | undefined,
): boolean {
  if (!row) return false;
  // `closest` exists on SVGElement too, so an icon inside a button resolves.
  const element = target instanceof Element ? target : null;
  if (!element) return false;
  if (!row.contains(element)) return false;

  const hit = element.closest(INTERACTIVE_SELECTOR);
  return hit != null && hit !== row && row.contains(hit);
}

export interface UseSelectionOptions {
  /**
   * Ids of every row currently addressable, in display order. This is the set
   * `toggleAll` operates on and the set `isAllSelected` is measured against —
   * so under an active filter, "select all" means "select all VISIBLE", which
   * is the only reading that does not surprise someone who just filtered.
   */
  ids: readonly string[];
  /** Controlled selection. */
  value?: Set<string>;
  /** Initial selection for the uncontrolled case. */
  defaultValue?: Set<string>;
  /** Fires on every change, in both controlled and uncontrolled modes. */
  onChange?: (selected: Set<string>) => void;
}

export interface SelectionModel {
  /** Currently selected ids. Treat as immutable. */
  selected: Set<string>;
  /** ADDITIVE. Flips one id and leaves every other alone. The checkbox gesture. */
  toggle: (id: string) => void;
  /**
   * Selects or clears every id in `ids`. Ids outside `ids` — rows hidden by a
   * filter — are left untouched, so filtering then "select all" then clearing
   * the filter cannot silently drop a selection the operator made earlier.
   */
  toggleAll: (next?: boolean) => void;
  /**
   * EXCLUSIVE. Replaces the whole selection with this one id, or clears it
   * when this id is already the sole selection. The row-body gesture.
   */
  selectOnly: (id: string) => void;
  clear: () => void;
  isSelected: (id: string) => boolean;
  /** True when every id in `ids` is selected. Drives the header checkbox. */
  isAllSelected: boolean;
  /**
   * True when SOME but not all of `ids` are selected. Drives the header
   * checkbox's `indeterminate`, which is why it is false when everything is
   * selected rather than simply "more than zero".
   */
  isSomeSelected: boolean;
}

/**
 * Row selection state with the framework's two-gesture discipline.
 *
 * Controlled (`value` + `onChange`) and uncontrolled (`defaultValue`) usage
 * come from one implementation, and `onChange` fires in both.
 *
 * ```tsx
 * const selection = useSelection({ ids: rows.map((r) => r.id) });
 * <Table data={rows} rowKey={(r) => r.id} selection={selection} … />
 * ```
 */
export function useSelection({
  ids,
  value,
  defaultValue,
  onChange,
}: UseSelectionOptions): SelectionModel {
  const [selected, setSelected] = useControllableState<Set<string>>({
    value,
    defaultValue: defaultValue ?? EMPTY_SELECTION,
    onChange,
  });

  const { isAllSelected, isSomeSelected } = useMemo(() => {
    let hit = 0;
    for (const id of ids) if (selected.has(id)) hit += 1;
    const all = ids.length > 0 && hit === ids.length;
    return { isAllSelected: all, isSomeSelected: hit > 0 && !all };
  }, [ids, selected]);

  const toggle = useEventCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      // `delete` reports whether it removed anything, so one call decides the
      // direction of the toggle.
      if (!next.delete(id)) next.add(id);
      return next;
    });
  });

  const toggleAll = useEventCallback((next?: boolean) => {
    const select = next ?? !isAllSelected;
    setSelected((prev) => {
      const result = new Set(prev);
      for (const id of ids) {
        if (select) result.add(id);
        else result.delete(id);
      }
      return result;
    });
  });

  const selectOnly = useEventCallback((id: string) => {
    setSelected((prev) => {
      // Clicking the body of the only selected row is its own undo.
      if (prev.size === 1 && prev.has(id)) return new Set<string>();
      return new Set<string>([id]);
    });
  });

  const clear = useEventCallback(() => {
    setSelected((prev) => (prev.size === 0 ? prev : new Set<string>()));
  });

  const isSelected = useEventCallback((id: string) => selected.has(id));

  return useMemo<SelectionModel>(
    () => ({
      selected,
      toggle,
      toggleAll,
      selectOnly,
      clear,
      isSelected: (id: string) => isSelected(id) ?? false,
      isAllSelected,
      isSomeSelected,
    }),
    [selected, toggle, toggleAll, selectOnly, clear, isSelected, isAllSelected, isSomeSelected],
  );
}

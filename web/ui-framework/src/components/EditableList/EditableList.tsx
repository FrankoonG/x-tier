import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ForwardedRef,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactElement,
  type ReactNode,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { VisuallyHidden } from '../../primitives/VisuallyHidden';
import { InlineMessage } from '../InlineMessage/InlineMessage';
import { useIsomorphicLayoutEffect } from '../Popover/overlayFocus';
import {
  applyShift,
  clearShift,
  dampOffset,
  resolveTarget,
  settleDrop,
  snapshotRows,
  type ReorderSnapshot,
} from '../../_shared/reorderGeometry';
import './EditableList.css';

/* -------------------------------------------------------------------------- */
/* Row identity                                                                */
/* -------------------------------------------------------------------------- */

/**
 * Rows are keyed by a generated id, never by array index.
 *
 * Keying by index means removing row 2 renumbers row 3 into row 2's key, so
 * React reconciles row 3's DOM onto row 2's — every uncommitted keystroke, the
 * caret and the focus ring shift up one row. The id travels with the row
 * through every insert, remove and move, so the DOM node does too.
 */
let rowIdSeq = 0;
function nextRowId(): string {
  rowIdSeq += 1;
  return `slrow${rowIdSeq.toString(36)}`;
}

/* -------------------------------------------------------------------------- */
/* Focus plumbing                                                              */
/* -------------------------------------------------------------------------- */

const FOCUSABLE_SELECTOR =
  'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),' +
  'textarea:not([disabled]),[contenteditable="true"],[tabindex]:not([tabindex="-1"])';

function rowElement(list: HTMLElement | null, index: number): HTMLElement | null {
  if (!list || index < 0) return null;
  return list.querySelector<HTMLElement>(`[data-row-index="${index}"]`);
}

/**
 * The caller's fields in one row, in tab order.
 *
 * Our own grip / duplicate / remove controls are excluded by the
 * `data-list-control` marker, so "the same field in the next row" means the
 * same *data* field and never lands on a delete button.
 */
function rowFields(row: HTMLElement): HTMLElement[] {
  return Array.from(row.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (element) => element.closest('[data-list-control]') === null,
  );
}

function fieldIndexOf(row: HTMLElement | null, node: Element | null): number {
  if (!row || !node) return 0;
  const index = rowFields(row).indexOf(node as HTMLElement);
  return index < 0 ? 0 : index;
}

function placeCaret(element: HTMLElement, caret: CaretPlacement): void {
  if (caret === 'none') return;
  if (!(element instanceof HTMLInputElement) && !(element instanceof HTMLTextAreaElement)) return;
  const position = caret === 'end' ? element.value.length : 0;
  try {
    element.setSelectionRange(position, position);
  } catch {
    // `setSelectionRange` throws on email/number inputs. Focus already landed,
    // which is the part that matters.
  }
}

type CaretPlacement = 'start' | 'end' | 'none';

type FocusRequest =
  | {
      kind: 'row';
      index: number;
      field: number;
      caret: CaretPlacement;
      control: 'field' | 'grip';
      /**
       * Only act if focus was actually lost. A keyed reorder moves the focused
       * DOM node rather than recreating it, so focus normally survives a move
       * on its own; forcing it would drag the user off the grip and onto the
       * first text field, breaking a run of Alt+ArrowUp after one press.
       */
      restoreOnly: boolean;
      token: number;
    }
  | { kind: 'add'; token: number };

/** Enter must not steal the key from a control that already means something by it. */
function acceptsInsertKey(target: HTMLElement): boolean {
  if (target.getAttribute('aria-expanded') === 'true') return false;
  if (target instanceof HTMLTextAreaElement) return false;
  if (target instanceof HTMLSelectElement) return false;
  if (target instanceof HTMLButtonElement) return false;
  if (target instanceof HTMLInputElement) {
    const type = target.type;
    return type !== 'checkbox' && type !== 'radio' && type !== 'button' && type !== 'submit';
  }
  return false;
}

/** True when Backspace/Delete in this field would delete nothing. */
function isEmptyTextTarget(target: HTMLElement): boolean {
  if (target instanceof HTMLInputElement) {
    const type = target.type;
    if (type === 'checkbox' || type === 'radio' || type === 'button' || type === 'submit') {
      return false;
    }
    return target.value === '';
  }
  if (target instanceof HTMLTextAreaElement) return target.value === '';
  return false;
}

/* -------------------------------------------------------------------------- */
/* Public types                                                                */
/* -------------------------------------------------------------------------- */

export type EditableListSize = 'sm' | 'md';

/** Everything a row renderer needs. Handed to `renderRow` on every render. */
export interface EditableListRowApi<T> {
  item: T;
  index: number;
  /** Total rows, including a trailing blank row if one is being shown. */
  count: number;
  /** Stable per-row key. Safe to build ids from. */
  rowId: string;
  /** Human row name — "Address 2 of 4". Already the group's accessible name. */
  rowLabel: string;
  isFirst: boolean;
  isLast: boolean;
  /** True when `isItemEmpty` reports this row holds nothing. */
  isBlank: boolean;
  invalid: boolean;
  error: ReactNode;
  /** Id of the rendered error node, or `undefined` when the row is valid. */
  errorId: string | undefined;
  canRemove: boolean;
  canAdd: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  disabled: boolean;
  update: (next: T | ((previous: T) => T)) => void;
  remove: () => void;
  duplicate: () => void;
  moveUp: () => void;
  moveDown: () => void;
  moveTo: (index: number) => void;
  insertAfter: (seed?: T) => void;
  /**
   * Spread onto the row's primary field. Carries the list keyboard model
   * (Enter / Backspace / Alt+Arrow) plus the validation wiring, so a caller
   * cannot forget `aria-invalid` or `aria-describedby`.
   */
  fieldProps: {
    onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
    'aria-invalid': true | undefined;
    'aria-describedby': string | undefined;
  };
}

export interface EditableListProps<T>
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'onChange' | 'defaultValue'> {
  /** Controlled rows. */
  items?: T[];
  /** Uncontrolled initial rows. */
  defaultItems?: T[];
  onItemsChange?: (items: T[]) => void;
  renderRow: (item: T, api: EditableListRowApi<T>) => ReactNode;

  /** Produces a blank row. Required for adding, inserting or a trailing row. */
  createItem?: () => T;
  /** Copies a row for the duplicate action. Defaults to a shallow copy. */
  cloneItem?: (item: T) => T;
  /**
   * Decides whether a row counts as empty. Drives Backspace-to-remove, the
   * trailing blank row and the announced count. Defaults to a shallow check
   * for `''`, `null`, `undefined` and empty arrays/objects-of-empty-strings.
   */
  isItemEmpty?: (item: T) => boolean;
  /** Per-row validation. Return a message node, or nothing when valid. */
  validateItem?: (item: T, index: number, items: T[]) => ReactNode;

  /** Floor. The remove control stays visible but inert at the floor. */
  minRows?: number;
  /** Ceiling. Adding past it is refused and announced. */
  maxRows?: number;

  addable?: boolean;
  removable?: boolean;
  duplicable?: boolean;
  reorderable?: boolean;
  /**
   * Keeps one empty row at the end instead of showing an add button. Typing
   * into it materialises it and a fresh blank row appears below.
   *
   * The trailing row is excluded from the announced count until it holds
   * something, because a row that does not exist yet must not be reported as
   * one that does.
   */
  trailingBlankRow?: boolean;
  /** Shows 1-based position numbers. Use where order is load-bearing. */
  showRank?: boolean;

  size?: EditableListSize;
  disabled?: boolean;
  /** Shown in place of the rows when there are none. */
  emptyState?: ReactNode;
  /**
   * Replaces the aggregated invalid-row message. `false` suppresses it, for a
   * caller that renders its own summary somewhere else on the form.
   */
  errorSummary?: ReactNode | false;

  /* -- Copy. Every user-visible string is a prop. ------------------------- */
  /** Noun used to build default labels. Default `'Row'`. */
  itemLabel?: string;
  addLabel?: string;
  labelRow?: (index: number, count: number) => string;
  labelBlankRow?: string;
  /** Why the trailing blank row's remove control is inert. */
  labelBlankRowNoRemove?: string;
  labelRemove?: (index: number, count: number) => string;
  labelDuplicate?: (index: number, count: number) => string;
  labelReorder?: (index: number, count: number) => string;
  labelMoveUp?: (index: number, count: number) => string;
  labelMoveDown?: (index: number, count: number) => string;
  labelDropDone?: string;
  /** Instructions attached to the reorder grip via `aria-describedby`. */
  labelReorderHint?: string;
  labelAdded?: (index: number, count: number) => string;
  labelRemoved?: (index: number, count: number) => string;
  labelDuplicated?: (index: number, count: number) => string;
  labelMoved?: (from: number, to: number, count: number) => string;
  labelAtStart?: string;
  labelAtEnd?: string;
  labelMinReached?: (min: number) => string;
  labelMaxReached?: (max: number) => string;
  labelErrorSummary?: (invalid: number, total: number) => string;
}

/* -------------------------------------------------------------------------- */
/* Defaults                                                                    */
/* -------------------------------------------------------------------------- */

function defaultIsItemEmpty(item: unknown): boolean {
  if (item == null) return true;
  if (typeof item === 'string') return item.trim() === '';
  if (typeof item === 'number' || typeof item === 'boolean') return false;
  if (Array.isArray(item)) return item.length === 0;
  if (typeof item === 'object') {
    return Object.values(item as Record<string, unknown>).every(
      (value) =>
        value == null ||
        value === '' ||
        value === false ||
        (Array.isArray(value) && value.length === 0),
    );
  }
  return false;
}

function defaultCloneItem<T>(item: T): T {
  if (Array.isArray(item)) return [...(item as unknown[])] as T;
  if (item !== null && typeof item === 'object') return { ...(item as object) } as T;
  return item;
}

const GripIcon = () => (
  <svg viewBox="0 0 12 16" width="12" height="16" fill="currentColor" focusable="false" aria-hidden="true">
    <circle cx="4" cy="4" r="1.25" />
    <circle cx="8" cy="4" r="1.25" />
    <circle cx="4" cy="8" r="1.25" />
    <circle cx="8" cy="8" r="1.25" />
    <circle cx="4" cy="12" r="1.25" />
    <circle cx="8" cy="12" r="1.25" />
  </svg>
);

const RemoveIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m4.5 4.5 7 7m0-7-7 7" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
  </svg>
);

const DuplicateIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <rect x="5.75" y="5.75" width="7.5" height="7.5" rx="1.5" stroke="currentColor" strokeWidth="1.4" />
    <path d="M10.25 3.25H4.75a1.5 1.5 0 0 0-1.5 1.5v5.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
  </svg>
);

const ArrowUpIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="M8 12.5v-9m0 0L4.5 7M8 3.5 11.5 7" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const ArrowDownIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="M8 3.5v9m0 0L4.5 9M8 12.5 11.5 9" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const CheckIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="m3.5 8.5 3 3 6-7" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const PlusIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" focusable="false" aria-hidden="true">
    <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
  </svg>
);

/* -------------------------------------------------------------------------- */
/* Component                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * An ordered list of caller-rendered rows with add, remove, duplicate and
 * reorder — the substrate every repeated-field editor in an operations panel
 * turns out to need.
 *
 * WHY FOCUS IS THE HARD PART
 * --------------------------
 * Removing a row destroys the element focus was sitting in. Left alone the
 * browser drops focus on `<body>`, which for a keyboard user means the form
 * silently ends: the next Tab restarts from the top of the document. So every
 * structural change names its successor explicitly —
 *
 *   - remove via the row button -> the same field in the row that slid up,
 *     falling back to the previous row, then to the add button;
 *   - remove via Backspace in an empty field -> the END of the previous row's
 *     last field, which is where the caret was heading anyway;
 *   - move -> stays on the moved row's same field, so a run of Alt+ArrowUp
 *     walks a row up the list without touching the mouse;
 *   - add / insert -> the first field of the new row.
 *
 * The request is recorded as state and satisfied in a layout effect, with a
 * bounded retry, because a controlled parent may not commit the new `items`
 * in the same tick.
 *
 * REORDERING HAS THREE INPUT PATHS, NOT TWO
 * -----------------------------------------
 * Keyboard parity does not discharge WCAG 2.2 SC 2.5.7: it requires a
 * single-pointer alternative to dragging, assessed independently of 2.1.1,
 * because a touchscreen user may have no keyboard at all. So:
 *
 *   1. pointer drag from the grip (native HTML5 drag events — no library);
 *   2. Alt+ArrowUp / Alt+ArrowDown from any field in the row;
 *   3. activate the grip to LIFT the row, which reveals move up / move down /
 *      drop buttons — operable by a single pointer, and by Arrow keys plus
 *      Enter/Escape when the grip has focus.
 *
 * Every path emits the same move and the same polite announcement, so the
 * three cannot drift apart.
 *
 * WHAT IS DELIBERATELY NOT HERE
 * -----------------------------
 * No global drag class on `document.body`. A row that unmounts mid-drag never
 * fires `dragend`, so anything written to the document leaks permanently;
 * the dragging state lives on the row as `data-dragging` and dies with it.
 */
function EditableListInner<T>(
  {
    items: itemsProp,
    defaultItems,
    onItemsChange,
    renderRow,
    createItem,
    cloneItem = defaultCloneItem,
    isItemEmpty = defaultIsItemEmpty as (item: T) => boolean,
    validateItem,
    minRows = 1,
    maxRows,
    addable,
    removable = true,
    duplicable = false,
    reorderable = false,
    trailingBlankRow = false,
    showRank = false,
    size = 'md',
    disabled = false,
    emptyState,
    errorSummary,
    itemLabel = 'Row',
    addLabel,
    labelRow,
    labelBlankRow = 'New row',
    labelBlankRowNoRemove = 'This row is empty.',
    labelRemove,
    labelDuplicate,
    labelReorder,
    labelMoveUp,
    labelMoveDown,
    labelDropDone = 'Drop here',
    labelReorderHint = 'Press Enter to lift, then Arrow Up or Arrow Down to move, Enter to drop, Escape to cancel.',
    labelAdded,
    labelRemoved,
    labelDuplicated,
    labelMoved,
    labelAtStart = 'Already first.',
    labelAtEnd = 'Already last.',
    labelMinReached,
    labelMaxReached,
    labelErrorSummary,
    className,
    ...rest
  }: EditableListProps<T>,
  ref: ForwardedRef<HTMLDivElement>,
): ReactElement {
  const uid = useId();
  const hintId = `${uid}hint`;
  const listRef = useRef<HTMLUListElement | null>(null);
  const addRef = useRef<HTMLButtonElement | null>(null);

  const [items, setItems] = useControllableState<T[]>({
    value: itemsProp,
    defaultValue: defaultItems ?? [],
    onChange: onItemsChange,
  });

  const canInsert = createItem !== undefined;
  const showAdd = (addable ?? !trailingBlankRow) && canInsert;

  if (import.meta.env?.DEV && !canInsert && (addable === true || trailingBlankRow)) {
    console.error(
      '[stratum] <EditableList> cannot add rows without `createItem`. ' +
        'The add control and the trailing blank row are both suppressed.',
    );
  }

  /* -- Row identity ------------------------------------------------------- */
  const [ids, setIds] = useState<string[]>(() => items.map(() => nextRowId()));
  if (ids.length !== items.length) {
    // Adjusting state during render is the supported way to react to a prop
    // change without an extra commit. Only the length can drift here: every
    // internal mutation splices ids and items together in one handler.
    setIds((previous) => {
      if (previous.length === items.length) return previous;
      if (previous.length > items.length) return previous.slice(0, items.length);
      const next = previous.slice();
      while (next.length < items.length) next.push(nextRowId());
      return next;
    });
  }

  /* -- Announcements ------------------------------------------------------ */
  const [announcement, setAnnouncement] = useState('');

  /* -- Focus choreography ------------------------------------------------- */
  const [focusRequest, setFocusRequest] = useState<FocusRequest | null>(null);
  const focusTokenRef = useRef(0);
  const focusAttemptsRef = useRef(0);

  const requestRowFocus = useCallback(
    (
      index: number,
      field: number,
      caret: CaretPlacement = 'none',
      options?: { control?: 'field' | 'grip'; restoreOnly?: boolean },
    ) => {
      focusTokenRef.current += 1;
      focusAttemptsRef.current = 0;
      setFocusRequest({
        kind: 'row',
        index,
        field,
        caret,
        control: options?.control ?? 'field',
        restoreOnly: options?.restoreOnly ?? false,
        token: focusTokenRef.current,
      });
    },
    [],
  );

  const requestAddFocus = useCallback(() => {
    focusTokenRef.current += 1;
    focusAttemptsRef.current = 0;
    setFocusRequest({ kind: 'add', token: focusTokenRef.current });
  }, []);

  useIsomorphicLayoutEffect(() => {
    if (!focusRequest) return;

    const settle = () => {
      focusAttemptsRef.current = 0;
      setFocusRequest(null);
    };

    const attempt = (): boolean => {
      if (focusRequest.kind === 'add') {
        const button = addRef.current;
        if (button) {
          button.focus();
          return true;
        }
        // No add button (a fixed-length list): the first surviving field is
        // still infinitely better than `<body>`.
        const first = rowElement(listRef.current, 0);
        const field = first ? rowFields(first)[0] : undefined;
        if (field) {
          field.focus();
          return true;
        }
        return false;
      }

      if (focusRequest.restoreOnly) {
        const active = typeof document === 'undefined' ? null : document.activeElement;
        const root = listRef.current;
        if (active && root && root.contains(active)) return true;
      }

      const row = rowElement(listRef.current, focusRequest.index);
      if (!row) return false;

      if (focusRequest.control === 'grip') {
        const grip = row.querySelector<HTMLElement>('.stratum-editable-list__grip');
        if (grip) {
          grip.focus();
          return true;
        }
      }

      const fields = rowFields(row);
      if (fields.length === 0) return false;
      const target = fields[Math.min(focusRequest.field, fields.length - 1)] ?? fields[0];
      if (!target) return false;
      target.focus();
      placeCaret(target, focusRequest.caret);
      return true;
    };

    if (attempt()) {
      settle();
      return;
    }

    focusAttemptsRef.current += 1;
    if (focusAttemptsRef.current > 3) {
      settle();
      return;
    }

    const frame = requestAnimationFrame(() => {
      setFocusRequest((previous) =>
        previous && previous.token === focusRequest.token ? { ...previous } : previous,
      );
    });
    return () => cancelAnimationFrame(frame);
    // `items` is a dependency because a controlled parent may commit the new
    // rows a tick after the mutation that requested the focus.
  }, [focusRequest, items]);

  /* -- Mutations ---------------------------------------------------------- */
  const commit = useCallback(
    (nextItems: T[], nextIds: string[]) => {
      setIds(nextIds);
      setItems(nextItems);
    },
    [setItems],
  );

  const count = items.length;
  const lastItem = count > 0 ? items[count - 1] : undefined;
  const lastIsBlank = lastItem !== undefined && isItemEmpty(lastItem);
  /** The auto-appended row at the end is not yet a row the user made. */
  const ghostIndex = trailingBlankRow && count > 0 && lastIsBlank ? count - 1 : -1;
  const reportedCount = ghostIndex >= 0 ? count - 1 : count;

  const rowName = useCallback(
    (index: number) =>
      index === ghostIndex
        ? labelBlankRow
        : labelRow
          ? labelRow(index, reportedCount)
          : `${itemLabel} ${index + 1} of ${reportedCount}`,
    [ghostIndex, labelBlankRow, labelRow, itemLabel, reportedCount],
  );

  const insertAt = useCallback(
    (index: number, seed?: T) => {
      if (disabled || !createItem) return;
      if (maxRows !== undefined && items.length >= maxRows) {
        setAnnouncement(
          labelMaxReached ? labelMaxReached(maxRows) : `Cannot add more than ${maxRows} ${itemLabel.toLowerCase()}s.`,
        );
        return;
      }
      const at = Math.max(0, Math.min(index, items.length));
      const nextItems = items.slice();
      nextItems.splice(at, 0, seed ?? createItem());
      const nextIds = ids.slice();
      nextIds.splice(at, 0, nextRowId());
      commit(nextItems, nextIds);
      requestRowFocus(at, 0, 'start');
      setAnnouncement(
        labelAdded
          ? labelAdded(at, nextItems.length)
          : `${itemLabel} ${at + 1} added. ${nextItems.length} total.`,
      );
    },
    [
      commit,
      createItem,
      disabled,
      ids,
      itemLabel,
      items,
      labelAdded,
      labelMaxReached,
      maxRows,
      requestRowFocus,
    ],
  );

  const removeAt = useCallback(
    (index: number, mode: 'same-field' | 'previous-end') => {
      if (disabled || !removable) return;
      if (items.length <= minRows) {
        setAnnouncement(
          labelMinReached
            ? labelMinReached(minRows)
            : `At least ${minRows} ${itemLabel.toLowerCase()}${minRows === 1 ? '' : 's'} required.`,
        );
        return;
      }

      const row = rowElement(listRef.current, index);
      const field = fieldIndexOf(row, typeof document === 'undefined' ? null : document.activeElement);

      const nextItems = items.slice();
      nextItems.splice(index, 1);
      const nextIds = ids.slice();
      nextIds.splice(index, 1);
      commit(nextItems, nextIds);

      if (nextItems.length === 0) {
        requestAddFocus();
      } else if (mode === 'previous-end') {
        if (index === 0) requestRowFocus(0, 0, 'start');
        else requestRowFocus(index - 1, Number.MAX_SAFE_INTEGER, 'end');
      } else {
        requestRowFocus(Math.min(index, nextItems.length - 1), field, 'end');
      }

      setAnnouncement(
        labelRemoved
          ? labelRemoved(index, nextItems.length)
          : `${itemLabel} ${index + 1} removed. ${nextItems.length} remaining.`,
      );
    },
    [
      commit,
      disabled,
      ids,
      itemLabel,
      items,
      labelMinReached,
      labelRemoved,
      minRows,
      removable,
      requestAddFocus,
      requestRowFocus,
    ],
  );

  const duplicateAt = useCallback(
    (index: number) => {
      if (disabled) return;
      const source = items[index];
      if (source === undefined) return;
      if (maxRows !== undefined && items.length >= maxRows) {
        setAnnouncement(
          labelMaxReached ? labelMaxReached(maxRows) : `Cannot add more than ${maxRows} ${itemLabel.toLowerCase()}s.`,
        );
        return;
      }
      const nextItems = items.slice();
      nextItems.splice(index + 1, 0, cloneItem(source));
      const nextIds = ids.slice();
      nextIds.splice(index + 1, 0, nextRowId());
      commit(nextItems, nextIds);
      requestRowFocus(index + 1, 0, 'end');
      setAnnouncement(
        labelDuplicated
          ? labelDuplicated(index, nextItems.length)
          : `${itemLabel} ${index + 1} duplicated. ${nextItems.length} total.`,
      );
    },
    [
      cloneItem,
      commit,
      disabled,
      ids,
      itemLabel,
      items,
      labelDuplicated,
      labelMaxReached,
      maxRows,
      requestRowFocus,
    ],
  );

  const moveTo = useCallback(
    (
      from: number,
      to: number,
      focus: { field?: number; control?: 'field' | 'grip' } = {},
    ) => {
      if (disabled || !reorderable) return;
      const target = Math.max(0, Math.min(to, items.length - 1));
      if (target === from || from < 0 || from >= items.length) {
        setAnnouncement(to < from ? labelAtStart : labelAtEnd);
        return;
      }
      const nextItems = items.slice();
      const [moved] = nextItems.splice(from, 1);
      if (moved === undefined) return;
      nextItems.splice(target, 0, moved);

      const nextIds = ids.slice();
      const [movedId] = nextIds.splice(from, 1);
      nextIds.splice(target, 0, movedId ?? nextRowId());

      commit(nextItems, nextIds);
      // Focus normally rides along with the moved DOM node; this is the safety
      // net for a row renderer that recreates its fields on every render.
      requestRowFocus(target, focus.field ?? 0, 'none', {
        control: focus.control ?? 'field',
        restoreOnly: true,
      });
      setAnnouncement(
        labelMoved
          ? labelMoved(from, target, nextItems.length)
          : `${itemLabel} moved to position ${target + 1} of ${nextItems.length}.`,
      );
    },
    [
      commit,
      disabled,
      ids,
      itemLabel,
      items,
      labelAtEnd,
      labelAtStart,
      labelMoved,
      reorderable,
      requestRowFocus,
    ],
  );

  const updateAt = useCallback(
    (index: number, next: T | ((previous: T) => T)) => {
      const current = items[index];
      if (current === undefined) return;
      const resolved =
        typeof next === 'function' ? (next as (previous: T) => T)(current) : next;
      if (Object.is(resolved, current)) return;
      const nextItems = items.slice();
      nextItems[index] = resolved;
      setItems(nextItems);
    },
    [items, setItems],
  );

  /* -- Trailing blank row -------------------------------------------------- */
  const lastAutoAppendRef = useRef<T[] | null>(null);

  useEffect(() => {
    if (!trailingBlankRow || disabled || !createItem) return;
    if (maxRows !== undefined && items.length >= maxRows) return;
    const last = items.length > 0 ? items[items.length - 1] : undefined;
    if (last !== undefined && isItemEmpty(last)) return;
    // A controlled parent that ignores `onItemsChange` hands back the same
    // array, and appending to it again would spin. Keying on the array
    // identity refuses exactly that case while still allowing a genuine
    // second append after the parent has committed something new.
    if (lastAutoAppendRef.current === items) return;
    lastAutoAppendRef.current = items;
    // Silent: no announcement and no focus move. The ghost row is an
    // affordance, not an event, and announcing it on every keystroke in the
    // last row is exactly the noise that makes people turn live regions off.
    commit([...items, createItem()], [...ids, nextRowId()]);
  }, [trailingBlankRow, disabled, createItem, maxRows, items, ids, isItemEmpty, commit]);

  /* -- Drag and lift ------------------------------------------------------ */
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [dropTarget, setDropTarget] = useState<{ index: number; edge: 'before' | 'after' } | null>(
    null,
  );
  const [liftIndex, setLiftIndex] = useState<number | null>(null);
  const liftOriginRef = useRef<number | null>(null);

  /* POINTER DRAG, NOT HTML5 DRAG-AND-DROP.
   *
   * This used to run on `draggable` + dragstart/dragover/drop. That API cannot
   * produce the one thing a reorder is judged on: the browser owns the drag
   * image, so nothing moves under the pointer until you let go, and the rows
   * you are dragging past never move at all. It also has no touch support
   * whatsoever, which put this component in breach of the same WCAG 2.5.7
   * single-pointer requirement the rest of the list family satisfies.
   *
   * Pointer events plus the shared geometry in _shared/reorderGeometry give
   * live displacement — the dragged row tracks the pointer 1:1 and its
   * neighbours slide out of the way — and work on touch for free via
   * `setPointerCapture`. The keyboard model below is untouched. */
  const rowEls = useRef(new Map<number, HTMLElement>());
  const getRow = useCallback((i: number) => rowEls.current.get(i), []);
  const rowRefFns = useRef(new Map<number, (el: HTMLElement | null) => void>());
  const rowRef = useCallback((index: number) => {
    let fn = rowRefFns.current.get(index);
    if (!fn) {
      fn = (el: HTMLElement | null) => {
        if (el) rowEls.current.set(index, el);
        else rowEls.current.delete(index);
      };
      rowRefFns.current.set(index, fn);
    }
    return fn;
  }, []);

  const dragSession = useRef<{
    index: number;
    pointerId: number;
    startY: number;
    snapshot: ReorderSnapshot;
    target: number;
    dy: number;
  } | null>(null);

  const endDrag = useCallback(() => {
    const session = dragSession.current;
    if (session) clearShift(getRow, session.snapshot, session.index);
    dragSession.current = null;
    setDragIndex(null);
    setDropTarget(null);
  }, [getRow]);

  const handleGripPointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const session = dragSession.current;
    if (!session || session.pointerId !== event.pointerId) return;
    // Damped at the ends: free inside the list, progressively resisted past it.
    const dy = dampOffset(session.snapshot, session.index, event.clientY - session.startY);
    const target = resolveTarget(session.snapshot, session.index, dy);
    session.target = target;
    session.dy = dy;
    applyShift(getRow, session.snapshot, session.index, target, dy);
    setDropTarget((previous) => {
      if (target === session.index) return previous === null ? previous : null;
      const edge: 'before' | 'after' = target < session.index ? 'before' : 'after';
      return previous && previous.index === target && previous.edge === edge
        ? previous
        : { index: target, edge };
    });
  };

  const handleGripPointerUp = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const session = dragSession.current;
    if (!session || session.pointerId !== event.pointerId) return;
    const { index, target } = session;
    // Clearing the transforms in the same commit that reorders the array is a
    // visual no-op: the shift each row shows is exactly the layout delta the
    // reorder produces — provided neither half animates, hence the one-frame
    // suppression below.
    settleDrop(getRow, session.snapshot, index, target, session.dy);
    endDrag();
    if (target !== index) moveTo(index, target);
  };

  const dropLift = useCallback(() => {
    setLiftIndex(null);
    liftOriginRef.current = null;
  }, []);

  const handleGripKeyDown = (index: number) => (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (!reorderable || disabled) return;
    const lifted = liftIndex === index;

    if (event.key === 'Enter' || event.key === ' ' || event.key === 'Spacebar') {
      event.preventDefault();
      if (lifted) dropLift();
      else {
        liftOriginRef.current = index;
        setLiftIndex(index);
        setAnnouncement(labelReorderHint);
      }
      return;
    }

    if (event.key === 'Escape' && lifted) {
      event.preventDefault();
      event.stopPropagation();
      const origin = liftOriginRef.current;
      dropLift();
      if (origin !== null && origin !== index) moveTo(index, origin);
      return;
    }

    if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
      if (!lifted && !event.altKey) return;
      event.preventDefault();
      const to = index + (event.key === 'ArrowUp' ? -1 : 1);
      if (to < 0 || to >= items.length) {
        setAnnouncement(to < 0 ? labelAtStart : labelAtEnd);
        return;
      }
      moveTo(index, to, { control: 'grip' });
      if (lifted) setLiftIndex(to);
    }
  };

  const handleGripClick = (index: number) => () => {
    if (!reorderable || disabled) return;
    if (liftIndex === index) {
      dropLift();
      return;
    }
    liftOriginRef.current = index;
    setLiftIndex(index);
    setAnnouncement(labelReorderHint);
  };

  const handleGripPointerDown = (index: number) => (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (!reorderable || disabled || items.length < 2) return;
    if (event.button !== 0) return;
    const snapshot = snapshotRows(getRow, items.length);
    if (!snapshot) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    dragSession.current = {
      index,
      pointerId: event.pointerId,
      startY: event.clientY,
      snapshot,
      target: index,
      dy: 0,
    };
    setDragIndex(index);
    // Picking a row up is not a text selection gesture.
    event.preventDefault();
  };

  /* -- Field keyboard model ----------------------------------------------- */
  const handleFieldKeyDown = (index: number) => (event: ReactKeyboardEvent<HTMLElement>) => {
    if (disabled || event.defaultPrevented) return;
    const native = event.nativeEvent as unknown as { isComposing?: boolean };
    if (native.isComposing) return;
    const target = event.target as HTMLElement | null;
    if (!target) return;

    if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      if (!reorderable) return;
      event.preventDefault();
      const row = target.closest<HTMLElement>('[data-row-index]');
      moveTo(index, index + (event.key === 'ArrowUp' ? -1 : 1), {
        field: fieldIndexOf(row, target),
      });
      return;
    }

    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;

    if (event.key === 'Enter') {
      if (!canInsert || (!showAdd && !trailingBlankRow)) return;
      if (!acceptsInsertKey(target)) return;
      event.preventDefault();
      insertAt(index + 1);
      return;
    }

    if (event.key === 'Backspace' || event.key === 'Delete') {
      if (!removable) return;
      if (!isEmptyTextTarget(target)) return;
      const item = items[index];
      if (item === undefined || !isItemEmpty(item)) return;
      // The ghost row is not a row the user created, so Backspace in it does
      // nothing rather than deleting the row above.
      if (index === ghostIndex) return;
      event.preventDefault();
      removeAt(index, 'previous-end');
    }
  };

  /* -- Validation --------------------------------------------------------- */
  const errors = useMemo(() => {
    if (!validateItem) return [];
    return items.map((item, index) =>
      index === ghostIndex ? null : (validateItem(item, index, items) ?? null),
    );
  }, [ghostIndex, items, validateItem]);

  const invalidCount = errors.reduce<number>(
    (total, error) => (error != null && error !== false ? total + 1 : total),
    0,
  );

  const summaryNode =
    errorSummary === false
      ? null
      : errorSummary !== undefined
        ? errorSummary
        : invalidCount > 0
          ? (
              <InlineMessage variant="danger" size="xs" role="status">
                {labelErrorSummary
                  ? labelErrorSummary(invalidCount, reportedCount)
                  : `${invalidCount} of ${reportedCount} ${itemLabel.toLowerCase()}${
                      reportedCount === 1 ? '' : 's'
                    } need attention.`}
              </InlineMessage>
            )
          : null;

  const canAddMore = maxRows === undefined || items.length < maxRows;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="editable-list"
      data-size={size}
      data-disabled={disabled || undefined}
      data-reorderable={reorderable || undefined}
      className={clsx('stratum-editable-list', className)}
    >
      {summaryNode}

      {items.length === 0 && emptyState != null && emptyState !== false ? (
        <div className="stratum-editable-list__empty">{emptyState}</div>
      ) : (
        <ul
          ref={listRef}
          className="stratum-editable-list__rows"
          data-dragging={dragIndex !== null || undefined}
        >
          {items.map((item, index) => {
            const rowId = ids[index] ?? `${uid}row${index}`;
            const errorNode = errors[index] ?? null;
            const invalid = errorNode != null && errorNode !== false;
            const errorId = invalid ? `${rowId}error` : undefined;
            const isGhost = index === ghostIndex;
            const rowLabel = rowName(index);
            const canRemoveRow = removable && !disabled && items.length > minRows && !isGhost;
            const isFirst = index === 0;
            const isLast = index === items.length - 1;
            const lifted = liftIndex === index;

            const api: EditableListRowApi<T> = {
              item,
              index,
              count: items.length,
              rowId,
              rowLabel,
              isFirst,
              isLast,
              isBlank: isItemEmpty(item),
              invalid,
              error: errorNode,
              errorId,
              canRemove: canRemoveRow,
              canAdd: canInsert && canAddMore,
              canMoveUp: reorderable && !isFirst,
              canMoveDown: reorderable && !isLast,
              disabled,
              update: (next) => updateAt(index, next),
              remove: () => removeAt(index, 'same-field'),
              duplicate: () => duplicateAt(index),
              moveUp: () => moveTo(index, index - 1),
              moveDown: () => moveTo(index, index + 1),
              moveTo: (to) => moveTo(index, to),
              insertAfter: (seed) => insertAt(index + 1, seed),
              fieldProps: {
                onKeyDown: handleFieldKeyDown(index),
                'aria-invalid': invalid || undefined,
                'aria-describedby': errorId,
              },
            };

            return (
              <li
                key={rowId}
                className="stratum-editable-list__row"
                data-row-index={index}
                data-invalid={invalid || undefined}
                data-ghost={isGhost || undefined}
                data-dragging={dragIndex === index || undefined}
                data-lifted={lifted || undefined}
                data-drop-edge={
                  dropTarget && dropTarget.index === index && dragIndex !== null
                    ? dropTarget.edge
                    : undefined
                }
                ref={rowRef(index)}
              >
                {(showRank || reorderable) && (
                  <div className="stratum-editable-list__gutter" data-list-control="">
                    {showRank && (
                      <span className="stratum-editable-list__rank" aria-hidden="true">
                        {isGhost ? '' : index + 1}
                      </span>
                    )}
                    {reorderable && (
                      <button
                        type="button"
                        className="stratum-editable-list__grip"
                        aria-label={
                          labelReorder
                            ? labelReorder(index, reportedCount)
                            : `Reorder ${itemLabel.toLowerCase()} ${index + 1}`
                        }
                        aria-pressed={lifted}
                        aria-describedby={hintId}
                        disabled={disabled || items.length < 2}
                        onPointerDown={handleGripPointerDown(index)}
                        onPointerMove={handleGripPointerMove}
                        onPointerUp={handleGripPointerUp}
                        onPointerCancel={endDrag}
                        onLostPointerCapture={endDrag}
                        onKeyDown={handleGripKeyDown(index)}
                        onClick={handleGripClick(index)}
                      >
                        <GripIcon />
                      </button>
                    )}
                  </div>
                )}

                <div
                  className="stratum-editable-list__fields"
                  role="group"
                  aria-label={rowLabel}
                >
                  {renderRow(item, api)}
                  {invalid && (
                    <InlineMessage id={errorId} variant="danger" size="xs" role="none">
                      {errorNode}
                    </InlineMessage>
                  )}
                </div>

                <div className="stratum-editable-list__actions" data-list-control="">
                  {lifted && (
                    <>
                      <button
                        type="button"
                        className="stratum-editable-list__action"
                        aria-label={
                          labelMoveUp
                            ? labelMoveUp(index, reportedCount)
                            : `Move ${itemLabel.toLowerCase()} ${index + 1} up`
                        }
                        aria-disabled={isFirst || undefined}
                        onClick={() => {
                          if (isFirst) {
                            setAnnouncement(labelAtStart);
                            return;
                          }
                          moveTo(index, index - 1, { control: 'grip' });
                          setLiftIndex(index - 1);
                        }}
                      >
                        <ArrowUpIcon />
                      </button>
                      <button
                        type="button"
                        className="stratum-editable-list__action"
                        aria-label={
                          labelMoveDown
                            ? labelMoveDown(index, reportedCount)
                            : `Move ${itemLabel.toLowerCase()} ${index + 1} down`
                        }
                        aria-disabled={isLast || undefined}
                        onClick={() => {
                          if (isLast) {
                            setAnnouncement(labelAtEnd);
                            return;
                          }
                          moveTo(index, index + 1, { control: 'grip' });
                          setLiftIndex(index + 1);
                        }}
                      >
                        <ArrowDownIcon />
                      </button>
                      <button
                        type="button"
                        className="stratum-editable-list__action"
                        data-tone="accent"
                        aria-label={labelDropDone}
                        onClick={dropLift}
                      >
                        <CheckIcon />
                      </button>
                    </>
                  )}

                  {duplicable && (
                    <button
                      type="button"
                      className="stratum-editable-list__action"
                      aria-label={
                        labelDuplicate
                          ? labelDuplicate(index, reportedCount)
                          : `Duplicate ${itemLabel.toLowerCase()} ${index + 1}`
                      }
                      aria-disabled={!canAddMore || disabled || undefined}
                      onClick={() => duplicateAt(index)}
                    >
                      <DuplicateIcon />
                    </button>
                  )}

                  {removable && (
                    <button
                      type="button"
                      className="stratum-editable-list__action"
                      data-tone="danger"
                      aria-label={
                        labelRemove
                          ? labelRemove(index, reportedCount)
                          : `Remove ${itemLabel.toLowerCase()} ${index + 1}`
                      }
                      // `aria-disabled`, never `disabled`: a control that drops
                      // out of the tab order the moment you hit the floor moves
                      // focus out from under the user, and a control that
                      // vanishes teaches nothing about why.
                      aria-disabled={!canRemoveRow || undefined}
                      onClick={() => {
                        if (isGhost) {
                          setAnnouncement(labelBlankRowNoRemove);
                          return;
                        }
                        removeAt(index, 'same-field');
                      }}
                    >
                      <RemoveIcon />
                    </button>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {showAdd && (
        <button
          ref={addRef}
          type="button"
          className="stratum-editable-list__add"
          disabled={disabled}
          aria-disabled={!canAddMore || undefined}
          onClick={() => insertAt(items.length)}
        >
          <PlusIcon />
          <span>{addLabel ?? `Add ${itemLabel.toLowerCase()}`}</span>
        </button>
      )}

      {reorderable && (
        <VisuallyHidden id={hintId}>{labelReorderHint}</VisuallyHidden>
      )}

      {/* Mounted for the lifetime of the list, not created alongside its first
          message: a live region inserted at the same moment as its content is
          frequently not announced at all. */}
      <VisuallyHidden role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </VisuallyHidden>
    </div>
  );
}

/**
 * `forwardRef` erases generics, so the ref-forwarding component is re-typed as
 * a generic function. The runtime value is an ordinary `forwardRef` result.
 */
export const EditableList = forwardRef(EditableListInner) as <T>(
  props: EditableListProps<T> & { ref?: Ref<HTMLDivElement> },
) => ReactElement;

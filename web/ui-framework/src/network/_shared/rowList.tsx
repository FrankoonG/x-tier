/* ---------------------------------------------------------------------------
 * Shared substrate for the ORDERED list family.
 *
 * NOT exported from the package. PriorityList, AddressList and RangeList are
 * three skins on one state machine, and ChainBuilder shares half of it. Writing
 * that machine once is not a tidiness preference — it is the only way the focus
 * bug below gets fixed once instead of four times.
 *
 * THE THREE THINGS EVERY ORDERED LIST GETS WRONG
 * ----------------------------------------------
 *  1. FOCUS ON REMOVAL. Deleting the row that currently holds focus sends focus
 *     to `<body>`, which drops a keyboard user out of the form entirely and
 *     gives a screen-reader user no indication anything happened. The policy
 *     here is explicit and testable: focus goes to the SAME control in the next
 *     row, else the previous row, else the add button. `useFocusRelay` exists to
 *     make that a two-line call at each site.
 *
 *  2. ANNOUNCEMENT. A row count that changes, or an entry that moves from
 *     position 4 to position 2, is a state change with no visual event a screen
 *     reader can observe. Every mutation goes through `announce()`, and the live
 *     region is mounted permanently — a region inserted at the same moment as
 *     its first message frequently is not announced at all, because assistive
 *     technology has nothing to diff against. Same reasoning as Toast.
 *
 *  3. REORDER WITH A POINTER ONLY. Keyboard parity does NOT discharge the
 *     obligation: WCAG 2.2 SC 2.5.7 (Dragging Movements, AA) requires a
 *     SINGLE-POINTER alternative and is assessed independently of 2.1.1, because
 *     a touchscreen user may have no keyboard at all. So there are three input
 *     paths, always, and they emit the same event:
 *
 *       a. pointer drag from the grip           (mouse, pen, touch)
 *       b. keyboard lift/drop on the grip       Space/Enter, arrows, Home/End,
 *                                               Escape to cancel
 *       c. Alt+ArrowUp / Alt+ArrowDown anywhere in the row, and explicit
 *          move-up / move-down BUTTONS          (single pointer, no drag)
 *
 * WHY `aria-grabbed` IS NOT USED
 * ------------------------------
 * It is deprecated in ARIA 1.1 and was never implemented consistently. The
 * grabbed state is carried by a live-region announcement plus a `data-grabbed`
 * attribute for styling, which is what actually reaches a user.
 *
 * WHY THERE IS NO GLOBAL `user-select` CLASS
 * ------------------------------------------
 * The pattern this family replaces set a class on `document.body` at drag start
 * and removed it at drag end, with no cleanup path — so a row that unmounted
 * mid-drag left the whole document unselectable forever. Here the rule is scoped
 * to the list root (`[data-dragging]`) and the drag uses pointer capture, so
 * losing the element cancels the drag through `lostpointercapture` and nothing
 * can outlive the component.
 * ------------------------------------------------------------------------- */
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';
import clsx from 'clsx';
import { useEventCallback } from '../../hooks/useEventCallback';
import {
  applyShift,
  clearShift,
  dampOffset,
  resolveTarget,
  settleDrop,
  snapshotRows,
  type ReorderSnapshot,
} from '../../_shared/reorderGeometry';
import './rowList.css';

/* ========================================================================== */
/* Live region                                                                */
/* ========================================================================== */

/** Invisible marker appended to force a repeat announcement. */
const REPEAT_MARK = '\u200B';

export interface Announcer {
  /** Current message. Pass to `<ListAnnouncer>`. */
  message: string;
  announce: (text: string) => void;
}

/**
 * Politely announced status text for a list.
 *
 * The zero-width space is deliberate. Moving a row twice in the same direction
 * produces the same sentence twice ("moved to position 3 of 7"), and a live
 * region whose text content is unchanged is not re-announced — the operator
 * would hear the first move and silence for the second. Appending an invisible
 * character forces a diff without changing what is read aloud.
 */
export function useAnnouncer(): Announcer {
  const [message, setMessage] = useState('');
  const announce = useCallback((text: string) => {
    setMessage((prev) => {
      const bare = prev.endsWith(REPEAT_MARK) ? prev.slice(0, -REPEAT_MARK.length) : prev;
      return bare === text ? `${text}${REPEAT_MARK}` : text;
    });
  }, []);
  return { message, announce };
}

export function ListAnnouncer({ message }: { message: string }) {
  return (
    <span
      className="stratum-visually-hidden"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      data-stratum="list-announcer"
    >
      {message}
    </span>
  );
}

/* ========================================================================== */
/* Focus relay                                                                */
/* ========================================================================== */

export interface FocusRelay {
  /** Ref callback registering the element that `key` names. */
  register: (key: string) => (el: HTMLElement | null) => void;
  /** Focus `key` once React has committed the new tree. */
  requestFocus: (key: string) => void;
}

/**
 * Moves focus to a named element after the render that removed or added a row.
 *
 * Keys are the caller's convention — `field:<id>`, `handle:<id>`, `add`. The
 * request is honoured in an effect rather than inline, because at the moment the
 * caller decides where focus should go the target may not exist yet.
 */
export function useFocusRelay(): FocusRelay {
  const nodes = useRef(new Map<string, HTMLElement>());
  const callbacks = useRef(new Map<string, (el: HTMLElement | null) => void>());
  const [pending, setPending] = useState<string | null>(null);

  useEffect(() => {
    if (pending === null) return;
    // The row may have been removed between the request and this effect, in
    // which case nothing is focused and focus stays where the browser put it.
    nodes.current.get(pending)?.focus();
    setPending(null);
  }, [pending]);

  const register = useCallback((key: string) => {
    let fn = callbacks.current.get(key);
    if (!fn) {
      fn = (el: HTMLElement | null) => {
        if (el) nodes.current.set(key, el);
        else nodes.current.delete(key);
      };
      callbacks.current.set(key, fn);
    }
    return fn;
  }, []);

  const requestFocus = useCallback((key: string) => setPending(key), []);

  return { register, requestFocus };
}

/* ========================================================================== */
/* Reorder                                                                    */
/* ========================================================================== */

export interface ReorderLabels {
  /** Accessible name of the drag handle. Positions are 1-based. */
  handle: (position: number, total: number) => string;
  moveUp: (position: number, total: number) => string;
  moveDown: (position: number, total: number) => string;
  moveTop: (position: number, total: number) => string;
  moveBottom: (position: number, total: number) => string;
  /** Announced after any successful move. */
  moved: (position: number, total: number) => string;
  /** Announced when a move is refused because the row is already at an end. */
  atStart: string;
  atEnd: string;
  grabbed: (position: number, total: number) => string;
  dropped: (position: number, total: number) => string;
  cancelled: (position: number, total: number) => string;
  /** Description attached to the handle explaining the keyboard model. */
  instructions: string;
}

export const DEFAULT_REORDER_LABELS: ReorderLabels = {
  handle: (p, t) => `Reorder, position ${p} of ${t}`,
  moveUp: (p, t) => `Move up, from position ${p} of ${t}`,
  moveDown: (p, t) => `Move down, from position ${p} of ${t}`,
  moveTop: (p, t) => `Move to top, from position ${p} of ${t}`,
  moveBottom: (p, t) => `Move to bottom, from position ${p} of ${t}`,
  moved: (p, t) => `Moved to position ${p} of ${t}`,
  atStart: 'Already first',
  atEnd: 'Already last',
  grabbed: (p, t) =>
    `Grabbed, position ${p} of ${t}. Use the arrow keys to move, Enter to drop, Escape to cancel.`,
  dropped: (p, t) => `Dropped at position ${p} of ${t}`,
  cancelled: (p, t) => `Reorder cancelled, back at position ${p} of ${t}`,
  instructions:
    'Press Space to pick up, arrow keys to move, Enter to drop, Escape to cancel. ' +
    'Alt with the up and down arrows moves the row without picking it up.',
};

export interface UseReorderOptions {
  count: number;
  /** Emitted by all three input paths. Indices are 0-based. */
  onMove: (from: number, to: number) => void;
  announce: (text: string) => void;
  labels: ReorderLabels;
  /** Suppresses every path. The controls should not be rendered at all. */
  disabled?: boolean;
}

/** Props for the row element. Spread onto the `<li>`. */
export interface ReorderRowProps {
  ref: (el: HTMLElement | null) => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  /**
   * Reserved for a consumer's own inline style. The drag offset is NOT passed
   * through here any more: it is written to the element as `--_drag-dy` /
   * `--_shift` during the gesture, because a pointermove that goes through
   * React state cannot keep up with a finger.
   */
  style?: CSSProperties | undefined;
  'data-dragging': true | undefined;
  'data-grabbed': true | undefined;
  /** Side the drop indicator is drawn on while a drag is over this row. */
  'data-drop': 'before' | 'after' | undefined;
}

/** Props for the grip button. */
export interface ReorderHandleProps {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: () => void;
  onLostPointerCapture: () => void;
  onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  onBlur: () => void;
  'aria-label': string;
  'aria-describedby': string;
  'data-grabbed': true | undefined;
}

export interface ReorderApi {
  /** True while a pointer drag is in progress anywhere in the list. */
  dragging: boolean;
  instructionsId: string;
  instructions: string;
  getRowProps: (index: number) => ReorderRowProps;
  getHandleProps: (index: number) => ReorderHandleProps;
  /** Single-pointer path. `delta` of -1/+1, or ±Infinity for the ends. */
  moveBy: (index: number, delta: number) => void;
}

interface DragSession {
  index: number;
  pointerId: number;
  startY: number;
  /** Frozen row geometry. See _shared/reorderGeometry for why it is frozen. */
  snapshot: ReorderSnapshot;
  /** Latest computed drop index, kept out of state so `pointerup` cannot race. */
  target: number;
  /** Latest damped offset. The drop springs whatever is left of it. */
  dy: number;
}

/**
 * The reorder state machine: pointer drag, keyboard lift/drop, and Alt+Arrow.
 *
 * Row geometry is snapshotted at drag start rather than measured per move. A
 * snapshot means a poll that re-renders the list mid-drag cannot make the drop
 * target jump, and it is what makes live sibling displacement safe: the rows
 * move visually while the arithmetic keeps referring to where they started, so
 * the geometry cannot feed back into itself and the target cannot oscillate.
 *
 * LIVE DISPLACEMENT
 * -----------------
 * The rows between the origin and the current target slide out of the way under
 * the pointer, by exactly one dragged-row height plus the gap. That single
 * behaviour is what "real-time" means to an operator — not the array changing.
 * The commit is still deferred to `pointerup`, deliberately: the displacement
 * each row is already showing equals the layout delta the commit will produce,
 * so clearing the transforms in the same frame as the reorder is visually a
 * no-op, whereas committing continuously would rewrite the list under the
 * pointer and invalidate the very snapshot that keeps the target stable.
 *
 * Both the dragged row's offset and the siblings' shifts are written straight
 * to the DOM as custom properties rather than routed through React state. A
 * pointermove that re-renders the whole list cannot keep up with a finger, and
 * this is the same reason framer-motion's Reorder uses motion values. React
 * state holds only "is a drag in progress".
 *
 * The snapshot is also why there is no edge auto-scroll: the drop targets are
 * fixed at pick-up, so scrolling mid-drag would move them out from under the
 * arithmetic. A list long enough to need scrolling is a list where the move
 * buttons and Alt+Arrow are the better instrument anyway, and both work at any
 * length.
 */
export function useReorder({
  count,
  onMove,
  announce,
  labels,
  disabled = false,
}: UseReorderOptions): ReorderApi {
  const uid = useId();
  const instructionsId = `${uid}reorder-help`;

  const rows = useRef(new Map<number, HTMLElement>());
  // Memoised per index so a row does not detach and re-attach its ref on every
  // render — which would otherwise clear the geometry map mid-drag.
  const rowRefs = useRef(new Map<number, (el: HTMLElement | null) => void>());

  const session = useRef<DragSession | null>(null);
  const [drag, setDrag] = useState<{ index: number; target: number; dy: number } | null>(null);
  const [grabbed, setGrabbed] = useState<{ index: number; origin: number } | null>(null);

  const emitMove = useEventCallback(onMove);
  const say = useEventCallback(announce);

  const move = useCallback(
    (from: number, to: number): boolean => {
      if (to < 0) {
        say(labels.atStart);
        return false;
      }
      if (to > count - 1) {
        say(labels.atEnd);
        return false;
      }
      if (to === from) return false;
      emitMove(from, to);
      say(labels.moved(to + 1, count));
      return true;
    },
    [count, labels, emitMove, say],
  );

  const moveBy = useCallback(
    (index: number, delta: number) => {
      const to = delta === Infinity ? count - 1 : delta === -Infinity ? 0 : index + delta;
      if (to === index) {
        // "Move to top" from the top and "move up" from the top are the same
        // refusal, and both have to say something — silence reads as a bug.
        say(index <= 0 ? labels.atStart : labels.atEnd);
        return;
      }
      if (move(index, to)) {
        setGrabbed((g) => (g && g.index === index ? { index: to, origin: g.origin } : g));
      }
    },
    [count, labels, move, say],
  );

  /* -- Pointer drag ------------------------------------------------------- */

  /** Writes the dragged row's offset and every sibling's shift straight to the DOM. */
  const getRow = useCallback((i: number) => rows.current.get(i), []);

  const paint = useCallback(
    (s: DragSession, dy: number) => applyShift(getRow, s.snapshot, s.index, s.target, dy),
    [getRow],
  );

  const clearPaint = useCallback(
    (s: DragSession) => clearShift(getRow, s.snapshot, s.index),
    [getRow],
  );

  const endDrag = useCallback(() => {
    session.current = null;
    setDrag(null);
  }, []);

  const cancelDrag = useCallback(() => {
    const s = session.current;
    if (!s) return;
    clearPaint(s);
    endDrag();
    say(labels.cancelled(s.index + 1, count));
  }, [clearPaint, endDrag, say, labels, count]);

  // Escape during a pointer drag aborts it. Registered only while dragging, so
  // there is no listener to leak and no way for a stale one to fire.
  useEffect(() => {
    if (!drag) return undefined;
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      cancelDrag();
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [drag, cancelDrag]);

  const onPointerDown = useCallback(
    (index: number, event: ReactPointerEvent<HTMLElement>) => {
      if (disabled || count < 2) return;
      // Primary button only; a right-click must still open a context menu.
      if (event.button !== 0) return;

      const snapshot = snapshotRows(getRow, count);
      if (!snapshot) return;

      event.currentTarget.setPointerCapture(event.pointerId);
      session.current = {
        index,
        pointerId: event.pointerId,
        startY: event.clientY,
        snapshot,
        target: index,
        dy: 0,
      };
      setDrag({ index, target: index, dy: 0 });
      // Picking a row up is not a text selection gesture.
      event.preventDefault();
    },
    [disabled, count],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const s = session.current;
      if (!s || s.pointerId !== event.pointerId) return;

      // Damped at the ends: free inside the list, progressively resisted past
      // it, so the row cannot be dragged off into space.
      const dy = dampOffset(s.snapshot, s.index, event.clientY - s.startY);
      s.target = resolveTarget(s.snapshot, s.index, dy);
      s.dy = dy;
      paint(s, dy);
    },
    [paint],
  );

  const onPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const s = session.current;
      if (!s || s.pointerId !== event.pointerId) return;
      const { index, target } = s;
      // Clear the transforms in the same commit that reorders the array. The
      // shift each row is showing is exactly the layout delta the reorder is
      // about to apply, so the two cancel and nothing visibly jumps — but only
      // if neither half is animated, hence the one-frame suppression.
      settleDrop(getRow, s.snapshot, index, target, s.dy);
      endDrag();
      if (target !== index) move(index, target);
    },
    [clearPaint, endDrag, move],
  );

  /* -- Keyboard ----------------------------------------------------------- */

  const handleKeyDown = useCallback(
    (index: number, event: ReactKeyboardEvent<HTMLElement>) => {
      if (disabled) return;
      const isGrabbed = grabbed?.index === index;

      if (event.key === ' ' || event.key === 'Enter') {
        if (count < 2) return;
        event.preventDefault();
        if (isGrabbed) {
          setGrabbed(null);
          say(labels.dropped(index + 1, count));
        } else {
          setGrabbed({ index, origin: index });
          say(labels.grabbed(index + 1, count));
        }
        return;
      }

      if (event.key === 'Escape' && grabbed && isGrabbed) {
        event.preventDefault();
        // Escape restores the row to where it started rather than leaving it
        // wherever the arrows happened to have taken it — a cancel that does
        // not undo is not a cancel.
        if (grabbed.origin !== index) emitMove(index, grabbed.origin);
        setGrabbed(null);
        say(labels.cancelled(grabbed.origin + 1, count));
        return;
      }

      const alt = event.altKey && !event.ctrlKey && !event.metaKey;
      if (!isGrabbed && !alt) return;
      // A control inside the row that already acted on this key wins.
      if (event.defaultPrevented) return;

      switch (event.key) {
        case 'ArrowUp':
          event.preventDefault();
          moveBy(index, -1);
          return;
        case 'ArrowDown':
          event.preventDefault();
          moveBy(index, 1);
          return;
        case 'Home':
          event.preventDefault();
          moveBy(index, -Infinity);
          return;
        case 'End':
          event.preventDefault();
          moveBy(index, Infinity);
          return;
        default:
      }
    },
    [disabled, grabbed, count, labels, say, emitMove, moveBy],
  );

  const rowKeyDown = useCallback(
    (index: number, event: ReactKeyboardEvent<HTMLElement>) => {
      if (disabled) return;
      if (!event.altKey || event.ctrlKey || event.metaKey) return;
      if (event.defaultPrevented) return;
      if (event.key !== 'ArrowUp' && event.key !== 'ArrowDown') return;
      event.preventDefault();
      moveBy(index, event.key === 'ArrowUp' ? -1 : 1);
    },
    [disabled, moveBy],
  );

  /* -- Prop builders ------------------------------------------------------ */

  const rowRef = useCallback((index: number) => {
    let fn = rowRefs.current.get(index);
    if (!fn) {
      fn = (el: HTMLElement | null) => {
        if (el) rows.current.set(index, el);
        else rows.current.delete(index);
      };
      rowRefs.current.set(index, fn);
    }
    return fn;
  }, []);

  const getRowProps = useCallback(
    (index: number): ReorderRowProps => {
      const active = drag;
      const isDragged = active?.index === index;
      const drop: 'before' | 'after' | undefined =
        active && active.target === index && active.index !== index
          ? active.index > index
            ? 'before'
            : 'after'
          : undefined;
      return {
        ref: rowRef(index),
        onKeyDown: (event) => rowKeyDown(index, event),
        'data-dragging': isDragged || undefined,
        'data-grabbed': grabbed?.index === index || undefined,
        'data-drop': drop,
      };
    },
    [drag, grabbed, rowRef, rowKeyDown],
  );

  const getHandleProps = useCallback(
    (index: number): ReorderHandleProps => ({
      onPointerDown: (event) => onPointerDown(index, event),
      onPointerMove,
      onPointerUp,
      onPointerCancel: cancelDrag,
      onLostPointerCapture: endDrag,
      onKeyDown: (event) => handleKeyDown(index, event),
      // A grab that survives losing focus is a trap: the arrows would keep
      // moving a row the operator is no longer looking at.
      onBlur: () => setGrabbed(null),
      'aria-label': labels.handle(index + 1, count),
      'aria-describedby': instructionsId,
      'data-grabbed': grabbed?.index === index || undefined,
    }),
    [
      onPointerDown,
      onPointerMove,
      onPointerUp,
      cancelDrag,
      endDrag,
      handleKeyDown,
      grabbed,
      labels,
      count,
      instructionsId,
    ],
  );

  return {
    dragging: drag != null,
    instructionsId,
    instructions: labels.instructions,
    getRowProps,
    getHandleProps,
    moveBy,
  };
}

/** Merges caller overrides into the default reorder vocabulary, memoised. */
export function useReorderLabels(overrides?: Partial<ReorderLabels>): ReorderLabels {
  return useMemo(() => ({ ...DEFAULT_REORDER_LABELS, ...overrides }), [overrides]);
}

/* ========================================================================== */
/* Presentational parts                                                       */
/* ========================================================================== */

const GripIcon = (
  <svg viewBox="0 0 16 16" width="1em" height="1em" focusable="false" aria-hidden="true">
    <g fill="currentColor">
      <circle cx="6" cy="4" r="1.15" />
      <circle cx="10" cy="4" r="1.15" />
      <circle cx="6" cy="8" r="1.15" />
      <circle cx="10" cy="8" r="1.15" />
      <circle cx="6" cy="12" r="1.15" />
      <circle cx="10" cy="12" r="1.15" />
    </g>
  </svg>
);

const strokeIcon = (d: string) => (
  <svg viewBox="0 0 16 16" width="1em" height="1em" fill="none" focusable="false" aria-hidden="true">
    <path d={d} stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const ChevronUp = strokeIcon('m4 10 4-4 4 4');
const ChevronDown = strokeIcon('m4 6 4 4 4-4');
const ChevronTop = strokeIcon('M4 3.5h8M4 11l4-4 4 4');
const ChevronBottom = strokeIcon('M4 12.5h8M4 5l4 4 4-4');

export const RemoveIcon = strokeIcon('M4.5 4.5 11.5 11.5M11.5 4.5 4.5 11.5');
export const AddIcon = strokeIcon('M8 3.5v9M3.5 8h9');
export const InsertIcon = strokeIcon('M8 4.5v7M4.5 8h7');

export interface ReorderControlsProps {
  index: number;
  total: number;
  api: ReorderApi;
  labels: ReorderLabels;
  /** Adds move-to-top / move-to-bottom buttons. Default `false`. */
  showEdgeMoves?: boolean;
  /** Renders an inert spacer for a row that must not move. */
  locked?: boolean;
}

/**
 * The reorder cluster: grip, move up, move down, optionally move to the ends.
 *
 * THE BUTTONS ARE NEVER DISABLED AT THE ENDS.
 * A disabled button leaves the tab order, so a keyboard user tabbing down a
 * list would find the control vanish from row 1 and reappear at row 2, which
 * reads as a rendering fault. Instead the button stays focusable and answers
 * "already first" through the live region — a control that explains the limit
 * teaches it; one that disappears does not.
 */
export function ReorderControls({
  index,
  total,
  api,
  labels,
  showEdgeMoves = false,
  locked = false,
}: ReorderControlsProps) {
  if (locked) {
    return <span className="stratum-row-list__reorder" data-locked="true" aria-hidden="true" />;
  }
  const handleProps = api.getHandleProps(index);
  return (
    <span className="stratum-row-list__reorder">
      <button {...handleProps} type="button" className="stratum-row-list__grip">
        {GripIcon}
      </button>
      <span className="stratum-row-list__nudge">
        {showEdgeMoves && (
          <button
            type="button"
            className="stratum-row-list__move"
            aria-label={labels.moveTop(index + 1, total)}
            onClick={() => api.moveBy(index, -Infinity)}
          >
            {ChevronTop}
          </button>
        )}
        <button
          type="button"
          className="stratum-row-list__move"
          aria-label={labels.moveUp(index + 1, total)}
          onClick={() => api.moveBy(index, -1)}
        >
          {ChevronUp}
        </button>
        <button
          type="button"
          className="stratum-row-list__move"
          aria-label={labels.moveDown(index + 1, total)}
          onClick={() => api.moveBy(index, 1)}
        >
          {ChevronDown}
        </button>
        {showEdgeMoves && (
          <button
            type="button"
            className="stratum-row-list__move"
            aria-label={labels.moveBottom(index + 1, total)}
            onClick={() => api.moveBy(index, Infinity)}
          >
            {ChevronBottom}
          </button>
        )}
      </span>
    </span>
  );
}

export interface AddRowButtonProps {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  /** Reason the control is unavailable, e.g. the row ceiling. */
  disabledReason?: string;
  buttonRef?: (el: HTMLElement | null) => void;
  className?: string;
}

/**
 * The full-width dashed "add" plate.
 *
 * A REAL BUTTON, because the pattern this replaces used `<div onClick>` — the
 * largest, most prominent target in the form and the only one no keyboard could
 * reach. Its own label is the accessible name; `disabledReason` is wired through
 * `aria-describedby` so hitting the ceiling explains itself.
 */
export function AddRowButton({
  label,
  onClick,
  disabled = false,
  disabledReason,
  buttonRef,
  className,
}: AddRowButtonProps) {
  const uid = useId();
  const reasonId = `${uid}reason`;
  return (
    <div className="stratum-row-add-wrap">
      <button
        type="button"
        ref={buttonRef}
        className={clsx('stratum-row-add', className)}
        onClick={onClick}
        disabled={disabled}
        aria-describedby={disabled && disabledReason ? reasonId : undefined}
      >
        <span className="stratum-row-add__icon" aria-hidden="true">
          {AddIcon}
        </span>
        {label}
      </button>
      {disabled && disabledReason && (
        <span id={reasonId} className="stratum-row-list__ceiling">
          {disabledReason}
        </span>
      )}
    </div>
  );
}

/* ========================================================================== */
/* Pure helpers                                                               */
/* ========================================================================== */

/** Moves `from` to `to`, returning a new array. Out-of-range indices are a no-op. */
export function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
  if (from === to) return items.slice();
  if (from < 0 || from >= items.length || to < 0 || to >= items.length) return items.slice();
  const next = items.slice();
  const [entry] = next.splice(from, 1);
  if (entry === undefined) return items.slice();
  next.splice(to, 0, entry);
  return next;
}

/**
 * Splits a pasted block into candidate values.
 *
 * Operator data arrives as a block: a column out of a spreadsheet, a comma list
 * out of a config file, a set of lines out of a terminal. Splitting on newline,
 * comma, semicolon and whitespace covers all three. Nothing is validated here —
 * rejected fragments become rows the operator can fix, which is the whole point.
 * None of the value shapes this family accepts may contain a space, so splitting
 * on whitespace is safe; a shape that could would need its own splitter.
 */
export function splitPastedBlock(text: string): string[] {
  return text
    .split(/[\s,;]+/)
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
}

/** `true` when the pasted text carries more than one value. */
export function looksLikeBlock(text: string): boolean {
  return splitPastedBlock(text).length > 1;
}

let rowSeq = 0;

/**
 * Id for a row the component created itself.
 *
 * Monotonic rather than random so two rows created in the same tick cannot
 * collide, and prefixed so ids from different lists never compare equal.
 */
export function makeRowId(prefix: string): string {
  rowSeq += 1;
  return `${prefix}-${rowSeq}`;
}

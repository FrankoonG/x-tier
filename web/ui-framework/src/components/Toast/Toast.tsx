import {
  createContext,
  forwardRef,
  isValidElement,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { FloatingPortal } from '@floating-ui/react';
import { usePresence, type PresenceState } from '../../hooks/usePresence';
import { useEventCallback } from '../../hooks/useEventCallback';
import { statusGlyph, type StatusTone } from '../_shared/statusIcons';
import './Toast.css';

/* =========================================================================
 * Types
 * ====================================================================== */

export type ToastVariant = 'success' | 'error' | 'warning' | 'info' | 'neutral' | 'live';

export type ToastPlacement =
  | 'top-left'
  | 'top-center'
  | 'top-right'
  | 'bottom-left'
  | 'bottom-center'
  | 'bottom-right';

export type ToastSwipeDirection = 'left' | 'right' | 'up' | 'down' | 'none';

/** Why a toast went away. Passed to `onDismiss` so callers can tell an
 *  ignored notification from an acknowledged one. */
export type ToastDismissReason = 'timeout' | 'action' | 'user' | 'swipe' | 'api';

export interface ToastAction {
  label: string;
  onClick: (event: ReactMouseEvent<HTMLButtonElement>) => void;
  /** Keep the toast open after the action fires. Default `false`. */
  keepOpen?: boolean;
}

export interface ToastOptions {
  /** Supply to update or de-duplicate an existing toast. Generated otherwise. */
  id?: string;
  title?: ReactNode;
  /** Accepts any node — links, `<code>`, counts. Not restricted to a string. */
  message?: ReactNode;
  variant?: ToastVariant;
  /**
   * Milliseconds before auto-dismiss. `null` pins the toast open.
   * Defaults come from the provider: `liveDuration` for `'live'`, `duration`
   * otherwise, and pinned open when the toast has an `action` and
   * `stickyWithAction` is on.
   */
  duration?: number | null;
  /** `null` suppresses the default variant glyph entirely. */
  icon?: ReactNode | null;
  action?: ToastAction;
  /** Default `true`. `false` removes the dismiss button. */
  dismissible?: boolean;
  /**
   * Overrides the text pushed to the live region. Only needed when `message`
   * is a node whose flattened text does not read well on its own.
   */
  announcement?: string;
  onDismiss?: (reason: ToastDismissReason) => void;
}

export interface ToastRecord extends ToastOptions {
  id: string;
  variant: ToastVariant;
  /** `false` while the exit animation runs; the store drops it afterwards. */
  open: boolean;
  createdAt: number;
}

export interface ToastStore {
  subscribe: (listener: () => void) => () => void;
  getSnapshot: () => readonly ToastRecord[];
  add: (options: ToastOptions) => string;
  update: (id: string, patch: Partial<ToastOptions>) => void;
  /** Begins the exit animation. Omit `id` to dismiss everything. */
  dismiss: (id?: string, reason?: ToastDismissReason) => void;
  /** Drops the record. Called by the item once its exit animation finished. */
  remove: (id: string) => void;
  /** Removes everything immediately, no animation. */
  clear: () => void;
}

export type ToastVariantFn = (
  message: ReactNode,
  options?: Omit<ToastOptions, 'message' | 'variant'>,
) => string;

export interface ToastApi {
  (input: string | ToastOptions): string;
  success: ToastVariantFn;
  error: ToastVariantFn;
  warning: ToastVariantFn;
  info: ToastVariantFn;
  neutral: ToastVariantFn;
  /** Short-lived confirmation for a change that applied immediately. */
  live: ToastVariantFn;
  dismiss: (id?: string) => void;
  update: (id: string, patch: Partial<ToastOptions>) => void;
  clear: () => void;
}

/* =========================================================================
 * Store
 * ====================================================================== */

const EMPTY: readonly ToastRecord[] = Object.freeze([]);

let idCounter = 0;
function nextId(): string {
  idCounter += 1;
  return `stratum-toast-${idCounter}`;
}

export function createToastStore(): ToastStore {
  let records: readonly ToastRecord[] = EMPTY;
  const listeners = new Set<() => void>();

  const emit = () => {
    for (const listener of listeners) listener();
  };

  const store: ToastStore = {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot() {
      return records;
    },
    add(options) {
      const id = options.id ?? nextId();
      const existing = records.find((r) => r.id === id);
      if (existing) {
        // Re-adding a known id updates in place rather than stacking a
        // duplicate. This is what makes `toast({ id: 'sync' })` on every poll
        // safe to call.
        store.update(id, options);
        return id;
      }
      const record: ToastRecord = {
        variant: 'neutral',
        dismissible: true,
        ...options,
        id,
        open: true,
        createdAt: Date.now(),
      };
      records = [...records, record];
      emit();
      return id;
    },
    update(id, patch) {
      let changed = false;
      records = records.map((r) => {
        if (r.id !== id) return r;
        changed = true;
        return { ...r, ...patch, id: r.id, open: r.open, createdAt: r.createdAt };
      });
      if (changed) emit();
    },
    dismiss(id, reason = 'api') {
      const fired: Array<(reason: ToastDismissReason) => void> = [];
      let changed = false;
      records = records.map((r) => {
        if (id !== undefined && r.id !== id) return r;
        if (!r.open) return r;
        changed = true;
        if (r.onDismiss) fired.push(r.onDismiss);
        return { ...r, open: false };
      });
      if (!changed) return;
      emit();
      // Callbacks run after the store has settled and published. Firing them
      // mid-`map` would let a handler that adds another toast have its write
      // overwritten by the assignment still in flight.
      for (const callback of fired) callback(reason);
    },
    remove(id) {
      const next = records.filter((r) => r.id !== id);
      if (next.length === records.length) return;
      records = next.length === 0 ? EMPTY : next;
      emit();
    },
    clear() {
      if (records.length === 0) return;
      records = EMPTY;
      emit();
    },
  };

  return store;
}

/** The store the imperative {@link toast} escape hatch writes to. */
export const defaultToastStore: ToastStore = createToastStore();

function createToastApi(store: ToastStore): ToastApi {
  const push = (input: string | ToastOptions): string =>
    store.add(typeof input === 'string' ? { message: input } : input);

  const forVariant =
    (variant: ToastVariant): ToastVariantFn =>
    (message, options) =>
      store.add({ ...options, message, variant });

  const api = push as ToastApi;
  api.success = forVariant('success');
  api.error = forVariant('error');
  api.warning = forVariant('warning');
  api.info = forVariant('info');
  api.neutral = forVariant('neutral');
  api.live = forVariant('live');
  api.dismiss = (id) => store.dismiss(id, 'api');
  api.update = (id, patch) => store.update(id, patch);
  api.clear = () => store.clear();
  return api;
}

/** Counts mounted providers so the imperative helper can warn when nothing
 *  is listening — otherwise a missing `<ToastProvider>` fails silently. */
let mountedProviders = 0;
let warnedNoProvider = false;

const globalApi = createToastApi(defaultToastStore);

/**
 * Imperative escape hatch for non-React call sites — a websocket handler, a
 * fetch interceptor, a router guard. Writes to {@link defaultToastStore},
 * which a `<ToastProvider>` with no explicit `store` renders.
 */
export const toast: ToastApi = ((input: string | ToastOptions) => {
  if (import.meta.env?.DEV && mountedProviders === 0 && !warnedNoProvider) {
    warnedNoProvider = true;
    console.error(
      '[stratum] toast() was called with no <ToastProvider> mounted, so nothing ' +
        'will be shown. Render <ToastProvider> once near the root of the app.',
    );
  }
  return globalApi(input);
}) as ToastApi;
toast.success = globalApi.success;
toast.error = globalApi.error;
toast.warning = globalApi.warning;
toast.info = globalApi.info;
toast.neutral = globalApi.neutral;
toast.live = globalApi.live;
toast.dismiss = globalApi.dismiss;
toast.update = globalApi.update;
toast.clear = globalApi.clear;

/* =========================================================================
 * Context
 * ====================================================================== */

interface ToastContextValue {
  store: ToastStore;
  api: ToastApi;
}

const ToastContext = createContext<ToastContextValue | null>(null);

/**
 * Returns the toast API bound to the nearest provider's store, falling back
 * to the global store so a component in isolation (a test, a story) still
 * compiles and runs.
 */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (import.meta.env?.DEV && !ctx && mountedProviders === 0 && !warnedNoProvider) {
    warnedNoProvider = true;
    console.error('[stratum] useToast() was called with no <ToastProvider> mounted.');
  }
  return ctx ? ctx.api : globalApi;
}

/* =========================================================================
 * Helpers
 * ====================================================================== */

const TONE_BY_VARIANT: Record<ToastVariant, StatusTone> = {
  success: 'success',
  error: 'danger',
  warning: 'warning',
  info: 'info',
  neutral: 'neutral',
  live: 'live',
};

/**
 * Flattens a node to plain text for the live region.
 *
 * A screen reader cannot be handed a React element, and `message` is
 * deliberately a `ReactNode`, so the announcement is derived by walking
 * strings, numbers and children. Depth-limited because a deeply nested node
 * is not announcement material anyway.
 */
function toPlainText(node: ReactNode, depth = 0): string {
  if (node == null || node === false || node === true) return '';
  if (typeof node === 'string') return node;
  if (typeof node === 'number') return String(node);
  if (depth > 6) return '';
  if (Array.isArray(node)) {
    return node.map((child) => toPlainText(child as ReactNode, depth + 1)).join('');
  }
  if (isValidElement(node)) {
    const props = node.props as { children?: ReactNode } | null;
    return toPlainText(props?.children, depth + 1);
  }
  return '';
}

function swipeDirectionFor(placement: ToastPlacement): ToastSwipeDirection {
  if (placement.endsWith('-left')) return 'left';
  if (placement.endsWith('-right')) return 'right';
  return placement.startsWith('top') ? 'up' : 'down';
}

/** Subscribes to page visibility so timers pause in a background tab. A toast
 *  that expired while the tab was hidden was never actually seen. */
function subscribeVisibility(onChange: () => void): () => void {
  if (typeof document === 'undefined') return () => {};
  document.addEventListener('visibilitychange', onChange);
  return () => document.removeEventListener('visibilitychange', onChange);
}
function documentHidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden';
}
function useDocumentHidden(): boolean {
  return useSyncExternalStore(subscribeVisibility, documentHidden, () => false);
}

/**
 * A pausable countdown.
 *
 * Remaining time is bookkept in a ref, so pausing on hover and resuming does
 * not restart the toast's life — the classic bug in hand-rolled toasts, where
 * a user who hovers to read gets the full duration again on every mouse-out.
 */
function usePausableTimeout(durationMs: number | null, paused: boolean, onExpire: () => void) {
  const expire = useEventCallback(onExpire);
  const remainingRef = useRef<number>(durationMs ?? Number.POSITIVE_INFINITY);

  // Declared before the timer effect so it has already reset the budget by the
  // time the timer starts. Doing this during render would be a render-phase
  // side effect, which is unsafe under concurrent double-invoke.
  useEffect(() => {
    remainingRef.current = durationMs ?? Number.POSITIVE_INFINITY;
  }, [durationMs]);

  useEffect(() => {
    if (durationMs == null || !Number.isFinite(durationMs)) return;
    if (paused) return;

    const startedAt = Date.now();
    const timer = setTimeout(() => expire(), Math.max(0, remainingRef.current));

    return () => {
      clearTimeout(timer);
      remainingRef.current = Math.max(0, remainingRef.current - (Date.now() - startedAt));
    };
  }, [durationMs, paused, expire]);
}

/* =========================================================================
 * Toast — presentational
 * ====================================================================== */

export interface ToastProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'children'> {
  variant?: ToastVariant;
  title?: ReactNode;
  /** The message body. Any node. */
  children?: ReactNode;
  /** `null` suppresses the default variant glyph. */
  icon?: ReactNode | null;
  action?: ToastAction;
  dismissible?: boolean;
  onDismiss?: (reason: ToastDismissReason) => void;
  /** Accessible name of the dismiss button. */
  labelDismiss?: string;
  /** Accessible name of the toast group. */
  label?: string;
  /**
   * Total lifetime in ms. Drives only the countdown bar; the real timer lives
   * in the provider. `null` hides the bar.
   */
  progressMs?: number | null;
  /** Freezes the countdown bar. Kept in step with the JS timer by the item. */
  paused?: boolean;
  /** Presence phase, published as `data-state` for the CSS transitions. */
  state?: PresenceState;
  swipeDirection?: ToastSwipeDirection;
  /** Pixels of travel before a swipe dismisses. Default 48. */
  swipeThreshold?: number;
}

/**
 * A single notification.
 *
 * Usable standalone (the lab renders it that way) but normally produced by
 * `<ToastProvider>` from the store.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - The toast itself is NOT a live region. `<ToastProvider>` owns a pair of
 *   permanently-mounted `aria-live` regions and mirrors the text into them.
 *   A live region created in the same commit as its content is not reliably
 *   announced by NVDA or VoiceOver — the region has to already exist.
 * - `role="group"` with a label, rather than `role="alert"`, so a keyboard
 *   user landing here via the provider hotkey hears a container rather than
 *   an interruption they cannot navigate.
 * - Escape dismisses when focus is inside, and propagation is stopped so a
 *   toast over an open dialog does not also close the dialog.
 */
export const Toast = forwardRef<HTMLDivElement, ToastProps>(function Toast(
  {
    variant = 'neutral',
    title,
    children,
    icon,
    action,
    dismissible = true,
    onDismiss,
    labelDismiss = 'Dismiss',
    label = 'Notification',
    progressMs = null,
    paused = false,
    state = 'entered',
    swipeDirection = 'none',
    swipeThreshold = 48,
    className,
    style,
    onKeyDown,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
    ...rest
  },
  ref,
) {
  const [swipeOffset, setSwipeOffset] = useState(0);
  const [swiping, setSwiping] = useState(false);
  const [flung, setFlung] = useState(false);
  const startRef = useRef<{ x: number; y: number; id: number } | null>(null);
  const capturedRef = useRef(false);

  const axis: 'x' | 'y' =
    swipeDirection === 'left' || swipeDirection === 'right' ? 'x' : 'y';
  const sign = swipeDirection === 'left' || swipeDirection === 'up' ? -1 : 1;
  const canSwipe = swipeDirection !== 'none' && dismissible;

  const resetSwipe = useCallback(() => {
    startRef.current = null;
    capturedRef.current = false;
    setSwiping(false);
    setSwipeOffset(0);
  }, []);

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    onPointerDown?.(event);
    if (!canSwipe || event.defaultPrevented) return;
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    // Never hijack a press that started on something interactive; a swipe
    // that eats button clicks is worse than no swipe at all.
    if ((event.target as Element | null)?.closest?.('button, a, input, select, textarea')) return;
    startRef.current = { x: event.clientX, y: event.clientY, id: event.pointerId };
  };

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    onPointerMove?.(event);
    const start = startRef.current;
    if (!start || start.id !== event.pointerId) return;

    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    const along = axis === 'x' ? dx : dy;
    const across = axis === 'x' ? dy : dx;

    if (!capturedRef.current) {
      // 6px of slop, and the gesture must be predominantly along the swipe
      // axis — otherwise a page scroll started on a toast would drag it.
      if (Math.abs(along) < 6) return;
      if (Math.abs(along) <= Math.abs(across)) {
        startRef.current = null;
        return;
      }
      event.currentTarget.setPointerCapture(event.pointerId);
      capturedRef.current = true;
      setSwiping(true);
    }

    // Only travel toward the dismissing edge; dragging back is clamped so the
    // toast cannot be pushed across the viewport.
    setSwipeOffset(Math.max(0, along * sign));
  };

  const endSwipe = (event: ReactPointerEvent<HTMLDivElement>) => {
    const start = startRef.current;
    if (!start || start.id !== event.pointerId) return;
    if (capturedRef.current && event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    const passed = capturedRef.current && swipeOffset >= swipeThreshold;
    resetSwipe();
    if (passed) {
      setFlung(true);
      onDismiss?.('swipe');
    }
  };

  const handlePointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    onPointerUp?.(event);
    endSwipe(event);
  };

  const handlePointerCancel = (event: ReactPointerEvent<HTMLDivElement>) => {
    onPointerCancel?.(event);
    resetSwipe();
  };

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;
    if (event.key === 'Escape' && dismissible) {
      event.preventDefault();
      event.stopPropagation();
      onDismiss?.('user');
    }
  };

  const glyph = icon === null ? null : (icon ?? statusGlyph(TONE_BY_VARIANT[variant]));
  const offsetPx = swiping ? swipeOffset * sign : 0;

  return (
    <div
      // Before the spread: an attribute written after `...rest` wins in JSX,
      // so a consumer's own `role` or `aria-label` would be silently replaced
      // by the framework default.
      role="group"
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="toast"
      data-variant={variant}
      data-state={state}
      data-paused={paused || undefined}
      data-swiping={swiping || undefined}
      data-flung={flung || undefined}
      data-axis={canSwipe ? axis : undefined}
      className={clsx('stratum-toast', className)}
      style={{
        ...style,
        ['--_swipe-x' as string]: axis === 'x' ? `${offsetPx}px` : '0px',
        ['--_swipe-y' as string]: axis === 'y' ? `${offsetPx}px` : '0px',
        ['--_fling' as string]: `${sign * 120}%`,
        ...(progressMs != null && Number.isFinite(progressMs)
          ? { ['--_life' as string]: `${progressMs}ms` }
          : null),
      }}
      onKeyDown={handleKeyDown}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={handlePointerUp}
      onPointerCancel={handlePointerCancel}
    >
      {glyph != null && (
        <span className="stratum-toast__icon" aria-hidden="true">
          {glyph}
        </span>
      )}

      <div className="stratum-toast__body">
        {title != null && title !== false && (
          <div className="stratum-toast__title">{title}</div>
        )}
        {children != null && children !== false && (
          <div className="stratum-toast__message">{children}</div>
        )}
      </div>

      {(action || dismissible) && (
        <div className="stratum-toast__controls">
          {action && (
            <button
              type="button"
              className="stratum-toast__action stratum-focus-inset"
              onClick={(event) => {
                action.onClick(event);
                if (!action.keepOpen) onDismiss?.('action');
              }}
            >
              {action.label}
            </button>
          )}
          {dismissible && (
            <button
              type="button"
              className="stratum-toast__dismiss stratum-focus-inset"
              aria-label={labelDismiss}
              onClick={() => onDismiss?.('user')}
            >
              <svg
                viewBox="0 0 16 16"
                width="1em"
                height="1em"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.6"
                strokeLinecap="round"
                focusable="false"
                aria-hidden="true"
              >
                <path d="m4.5 4.5 7 7M11.5 4.5l-7 7" />
              </svg>
            </button>
          )}
        </div>
      )}

      {progressMs != null && Number.isFinite(progressMs) && (
        <span className="stratum-toast__countdown" aria-hidden="true" />
      )}
    </div>
  );
});

/* =========================================================================
 * Provider
 * ====================================================================== */

export interface ToastProviderProps {
  children?: ReactNode;
  placement?: ToastPlacement;
  /** How many toasts are on screen at once. The rest queue. Default 4. */
  limit?: number;
  /** Default lifetime for every variant except `'live'`. Default 5000. */
  duration?: number;
  /** Lifetime for the `'live'` variant. Default 1000. */
  liveDuration?: number;
  /** Per-variant override. `null` pins that variant open. */
  durationByVariant?: Partial<Record<ToastVariant, number | null>>;
  /**
   * A toast carrying an action does not auto-dismiss unless it names its own
   * `duration`. WCAG 2.2.1: a control that vanishes on a timer is not
   * operable. Default `true`.
   */
  stickyWithAction?: boolean;
  /** Show the shrinking lifetime bar. Default `true`. */
  showProgress?: boolean;
  swipeDirection?: ToastSwipeDirection;
  swipeThreshold?: number;
  /**
   * Keys that move focus into the toast region. Default `['F8']`. Pass `[]`
   * to disable.
   */
  hotkey?: string[];
  /** Accessible name of the toast region. Default `'Notifications'`. */
  label?: string;
  /** Appended to the region name as a hint. Default `'F8'`. */
  labelHotkey?: string;
  labelToast?: string;
  labelDismiss?: string;
  /** Text of the overflow chip. Default `'+N more'`. */
  labelOverflow?: (count: number) => string;
  /** Bring your own store, e.g. one per window in a multi-window app. */
  store?: ToastStore;
  /** Render into a portal on `document.body`. Default `true`. */
  portal?: boolean;
}

interface Announcement {
  key: string;
  text: string;
  assertive: boolean;
}

/** Live-region entries are pruned after this long so the region does not grow
 *  without bound. Removal is silent under `aria-relevant="additions"`. */
const ANNOUNCE_TTL = 8000;

/**
 * Owns the toast queue, the viewport and the live regions.
 *
 * WHY THE LIVE REGION IS SEPARATE FROM THE VISIBLE TOAST
 * -----------------------------------------------------
 * Both `aria-live` regions are rendered empty on mount and never unmounted.
 * Screen readers register a live region when it enters the accessibility
 * tree; a region that appears in the same commit as its first content is
 * frequently missed entirely. Mirroring the text into an always-present
 * region also means the visible toast can be a plain `role="group"` — so it
 * is navigable rather than an interruption, and an error can be announced
 * assertively while an info toast stays polite, without splitting the visual
 * stack into two containers.
 */
export function ToastProvider({
  children,
  placement = 'bottom-right',
  limit = 4,
  duration = 5000,
  liveDuration = 1000,
  durationByVariant,
  stickyWithAction = true,
  showProgress = true,
  swipeDirection,
  swipeThreshold = 48,
  hotkey = ['F8'],
  label = 'Notifications',
  labelHotkey = 'F8',
  labelToast = 'Notification',
  labelDismiss = 'Dismiss',
  labelOverflow = (count) => `+${count} more`,
  store: storeProp,
  portal = true,
}: ToastProviderProps) {
  const fallbackStore = defaultToastStore;
  const store = storeProp ?? fallbackStore;

  useEffect(() => {
    mountedProviders += 1;
    return () => {
      mountedProviders -= 1;
    };
  }, []);

  const api = useMemo(() => createToastApi(store), [store]);
  const value = useMemo<ToastContextValue>(() => ({ store, api }), [store, api]);

  const viewport = (
    <ToastViewport
      store={store}
      placement={placement}
      limit={limit}
      duration={duration}
      liveDuration={liveDuration}
      durationByVariant={durationByVariant}
      stickyWithAction={stickyWithAction}
      showProgress={showProgress}
      swipeDirection={swipeDirection ?? swipeDirectionFor(placement)}
      swipeThreshold={swipeThreshold}
      hotkey={hotkey}
      label={label}
      labelHotkey={labelHotkey}
      labelToast={labelToast}
      labelDismiss={labelDismiss}
      labelOverflow={labelOverflow}
    />
  );

  return (
    <ToastContext.Provider value={value}>
      {children}
      {/* Rendered inline rather than through the portal on purpose: a portal
        * only attaches after its own effect runs, and the whole point of these
        * regions is that they exist BEFORE the first toast is inserted. */}
      <ToastAnnouncer store={store} />
      {portal ? <FloatingPortal>{viewport}</FloatingPortal> : viewport}
    </ToastContext.Provider>
  );
}

/**
 * The pair of permanently-mounted live regions.
 *
 * A live region is registered by assistive tech when it enters the
 * accessibility tree. One created in the same commit as its content is
 * frequently not announced at all — the region has to already be there and be
 * empty. Keeping it separate from the visible toast also lets the visible
 * element stay a navigable `role="group"` instead of an interruption, and lets
 * an error announce assertively while an info toast stays polite without
 * splitting the visual stack into two containers.
 */
function ToastAnnouncer({ store }: { store: ToastStore }) {
  const records = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);

  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const announcedRef = useRef<Set<string>>(new Set());
  const timersRef = useRef<Set<ReturnType<typeof setTimeout>>>(new Set());
  const seqRef = useRef(0);

  useEffect(() => {
    const fresh: Announcement[] = [];
    for (const record of records) {
      if (announcedRef.current.has(record.id)) continue;
      announcedRef.current.add(record.id);
      const text =
        record.announcement ??
        [toPlainText(record.title), toPlainText(record.message)]
          .filter((part) => part.trim().length > 0)
          .join('. ');
      if (text.trim().length === 0) continue;
      seqRef.current += 1;
      fresh.push({
        // Sequence-keyed, not id-keyed: a caller-supplied id reused after the
        // stack emptied would otherwise collide with a live announcement.
        key: `${record.id}#${seqRef.current}`,
        text,
        assertive: record.variant === 'error',
      });
    }
    if (fresh.length === 0) return;

    setAnnouncements((prev) => [...prev, ...fresh]);
    const keys = new Set(fresh.map((a) => a.key));
    const timer = setTimeout(() => {
      timersRef.current.delete(timer);
      setAnnouncements((prev) => prev.filter((a) => !keys.has(a.key)));
    }, ANNOUNCE_TTL);
    timersRef.current.add(timer);
  }, [records]);

  // Keeps the dedup set from growing across a long-lived session.
  useEffect(() => {
    if (records.length > 0) return;
    announcedRef.current.clear();
  }, [records]);

  useEffect(() => {
    const timers = timersRef.current;
    return () => {
      for (const timer of timers) clearTimeout(timer);
      timers.clear();
    };
  }, []);

  return (
    <>
      <div
        className="stratum-visually-hidden"
        data-stratum="toast-announcer"
        data-priority="polite"
        aria-live="polite"
        aria-atomic="false"
        aria-relevant="additions"
      >
        {announcements
          .filter((a) => !a.assertive)
          .map((a) => (
            <div key={a.key}>{a.text}</div>
          ))}
      </div>
      <div
        className="stratum-visually-hidden"
        data-stratum="toast-announcer"
        data-priority="assertive"
        aria-live="assertive"
        aria-atomic="false"
        aria-relevant="additions"
      >
        {announcements
          .filter((a) => a.assertive)
          .map((a) => (
            <div key={a.key}>{a.text}</div>
          ))}
      </div>
    </>
  );
}

interface ToastViewportProps {
  store: ToastStore;
  placement: ToastPlacement;
  limit: number;
  duration: number;
  liveDuration: number;
  durationByVariant: Partial<Record<ToastVariant, number | null>> | undefined;
  stickyWithAction: boolean;
  showProgress: boolean;
  swipeDirection: ToastSwipeDirection;
  swipeThreshold: number;
  hotkey: string[];
  label: string;
  labelHotkey: string;
  labelToast: string;
  labelDismiss: string;
  labelOverflow: (count: number) => string;
}

function ToastViewport({
  store,
  placement,
  limit,
  duration,
  liveDuration,
  durationByVariant,
  stickyWithAction,
  showProgress,
  swipeDirection,
  swipeThreshold,
  hotkey,
  label,
  labelHotkey,
  labelToast,
  labelDismiss,
  labelOverflow,
}: ToastViewportProps) {
  const records = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);

  const [hovered, setHovered] = useState(false);
  const [focusWithin, setFocusWithin] = useState(false);
  const hidden = useDocumentHidden();
  const paused = hovered || focusWithin || hidden;

  const regionRef = useRef<HTMLOListElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  // Mirrors `focusWithin` for the reconciliation effect below. It is written
  // only from effects and event handlers — never during render, so a render
  // that React discards can never leave a stale value behind.
  const focusWithinRef = useRef(false);

  /* -- Hotkey ------------------------------------------------------------ */
  const openCount = records.reduce((n, r) => (r.open ? n + 1 : n), 0);

  useEffect(() => {
    if (hotkey.length === 0 || openCount === 0) return;
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.altKey || event.ctrlKey || event.metaKey) return;
      if (!hotkey.includes(event.key)) return;
      const region = regionRef.current;
      if (!region) return;
      event.preventDefault();
      const active = document.activeElement;
      if (active instanceof HTMLElement && !region.contains(active)) {
        returnFocusRef.current = active;
      }
      region.focus();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [hotkey, openCount]);

  /* -- Focus bookkeeping --------------------------------------------------
   * Removing the element that currently has focus does NOT fire `blur` in
   * most engines, so `focusWithin` cannot be maintained from events alone —
   * dismissing a focused toast would otherwise leave the viewport pinned in a
   * paused state forever, and drop the keyboard user to the top of the
   * document. This reconciles against the real `activeElement` after every
   * store change and hands focus somewhere sensible.
   */
  useEffect(() => {
    const region = regionRef.current;
    if (!region) return;

    const active = document.activeElement;
    if (active instanceof Node && region.contains(active)) {
      focusWithinRef.current = true;
      setFocusWithin(true);
      return;
    }

    const lostFromInside =
      focusWithinRef.current && (active === null || active === document.body);

    if (lostFromInside) {
      if (openCount > 0) {
        // Something is still on screen: park on the region so Tab continues
        // from here rather than from the top of the page.
        region.focus({ preventScroll: true });
        return;
      }
      const back = returnFocusRef.current;
      returnFocusRef.current = null;
      if (back && back.isConnected) back.focus({ preventScroll: true });
    }

    focusWithinRef.current = false;
    setFocusWithin(false);
  }, [records, openCount]);

  /* -- Windowing --------------------------------------------------------- */
  const openRecords = records.filter((r) => r.open);
  const visibleIds = new Set(openRecords.slice(-limit).map((r) => r.id));
  // Exiting records keep their slot until the animation finishes, so the stack
  // does not jump a queued toast into place mid-transition.
  const rendered = records.filter((r) => visibleIds.has(r.id) || !r.open);
  const overflow = Math.max(0, openRecords.length - limit);

  const isTop = placement.startsWith('top');
  // Newest is always nearest the viewport edge. For top placements that means
  // reversing the list in the DOM rather than in CSS, so visual order and tab
  // order stay identical.
  const ordered = isTop ? [...rendered].reverse() : rendered;

  const overflowChip =
    overflow > 0 ? (
      <li className="stratum-toast-viewport__overflow" key="__overflow">
        <span className="stratum-toast-overflow">{labelOverflow(overflow)}</span>
      </li>
    ) : null;

  const resolveDuration = (record: ToastRecord): number | null => {
    if (record.duration !== undefined) return record.duration;
    if (record.action && stickyWithAction) return null;
    const perVariant = durationByVariant?.[record.variant];
    if (perVariant !== undefined) return perVariant;
    return record.variant === 'live' ? liveDuration : duration;
  };

  return (
    <ol
      ref={regionRef}
      tabIndex={ordered.length > 0 ? -1 : undefined}
      // `list-style: none` strips list semantics in Safari/VoiceOver, so the
      // role is restored explicitly while items exist. An empty ARIA list is
      // invalid because it cannot own a listitem, so the persistent viewport
      // becomes presentational until the first toast arrives.
      role={ordered.length > 0 ? 'list' : 'presentation'}
      data-stratum="toast-viewport"
      data-placement={placement}
      data-empty={ordered.length === 0 || undefined}
      className="stratum-toast-viewport"
      aria-label={
        ordered.length > 0 ? (hotkey.length > 0 ? `${label} (${labelHotkey})` : label) : undefined
      }
      // `pointerover`/`pointerout` rather than the enter/leave pair: the
      // viewport itself is `pointer-events: none` so that clicks fall through
      // to the page, and only natively-bubbling events are guaranteed to
      // reach it from the toasts that do accept pointers.
      onPointerOver={(event) => {
        if (event.pointerType !== 'touch') setHovered(true);
      }}
      onPointerOut={(event) => {
        if (event.pointerType === 'touch') return;
        const next = event.relatedTarget as Node | null;
        if (!next || !event.currentTarget.contains(next)) setHovered(false);
      }}
      onFocus={() => {
        focusWithinRef.current = true;
        setFocusWithin(true);
      }}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          focusWithinRef.current = false;
          setFocusWithin(false);
        }
      }}
    >
      {isTop ? null : overflowChip}
      {ordered.map((record) => (
        <ToastItem
          key={record.id}
          record={record}
          store={store}
          durationMs={resolveDuration(record)}
          paused={paused}
          showProgress={showProgress}
          swipeDirection={swipeDirection}
          swipeThreshold={swipeThreshold}
          labelToast={labelToast}
          labelDismiss={labelDismiss}
        />
      ))}
      {isTop ? overflowChip : null}
    </ol>
  );
}

interface ToastItemProps {
  record: ToastRecord;
  store: ToastStore;
  durationMs: number | null;
  paused: boolean;
  showProgress: boolean;
  swipeDirection: ToastSwipeDirection;
  swipeThreshold: number;
  labelToast: string;
  labelDismiss: string;
}

function ToastItem({
  record,
  store,
  durationMs,
  paused,
  showProgress,
  swipeDirection,
  swipeThreshold,
  labelToast,
  labelDismiss,
}: ToastItemProps) {
  const { isPresent, ref, state } = usePresence(record.open);

  const dismiss = useEventCallback((reason: ToastDismissReason) => {
    store.dismiss(record.id, reason);
  });

  usePausableTimeout(durationMs, paused || !record.open, () => dismiss('timeout'));

  const remove = useEventCallback(() => store.remove(record.id));
  useEffect(() => {
    if (isPresent) return;
    remove();
  }, [isPresent, remove]);

  if (!isPresent) return null;

  return (
    <li className="stratum-toast-viewport__item">
      <Toast
        ref={ref}
        state={state}
        variant={record.variant}
        title={record.title}
        icon={record.icon}
        action={record.action}
        dismissible={record.dismissible ?? true}
        onDismiss={dismiss}
        label={labelToast}
        labelDismiss={labelDismiss}
        paused={paused}
        progressMs={showProgress ? durationMs : null}
        swipeDirection={swipeDirection}
        swipeThreshold={swipeThreshold}
      >
        {record.message}
      </Toast>
    </li>
  );
}

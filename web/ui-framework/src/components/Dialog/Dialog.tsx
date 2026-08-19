import {
  forwardRef,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type HTMLAttributes,
  type HTMLProps,
  type ReactNode,
} from 'react';
import {
  FloatingFocusManager,
  FloatingNode,
  FloatingOverlay,
  FloatingPortal,
  FloatingTree,
  useDismiss,
  useFloating,
  useFloatingNodeId,
  useFloatingParentNodeId,
  useInteractions,
  useMergeRefs,
  useRole,
} from '@floating-ui/react';
import clsx from 'clsx';
import { Button } from '../Button/Button';
import { useEventCallback } from '../../hooks/useEventCallback';
import { useMeasure } from '../../hooks/useMeasure';
import { usePresence } from '../../hooks/usePresence';
import { useScrollLock } from '../../hooks/useScrollLock';
import {
  resolveReturnFocusTarget,
  scheduleReturnFocus,
  useIsomorphicLayoutEffect,
} from '../Popover/overlayFocus';
import './Dialog.css';

export type DialogSize = 'sm' | 'md' | 'lg' | 'xl' | 'full';

/** The full-viewport wrappers a modal layer portals into, in stacking order. */
const LAYER_SELECTOR = '.stratum-dialog__overlay, .stratum-drawer__overlay';

/**
 * Dispatched on a single layer's overlay to ask that layer to close.
 *
 * Deliberately a DOM event rather than a FloatingTree message. The tree is
 * built from React context, so it only exists when the layers are rendered
 * inside one another — and the whole point of this escape hatch is to work for
 * a confirmation that has no idea what opened it. Addressing overlays directly
 * reaches every mounted layer regardless of how the consumer arranged them, and
 * unlike a `document`-level broadcast it can be aimed at one layer at a time,
 * which is what lets the stack unwind in order rather than all at once.
 */
const CLOSE_STACK_EVENT = 'stratum:close-layer-stack';

/** Only Dialog participates in the cascade; a Drawer would never answer. */
const DIALOG_LAYER_SELECTOR = '.stratum-dialog__overlay';

/**
 * Backstop for one step of the cascade. Only fires when a consumer ignores
 * `onOpenChange` so the layer never transitions at all, which would otherwise
 * wedge the sequence; a normal step resolves in a fraction of this.
 */
const CASCADE_TIMEOUT_MS = 1000;

/** The panel's 1px top and bottom border, which `borderBoxSize` of the children excludes. */
const PANEL_BORDER_PX = 2;

/** Guards against overlapping cascades. */
let cascading = false;

const nextFrame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

/**
 * Resolves when a layer has visually GONE, which is well before it unmounts.
 *
 * The exit runs two legs of different lengths on purpose: opacity over 250ms,
 * transform over 470ms, the framework's opacity-finishes-first rule. Unmount is
 * gated by the longer one, so waiting for removal spent ~220ms of every step
 * waiting on a panel that had already been fully transparent for a fifth of a
 * second. Across two layers that read as a stall between them rather than as a
 * stack unwinding.
 *
 * So each step waits for the OPACITY leg only. That is not an arbitrary shorter
 * delay — it is the moment the layer stops being visible, which is exactly the
 * cue the next one should follow. The transform keeps running underneath,
 * invisible, and `usePresence` unmounts on its own schedule.
 *
 * The animations are enumerated two frames after the dispatch because the exit
 * state has to commit and its transitions be created first — the same reason
 * `usePresence` waits a frame before calling `getAnimations()`.
 */
async function waitUntilVisuallyGone(element: Element, timeoutMs: number): Promise<void> {
  await nextFrame();
  await nextFrame();
  if (!element.isConnected) return;

  const fading = element
    .getAnimations({ subtree: true })
    .filter(
      (animation) =>
        (animation as CSSTransition).transitionProperty === 'opacity' &&
        (animation.playState === 'running' || animation.playState === 'paused'),
    );
  // Nothing fading — reduced motion may have collapsed it, or the consumer
  // never closed it. Either way there is nothing to wait for.
  if (fading.length === 0) return;

  await Promise.race([
    // allSettled, not all: a cancelled transition rejects, and a cancelled exit
    // still means the layer is done being visible.
    Promise.allSettled(fading.map((animation) => animation.finished)),
    new Promise((resolve) => setTimeout(resolve, timeoutMs)),
  ]);
}

async function runCascade(layers: HTMLElement[]): Promise<void> {
  // Topmost first. Document order is open order is z-order, because
  // FloatingPortal appends each overlay to <body> as it opens.
  for (let i = layers.length - 1; i >= 0; i -= 1) {
    const layer = layers[i];
    if (!layer?.isConnected) continue;
    layer.dispatchEvent(new CustomEvent(CLOSE_STACK_EVENT));
    await waitUntilVisuallyGone(layer, CASCADE_TIMEOUT_MS);
  }
}

/**
 * Closes every open Dialog at once.
 *
 * DISMISSAL AND RESOLUTION ARE NOT THE SAME THING, which is why this is a
 * separate call rather than a change to how Escape behaves. Escape and an
 * outside press mean "I did not mean to be here" and belong to the innermost
 * layer only — taking the parent down with them is the bug this component just
 * had. Pressing *Discard* in a confirmation means something else entirely: the
 * decision the whole stack existed to obtain has been made, and returning the
 * operator to a parent dialog that is now moot is busywork.
 *
 * A consumer owning both pieces of state can always just set them both false.
 * This exists for the case where it cannot: a shared ConfirmDialog rendered
 * three components deep has no reference to whatever opened it.
 */
export function closeDialogStack(): void {
  if (typeof document === 'undefined' || cascading) return;
  const layers = [...document.querySelectorAll<HTMLElement>(DIALOG_LAYER_SELECTOR)];
  if (layers.length === 0) return;

  cascading = true;

  /* THE STACK UNWINDS, IT DOES NOT EVAPORATE.
   *
   * Closing every layer on the same frame is legible only if you already knew
   * how many there were; it reads as a glitch rather than as three dialogs
   * closing. So each layer waits for the one above it to finish leaving —
   * `waitForRemoval` keys off the overlay actually unmounting, which happens
   * when `usePresence` sees the real exit animations finish, so the stagger is
   * exactly the exit duration with nothing hardcoded and it follows
   * `prefers-reduced-motion` down automatically.
   *
   * EVERY LAYER IS FROZEN UP FRONT, not as its turn arrives. A cascade of three
   * dialogs takes most of a second, and for that whole time the lower ones are
   * still on screen and would otherwise be fully interactive — long enough to
   * click a button in a dialog that is already condemned, or to open a fourth
   * layer into the middle of a sequence that has already decided what it is
   * closing. `data-stack-closing` takes their pointer events away immediately;
   * the module-level `cascading` flag makes a second call a no-op, so a
   * double-clicked Confirm cannot start two interleaved cascades. */
  for (const layer of layers) layer.setAttribute('data-stack-closing', '');

  void runCascade(layers).finally(() => {
    cascading = false;
  });
}

/** Hook form of {@link closeDialogStack}, for consumers who prefer one. */
export function useDialogStack(): { closeAll: () => void } {
  return useMemo(() => ({ closeAll: closeDialogStack }), []);
}

/** A point in viewport coordinates, as reported by `clientX` / `clientY`. */
export interface DialogOrigin {
  x: number;
  y: number;
}

export interface DialogProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  open: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Accessible name for the dialog. Required — a dialog without one is unusable. */
  title: ReactNode;
  /** Optional supporting line, wired up as `aria-describedby`. */
  description?: ReactNode;
  /** Action row pinned to the bottom of the panel. */
  footer?: ReactNode;
  size?: DialogSize;
  /** Allows Escape, an outside press and the close button to close the dialog. */
  dismissible?: boolean;
  /**
   * What a dismissal gesture closes.
   *
   * `'layer'` (default) closes this dialog only, leaving anything underneath
   * open — the standard, and what Escape should do.
   *
   * `'stack'` closes every open layer. Some flows genuinely want it: a wizard
   * whose steps are dialogs, where backing out of step three should not deposit
   * you in step two. It applies to Escape, outside presses and the close
   * button; an explicit action button should call `closeDialogStack()` instead
   * of changing this, so the two intentions stay distinguishable.
   */
  dismissBehavior?: 'layer' | 'stack';
  /**
   * Grows and shrinks the panel as its content changes, instead of snapping to
   * the new size.
   *
   * Opt-in, and off by default. It costs three `ResizeObserver`s and a wrapper
   * element, and plenty of dialogs change content for reasons that should NOT
   * animate — a form validating on each keystroke, a tab swap between panels of
   * very different length, a virtualised list. Turning it on for those makes
   * them jitter.
   *
   * The panel stops growing at its existing cap and the body starts scrolling;
   * that boundary is enforced entirely in CSS, so it survives a window resize
   * and never needs the viewport measured in JS.
   */
  autoResize?: boolean;
  /** Defaults to `dismissible`. */
  showCloseButton?: boolean;
  closeLabel?: string;
  /** Keeps `title` as the accessible name but removes it from the visual design. */
  hideTitle?: boolean;
  /**
   * The control the dialog should appear to grow out of. Its centre is measured
   * once, at the moment the dialog opens.
   *
   * Worth passing: the fallback is the focused element, and clicking a button
   * does not focus it in Safari or Firefox on macOS.
   */
  originRef?: { current: HTMLElement | null };
  /** An explicit origin point, for opening from a pointer position or a canvas hit. */
  origin?: DialogOrigin;
  /**
   * Element focused on open. Defaults to the panel itself, so a screen reader
   * announces the dialog's name and description before its controls.
   */
  initialFocusRef?: { current: HTMLElement | null };
  /**
   * Element focused when the dialog closes. Defaults to `originRef`, or to
   * whatever had focus at the moment the dialog opened.
   */
  returnFocusRef?: { current: HTMLElement | null };
  children?: ReactNode;
}

/* -------------------------------------------------------------------------- */

interface OriginDelta {
  dx: number;
  dy: number;
}

function rectCentre(element: Element | null | undefined): DialogOrigin | null {
  if (!element || !element.isConnected) return null;
  const rect = element.getBoundingClientRect();
  if (rect.width === 0 && rect.height === 0) return null;
  return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
}

/**
 * The element that most likely opened the dialog, used only as a last resort so
 * the origin-zoom does something sensible with no wiring at all.
 *
 * It is a fallback rather than the mechanism because clicking a `<button>` does
 * not focus it in Safari or in Firefox on macOS — there the active element is
 * still `<body>` and this returns null, which is why `originRef` exists and is
 * what a consumer should actually pass.
 */
function activeTrigger(): Element | null {
  const active = document.activeElement;
  if (!active || active === document.body || active === document.documentElement) return null;
  return active;
}

/**
 * Offset from the centre of the viewport to the origin point. Stored as a delta
 * rather than as a point because that is what the panel's transform needs, and
 * because it must be frozen: the exit has to replay the entrance in reverse,
 * from the same place, even if the trigger has since moved or been unmounted.
 */
function captureOrigin(
  explicit: DialogOrigin | undefined,
  anchor: { current: HTMLElement | null } | undefined,
): OriginDelta | null {
  if (typeof window === 'undefined') return null;
  const point = explicit ?? rectCentre(anchor?.current) ?? rectCentre(activeTrigger());
  if (!point) return null;
  return {
    dx: Math.round(point.x - window.innerWidth / 2),
    dy: Math.round(point.y - window.innerHeight / 2),
  };
}

const CloseIcon = () => (
  <svg
    viewBox="0 0 16 16"
    width="16"
    height="16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.6"
    strokeLinecap="round"
    focusable="false"
    aria-hidden="true"
  >
    <path d="m4.5 4.5 7 7M11.5 4.5l-7 7" />
  </svg>
);

/* -------------------------------------------------------------------------- */

const DialogImpl = forwardRef<HTMLDivElement, DialogProps>(function DialogImpl(
  {
    open,
    onOpenChange,
    title,
    description,
    footer,
    size = 'md',
    dismissible = true,
    dismissBehavior = 'layer',
    autoResize = false,
    showCloseButton,
    closeLabel = 'Close',
    hideTitle = false,
    originRef,
    origin,
    initialFocusRef,
    returnFocusRef,
    children,
    className,
    style,
    ...rest
  },
  forwardedRef,
) {
  const nodeId = useFloatingNodeId();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const generatedId = useId();
  const titleId = `${generatedId}-title`;
  const descriptionId = `${generatedId}-description`;

  const showClose = showCloseButton ?? dismissible;

  /* -- Origin capture -----------------------------------------------------
   * This runs while `open` is already true but before `usePresence` has
   * mounted the panel — `usePresence` promotes `isPresent` from inside an
   * effect, which costs one render and is exactly the window we need. So the
   * measurement happens before the dialog exists (nothing has moved yet, and
   * focus has not been pulled into the panel), and the value is available on
   * the panel's very first render, which is the only render `@starting-style`
   * ever sees.
   */
  const originDeltaRef = useRef<OriginDelta | null>(null);
  const capturedReturnRef = useRef<HTMLElement | null>(null);
  const wasOpenRef = useRef(false);

  useIsomorphicLayoutEffect(() => {
    if (open === wasOpenRef.current) return undefined;
    wasOpenRef.current = open;

    if (open) {
      // Neither value is cleared on close: the exit replays the same origin,
      // and focus goes back to whatever was live when the dialog opened.
      originDeltaRef.current = captureOrigin(origin, originRef);
      capturedReturnRef.current = resolveReturnFocusTarget(returnFocusRef ?? originRef);
      return undefined;
    }

    return scheduleReturnFocus({
      target: capturedReturnRef.current,
      panel: panelRef.current,
    });
    // `origin` is compared by its coordinates rather than by identity, so an
    // inline `{ x, y }` object literal does not re-run this on every render.
  }, [open, origin?.x, origin?.y, originRef, returnFocusRef]);

  const { refs, context } = useFloating({
    nodeId,
    open,
    onOpenChange: (next) => {
      onOpenChange?.(next);
      // `stack` turns a dismissal of this layer into a dismissal of all of
      // them. Emitted after this dialog's own handler so its state settles
      // first; every other open layer closes off the broadcast.
      if (!next && dismissBehavior === 'stack') closeDialogStack();
    },
  });

  /* Listens for the broadcast so any layer can be closed by any other, however
   * the two were rendered. Only while open, so a closed dialog cannot be
   * woken up by a stack close it was never part of. */
  const [overlayNode, setOverlayNode] = useState<HTMLDivElement | null>(null);

  const role = useRole(context, { role: 'dialog' });
  const dismiss = useDismiss(context, {
    enabled: dismissible,
    escapeKey: true,
    /* A press inside a layer stacked ABOVE this one is not a press outside it.
     *
     * `bubbles` already handles layering when the tree knows about it, and the
     * tree is built from React context — so it only forms when the inner dialog
     * is rendered INSIDE the outer one's children. Rendering the two as
     * siblings, each driven by its own piece of state, is at least as natural
     * to write and renders identically:
     *
     *     <Dialog open={outer}>…</Dialog>
     *     <Dialog open={inner}>…</Dialog>     // ← no parent in context
     *
     * That inner dialog starts a FloatingTree of its own, so the two layers are
     * unrelated roots, and the outer one classifies every press inside the inner
     * one as an outside press and dismisses itself. Clicking Cancel on a
     * confirmation destroyed the dialog that raised it — silently, with no error
     * and nothing in the markup to suggest why.
     *
     * The stacking order is recoverable without the tree: FloatingPortal appends
     * each overlay to <body> as it opens, so document order IS z-order. A press
     * landing in an overlay that follows this one belongs to that overlay.
     *
     * Deliberately one-directional. A press on the OUTER panel while an inner
     * dialog is open still dismisses the inner one, because the outer overlay
     * precedes it — which is the behaviour you want, and what a blanket "ignore
     * presses in other overlays" rule would have broken. */
    outsidePress: (event) => {
      const target = event.target as Element | null;
      if (!target?.closest) return true;
      const own = panelRef.current?.closest(LAYER_SELECTOR) ?? null;
      const hit = target.closest(LAYER_SELECTOR);
      if (!own || !hit || hit === own) return true;
      const stackedAbove = own.compareDocumentPosition(hit) & Node.DOCUMENT_POSITION_FOLLOWING;
      return !stackedAbove;
    },
    // Deciding on mousedown means a selection drag that starts on the panel and
    // finishes on the scrim is not mistaken for "clicked outside".
    outsidePressEvent: 'mousedown',
    // Escape and outside presses belong to the innermost open layer, so a
    // popover opened from inside this dialog does not take the dialog with it.
    bubbles: { escapeKey: false, outsidePress: false },
  });

  const { getFloatingProps } = useInteractions([role, dismiss]);

  const presence = usePresence(open);
  const overlayRefs = useMergeRefs<HTMLDivElement>([presence.ref, setOverlayNode]);

  /* The stack-close listener lives on this layer's own overlay rather than on
   * `document`, so `closeDialogStack()` can address one layer at a time instead
   * of felling them all with a single broadcast.
   *
   * The overlay node is held in STATE rather than a ref, and that is the whole
   * trick. `FloatingPortal` creates its portal container in a layout effect and
   * renders nothing into it until that lands, so the overlay mounts a commit
   * later than `presence.isPresent` flips. An effect keyed on `isPresent` — or
   * on `open` — therefore runs while the ref is still null, bails, and never
   * re-runs, because by the time the node exists no dependency has changed.
   * Keying on the node makes the effect fire exactly when there is something to
   * attach to. Measured before the fix: the ref demonstrably reached the DOM and
   * the listener was still never installed. */
  const emitClose = useEventCallback(() => onOpenChange?.(false));
  useEffect(() => {
    if (!open || !overlayNode) return undefined;
    const handle = () => emitClose();
    overlayNode.addEventListener(CLOSE_STACK_EVENT, handle);
    return () => overlayNode.removeEventListener(CLOSE_STACK_EVENT, handle);
  }, [open, emitClose, overlayNode]);

  // GEOMETRY IS LATCHED FOR THE DURATION OF THE EXIT.
  //
  // `open={selected !== null}` paired with `size={selected?.size ?? 'md'}` is
  // the idiomatic way to drive a dialog from nullable state, and it has a sharp
  // edge: closing clears the state, so `size` reverts to its default on the
  // very same commit that starts the exit. `--_w` is not a transitioned
  // property, so an `xl` panel snapped from 56rem to 32rem on the first frame
  // and only then zoomed back to its trigger — read as "it flashes back to the
  // wrong size before it closes".
  //
  // Freezing the size while exiting is not the AnimatePresence snapshot this
  // framework deliberately rejected (see usePresence): React still renders the
  // exiting subtree and content updates still land. Only the box stops changing
  // shape once it has started to leave, which is the rule you would state out
  // loud anyway — a thing on its way out does not resize.
  const lastOpenSize = useRef(size);
  if (presence.state !== 'exiting') lastOpenSize.current = size;
  const renderedSize = presence.state === 'exiting' ? lastOpenSize.current : size;

  /* -- Auto-resize ---------------------------------------------------------
   * The panel's height is driven from a measurement of its own content, and
   * then CLAMPED IN CSS by `min(var(--_h), 100%)`. That division of labour is
   * the whole design: JS knows how tall the content wants to be, CSS knows how
   * tall the panel is allowed to be, and the two never have to agree.
   *
   * The predecessor did the opposite — it computed the cap in JS as
   * `window.innerHeight * 0.85 - 120`, with 120 standing in for a header and
   * footer it never measured and which are both optional. That number is wrong
   * for any dialog without a title, goes stale on every window resize because
   * nothing listens for one, and disagrees with `85vh` on mobile as the URL bar
   * moves. Leaving the cap in CSS makes all three impossible.
   *
   * Three observers, not one: the header and footer are chrome whose height the
   * caller controls (a wrapping title, a two-row action bar), so guessing them
   * is the same mistake in miniature.
   *
   * The content wrapper is measured, never `.stratum-dialog__body` itself. The
   * body is the scroller, and writing a height onto the panel changes the
   * body's used height — observing it would feed straight back into the
   * measurement it just produced. */
  const { ref: contentRef, size: contentSize, hasSettled } = useMeasure<HTMLDivElement>();
  const { ref: headerRef, size: headerSize } = useMeasure<HTMLDivElement>();
  const { ref: footerRef, size: footerSize } = useMeasure<HTMLDivElement>();

  const headerIsBare = hideTitle && description == null && !showClose;

  /* A bare header is `display: contents`, so it has no box for ResizeObserver to
   * report and `headerSize` stays null forever. Requiring it would silently
   * disable auto-resize for exactly those dialogs, so it is required only when
   * the header actually renders one. */
  const headerMeasured = headerIsBare || headerSize != null;
  const naturalHeight =
    autoResize && contentSize && headerMeasured
      ? contentSize.height + (headerSize?.height ?? 0) + (footerSize?.height ?? 0) + PANEL_BORDER_PX
      : null;

  // Frozen while leaving, for the same reason the size is: a thing on its way
  // out does not resize. This one also matters mechanically — `usePresence`
  // waits on `getAnimations({ subtree: true })`, so a height transition
  // starting mid-exit would hold the unmount open.
  const lastOpenHeight = useRef<number | null>(null);
  // Cleared once the dialog is gone. `DialogImpl` outlives the panel — only the
  // portal contents unmount — so without this a reopen would paint one frame at
  // the height its PREVIOUS content happened to need. Unset, `--_h` makes the
  // whole `min()` invalid and `height` falls back to `auto`, which is the right
  // size by construction.
  if (!presence.isPresent) lastOpenHeight.current = null;
  else if (presence.state !== 'exiting' && naturalHeight != null) {
    lastOpenHeight.current = naturalHeight;
  }
  const renderedHeight = lastOpenHeight.current;

  // Floating UI's own `lockScroll` freezes `document.body` only, which does

  // nothing in an app shell where an inner element is the scroller — the page

  // behind the scrim scrolled freely. This blocks scroll intent outside the

  // panel while leaving the panel's own body scrollable.

  useScrollLock(open, panelRef);
  const setPanelRef = useMergeRefs<HTMLDivElement>([
    refs.setFloating,
    panelRef,
    forwardedRef,
  ]);

  const delta = originDeltaRef.current;
  const originStyle = {
    '--_origin-x': `${delta ? delta.dx : 0}px`,
    '--_origin-y': `${delta ? delta.dy : 0}px`,
  } as CSSProperties;


  if (!presence.isPresent) return null;

  return (
    <FloatingNode id={nodeId}>
      <FloatingPortal>
        <FloatingOverlay
          lockScroll
          // The presence ref goes on the outermost node so `getAnimations({
          // subtree: true })` sees the scrim and the panel, and the dialog
          // unmounts when the slower of the two has finished — no durations
          // duplicated in JS.
          ref={overlayRefs}
          data-stratum="dialog-overlay"
          data-state={presence.state}
          data-size={renderedSize}
          className="stratum-dialog__overlay"
        >
          <div className="stratum-dialog__scrim" data-state={presence.state} aria-hidden="true" />

          <FloatingFocusManager
            context={context}
            modal
            // Flipping `disabled` at close is what returns focus to the trigger
            // immediately instead of after the exit animation. Without it the
            // keyboard stays trapped inside a panel that is fading away.
            disabled={!open}
            // Focus the panel, not its first control. Landing on "Close,
            // button" tells a screen-reader user nothing about what they just
            // opened; landing on the panel reads its name and description.
            initialFocus={initialFocusRef ?? panelRef}
            returnFocus={capturedReturnRef}
            restoreFocus
            // A touch screen-reader user has no Escape key. If there is also no
            // visible close button, this is their only way out.
            visuallyHiddenDismiss={dismissible && !showClose ? closeLabel : false}
          >
            <div
              {...getFloatingProps(rest as HTMLProps<HTMLElement>)}
              ref={setPanelRef}
              role="dialog"
              aria-modal="true"
              tabIndex={-1}
              // Composed, not overwritten: these are written after the `rest`
              // spread, and in JSX a later key wins even when its value is
              // `undefined` — so assigning them directly would silently delete
              // a consumer's own `aria-labelledby` / `aria-describedby`. The
              // spread has already been consumed, hence the explicit reads.
              aria-labelledby={
                [titleId, rest['aria-labelledby']].filter(Boolean).join(' ') || undefined
              }
              aria-describedby={
                [description != null ? descriptionId : null, rest['aria-describedby']]
                  .filter(Boolean)
                  .join(' ') || undefined
              }
              data-stratum="dialog"
              data-state={presence.state}
              data-size={renderedSize}
              className={clsx('stratum-dialog', className)}
              data-auto-size={autoResize || undefined}
              /* Height animates only once the dialog has arrived.
               *
               * `hasSettled` alone is not enough: `useMeasure`'s state lives in
               * this component, which outlives the panel, so from the second
               * open onward it is already true on the entrance's first frame —
               * measured, and the gate silently stopped working. Requiring
               * `entered` as well scopes the transition to the period when the
               * dialog is actually sitting there, leaving the entrance to the
               * transform-and-opacity animation that owns it. */
              data-animate={
                (autoResize && hasSettled && presence.state === 'entered') || undefined
              }
              style={
                {
                  ...originStyle,
                  ...(renderedHeight != null ? { '--_h': `${renderedHeight}px` } : null),
                  ...style,
                } as CSSProperties
              }
            >
              <div
                ref={headerRef}
                className="stratum-dialog__header"
                data-bare={headerIsBare || undefined}
              >
                <div className="stratum-dialog__heading">
                  <h2
                    id={titleId}
                    className={clsx(
                      'stratum-dialog__title',
                      hideTitle && 'stratum-visually-hidden',
                    )}
                  >
                    {title}
                  </h2>
                  {description != null && (
                    <p id={descriptionId} className="stratum-dialog__description">
                      {description}
                    </p>
                  )}
                </div>

                {showClose && (
                  <Button
                    variant="ghost"
                    size="sm"
                    iconOnly
                    aria-label={closeLabel}
                    icon={<CloseIcon />}
                    className="stratum-dialog__close"
                    onClick={() => onOpenChange?.(false)}
                  />
                )}
              </div>

              <div className="stratum-dialog__body">
                {/* Wrapper so the measured node is the content, not the scroller. */}
                <div ref={contentRef}>{children}</div>
              </div>

              {footer != null && (
                <div ref={footerRef} className="stratum-dialog__footer">
                  {footer}
                </div>
              )}
            </div>
          </FloatingFocusManager>
        </FloatingOverlay>
      </FloatingPortal>
    </FloatingNode>
  );
});

/**
 * A modal dialog.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - Focus moves into the panel on open, is trapped there while it is open, and
 *   is returned to whatever had it before — at the moment the dialog closes,
 *   not when its animation ends.
 * - The rest of the page is hidden from assistive technology and page scroll is
 *   locked for as long as the dialog is up.
 * - `title` is mandatory and is the accessible name. `hideTitle` removes it
 *   visually without removing the name, which is the correct trade when a
 *   design has no room for a heading.
 * - Escape closes only the innermost layer, so a popover or tooltip opened from
 *   inside the dialog is dismissed first.
 *
 * MOTION
 * ------
 * The panel grows out of the control that opened it. The origin is measured
 * once, at open, and frozen: the exit animation collapses back to the same
 * point even if the trigger has since moved, scrolled away or unmounted.
 * Nothing waits on a timer — `usePresence` unmounts the dialog when the real
 * animations report finished, so the durations exist only in CSS and shorten
 * correctly under `prefers-reduced-motion`, where the zoom and the travel both
 * collapse to nothing and only the fade remains.
 */
export const Dialog = forwardRef<HTMLDivElement, DialogProps>(function Dialog(props, ref) {
  const parentId = useFloatingParentNodeId();

  if (parentId === null) {
    return (
      <FloatingTree>
        <DialogImpl {...props} ref={ref} />
      </FloatingTree>
    );
  }

  return <DialogImpl {...props} ref={ref} />;
});

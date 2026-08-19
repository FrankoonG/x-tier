import {
  cloneElement,
  forwardRef,
  isValidElement,
  useMemo,
  useRef,
  type Attributes,
  type CSSProperties,
  type HTMLAttributes,
  type HTMLProps,
  type ReactElement,
  type ReactNode,
} from 'react';
import {
  FloatingFocusManager,
  FloatingNode,
  FloatingOverlay,
  FloatingPortal,
  FloatingTree,
  arrow as arrowMiddleware,
  autoUpdate,
  flip,
  limitShift,
  offset as offsetMiddleware,
  shift,
  useClick,
  useDismiss,
  useFloating,
  useFloatingNodeId,
  useFloatingParentNodeId,
  useInteractions,
  useMergeRefs,
  useRole,
  type Placement,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { usePresence } from '../../hooks/usePresence';
import { getTriggerRef } from './getTriggerRef';
import {
  ARROW_PADDING,
  ARROW_PROTRUSION,
  resolveTransformOrigin,
  splitPlacement,
} from './floatingOrigin';
import { scheduleReturnFocus, useIsomorphicLayoutEffect } from './overlayFocus';
import './Popover.css';

export type PopoverPlacement = Placement;

export interface PopoverProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  /**
   * The element that opens the panel. It is cloned to receive the anchor ref,
   * `aria-expanded`, `aria-haspopup` and the activation handlers; any handler
   * or ref already on it is composed, not replaced.
   *
   * The panel is a `role="dialog"`, so it needs an accessible name: pass
   * `aria-label`, or `aria-labelledby` pointing at a heading inside it. A
   * development-mode error fires if neither is present.
   */
  trigger: ReactElement;
  /** Panel contents. */
  children?: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Preferred side/alignment. Flipped and shifted automatically to stay on screen. */
  placement?: PopoverPlacement;
  /** Distance in px between the trigger and the panel (or the arrow tip, if shown). */
  offset?: number;
  /** Renders a pointer triangle aimed at the trigger. */
  arrow?: boolean;
  /**
   * Traps focus inside the panel, hides the rest of the page from assistive
   * technology and locks page scroll. Use for a panel that must be resolved
   * before anything else can be touched; leave off for the ordinary case.
   */
  modal?: boolean;
  closeOnOutsideClick?: boolean;
  closeOnEscape?: boolean;
  /** Minimum gap kept between the panel and the viewport edge, in px. */
  collisionPadding?: number;
  /** Element focused when the panel opens. Defaults to its first tabbable child. */
  initialFocusRef?: { current: HTMLElement | null };
  /**
   * Accessible name for the hidden dismiss control rendered in `modal` mode, so
   * touch screen-reader users can leave a panel that has no visible close
   * button and no Escape key.
   */
  closeLabel?: string;
}

/* -------------------------------------------------------------------------- */

const PopoverImpl = forwardRef<HTMLDivElement, PopoverProps>(function PopoverImpl(
  {
    trigger,
    children,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    placement = 'bottom',
    offset: offsetProp = 8,
    arrow: showArrow = false,
    modal = false,
    closeOnOutsideClick = true,
    closeOnEscape = true,
    collisionPadding = 8,
    initialFocusRef,
    closeLabel = 'Close',
    className,
    style,
    ...rest
  },
  forwardedRef,
) {
  if (import.meta.env?.DEV && !rest['aria-label'] && !rest['aria-labelledby']) {
    console.error(
      '[stratum] <Popover> renders role="dialog" and requires an accessible name. ' +
        'Pass `aria-label`, or `aria-labelledby` pointing at a heading inside the panel. ' +
        'Without one a screen reader announces only "dialog".',
    );
  }

  if (import.meta.env?.DEV && !isValidElement(trigger)) {
    console.error(
      '[stratum] <Popover trigger> must be a single React element that forwards its ref and ' +
        'spreads unknown props — the anchor position and `aria-expanded` are attached to it.',
    );
  }

  const nodeId = useFloatingNodeId();
  const arrowRef = useRef<HTMLSpanElement | null>(null);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const wasOpenRef = useRef(false);

  const [open, setOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });

  const middleware = useMemo(() => {
    const list = [
      // The arrow tip, not the panel edge, should sit `offset` px from the
      // trigger — otherwise turning the arrow on visually moves the panel.
      offsetMiddleware(showArrow ? offsetProp + ARROW_PROTRUSION : offsetProp),
      flip({
        padding: collisionPadding,
        // `crossAxis: true` (the default) flips a bottom-placed panel to the
        // side as soon as it would overhang left or right — which happens for
        // any trigger near a viewport edge, and is jarring. That overflow is
        // shift()'s job; flip only reacts to the axis it actually owns.
        crossAxis: 'alignment',
        fallbackAxisSideDirection: 'end',
      }),
      // limitShift stops the panel sliding so far along a tall or wide trigger
      // that it visually detaches from it.
      shift({ padding: collisionPadding, limiter: limitShift() }),
    ];
    if (showArrow) {
      list.push(arrowMiddleware({ element: arrowRef, padding: ARROW_PADDING }));
    }
    return list;
  }, [showArrow, offsetProp, collisionPadding]);

  const {
    refs,
    floatingStyles,
    context,
    middlewareData,
    placement: resolvedPlacement,
  } = useFloating({
    nodeId,
    open,
    onOpenChange: (next) => setOpen(next),
    placement,
    // `fixed` survives a transformed or `contain`ed ancestor, which is common
    // in a panel layout. `transform: false` positions with top/left instead of
    // a transform, leaving `transform` free for the entrance animation.
    strategy: 'fixed',
    transform: false,
    whileElementsMounted: autoUpdate,
    middleware,
  });

  const click = useClick(context);
  const role = useRole(context, { role: 'dialog' });
  const dismiss = useDismiss(context, {
    escapeKey: closeOnEscape,
    outsidePress: closeOnOutsideClick,
    // Deciding on mousedown means a text selection that starts inside the panel
    // and ends outside it does not read as an outside press.
    outsidePressEvent: 'mousedown',
    // Escape closes the innermost open layer only. Without this a popover
    // opened from inside a dialog would take the dialog down with it.
    bubbles: { escapeKey: false, outsidePress: false },
  });

  const { getReferenceProps, getFloatingProps } = useInteractions([click, role, dismiss]);

  const presence = usePresence(open);
  const { side, align } = splitPlacement(resolvedPlacement);
  const transformOrigin = resolveTransformOrigin(
    { side, align },
    middlewareData.arrow?.x,
    middlewareData.arrow?.y,
  );

  const triggerElement = trigger as ReactElement<Record<string, unknown>>;
  const setTriggerRef = useMergeRefs<HTMLElement>([
    refs.setReference,
    isValidElement(trigger) ? getTriggerRef(trigger) : undefined,
  ]);
  const setPanelRef = useMergeRefs<HTMLDivElement>([
    refs.setFloating,
    presence.ref,
    panelRef,
    forwardedRef,
  ]);

  // Floating UI hands focus back to the trigger on close, but that restore is
  // lost when the close came from an outside press: Chrome re-runs focus
  // assignment on pointer release and the release lands on nothing focusable.
  // See scheduleReturnFocus.
  useIsomorphicLayoutEffect(() => {
    if (open === wasOpenRef.current) return undefined;
    wasOpenRef.current = open;
    if (open) return undefined;
    const anchor = context.elements.domReference;
    return scheduleReturnFocus({
      target: anchor instanceof HTMLElement ? anchor : null,
      panel: panelRef.current,
    });
  }, [open, context.elements.domReference]);

  const panelStyle: CSSProperties = { ...floatingStyles, transformOrigin, ...style };

  const panel = (
    <div
      {...getFloatingProps(rest as HTMLProps<HTMLElement>)}
      ref={setPanelRef}
      // Asserted after the spread so it cannot be clobbered, and so the panel
      // does not depend on `getFloatingProps` continuing to inject it. A
      // `role="dialog"` with no tabbable child is where FloatingFocusManager
      // falls back to focusing the panel itself, and `.focus()` on a div with
      // no tabindex is a no-op — focus would never leave the trigger and the
      // modal trap would have nothing to hold. Matches Dialog and Drawer.
      tabIndex={-1}
      data-stratum="popover"
      data-state={presence.state}
      data-side={side}
      data-align={align}
      data-modal={modal || undefined}
      data-arrow={showArrow || undefined}
      className={clsx('stratum-popover', className)}
      style={panelStyle}
    >
      {children}
      {showArrow && (
        <span
          ref={arrowRef}
          className="stratum-popover__arrow"
          aria-hidden="true"
          style={{
            left: middlewareData.arrow?.x != null ? `${middlewareData.arrow.x}px` : undefined,
            top: middlewareData.arrow?.y != null ? `${middlewareData.arrow.y}px` : undefined,
          }}
        />
      )}
    </div>
  );

  const managed = (
    <FloatingFocusManager
      context={context}
      modal={modal}
      // Flipping `disabled` at close returns focus to the trigger immediately
      // rather than after the exit animation, so the keyboard never sits in an
      // element that is on its way out.
      disabled={!open}
      initialFocus={initialFocusRef ?? 0}
      returnFocus
      restoreFocus
      visuallyHiddenDismiss={modal ? closeLabel : false}
    >
      {panel}
    </FloatingFocusManager>
  );

  const referenceProps = getReferenceProps({
    ...(isValidElement(trigger) ? triggerElement.props : {}),
    ref: setTriggerRef,
  } as HTMLProps<HTMLElement>);

  return (
    <FloatingNode id={nodeId}>
      {isValidElement(trigger)
        ? cloneElement(
            triggerElement,
            referenceProps as Partial<Record<string, unknown>> & Attributes,
          )
        : trigger}

      {presence.isPresent && (
        <FloatingPortal preserveTabOrder={!modal}>
          {modal ? (
            <FloatingOverlay
              lockScroll
              className="stratum-popover__overlay"
              data-stratum="popover-overlay"
              data-state={presence.state}
            >
              {managed}
            </FloatingOverlay>
          ) : (
            managed
          )}
        </FloatingPortal>
      )}
    </FloatingNode>
  );
});

/**
 * A panel anchored to a trigger, opened by click.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - The panel is `role="dialog"`. Focus moves into it on open and returns to
 *   the trigger on close — including when it closes by Escape, by an outside
 *   press, or because the consumer flipped `open` from elsewhere.
 * - `modal` additionally traps focus, marks the rest of the document
 *   `aria-hidden` and locks scroll. A modal panel also renders a
 *   visually-hidden dismiss button, because a touch screen-reader user has no
 *   Escape key and may have no visible close control.
 * - Escape closes only the innermost layer. Nesting is tracked through
 *   Floating UI's tree, so a popover inside a dialog does not dismiss both.
 * - The panel stays mounted while it animates out, so it is marked `inert` for
 *   that window; focus has already been restored by then.
 */
export const Popover = forwardRef<HTMLDivElement, PopoverProps>(function Popover(props, ref) {
  // A root overlay owns the tree; a nested one joins the tree it is already in.
  // The tree is what lets Escape and outside-press resolve innermost-first.
  const parentId = useFloatingParentNodeId();

  if (parentId === null) {
    return (
      <FloatingTree>
        <PopoverImpl {...props} ref={ref} />
      </FloatingTree>
    );
  }

  return <PopoverImpl {...props} ref={ref} />;
});

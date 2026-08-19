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
  FloatingNode,
  FloatingPortal,
  FloatingTree,
  NextFloatingDelayGroup,
  arrow as arrowMiddleware,
  autoUpdate,
  flip,
  limitShift,
  offset as offsetMiddleware,
  safePolygon,
  shift,
  useDismiss,
  useFloating,
  useFloatingNodeId,
  useFloatingParentNodeId,
  useFocus,
  useHover,
  useInteractions,
  useMergeRefs,
  useNextDelayGroup,
  useRole,
  type Placement,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { usePresence } from '../../hooks/usePresence';
import { getTriggerRef } from '../Popover/getTriggerRef';
import {
  ARROW_PADDING,
  arrowProtrusion,
  resolveTransformOrigin,
  splitPlacement,
} from '../Popover/floatingOrigin';
import './Tooltip.css';

/**
 * Edge length of the tooltip's arrow square, in px, before the 45° rotation.
 * Mirrors `--_arrow-size` in Tooltip.css (`--stratum-space-4`). A tooltip is a
 * smaller surface than a popover and carries a smaller arrow, so it does not
 * share Popover's `ARROW_SIZE` — both the offset compensation and the scale
 * origin are derived from this value instead.
 */
const TOOLTIP_ARROW_SIZE = 8;
const TOOLTIP_ARROW_PROTRUSION = arrowProtrusion(TOOLTIP_ARROW_SIZE);

export type TooltipPlacement = Placement;

/** Milliseconds before opening/closing. A bare number applies to both. */
export type TooltipDelay = number | { open?: number; close?: number };

export interface TooltipProviderProps {
  children?: ReactNode;
  /**
   * Delay applied to every tooltip in the group. Once one tooltip in the group
   * is open, moving to a sibling opens it immediately — the "warmed up" feel
   * of a toolbar — and the delay only comes back after the group goes cold.
   */
  delay?: TooltipDelay;
  /** How long the group stays warm after the last tooltip closes. */
  timeoutMs?: number;
}

/**
 * Shares one open/close delay across a group of tooltips.
 *
 * Wrap a toolbar or a table's row actions in this. Without it every tooltip
 * waits out its own open delay, which makes scanning along a row of icon
 * buttons feel like wading.
 */
export function TooltipProvider({
  children,
  delay = { open: 400, close: 120 },
  timeoutMs = 400,
}: TooltipProviderProps) {
  return (
    <NextFloatingDelayGroup delay={delay} timeoutMs={timeoutMs}>
      {children}
    </NextFloatingDelayGroup>
  );
}

export interface TooltipProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title' | 'content'> {
  /**
   * The element the tooltip describes. It is cloned to receive the anchor ref,
   * `aria-describedby` and the hover/focus handlers; handlers and refs already
   * on it are composed, not replaced.
   */
  trigger: ReactElement;
  /** Tooltip content. Keep it short — it is a description, not a panel. */
  children?: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  placement?: TooltipPlacement;
  /** Distance in px between the trigger and the tooltip (or its arrow tip). */
  offset?: number;
  arrow?: boolean;
  /** Ignored while a `TooltipProvider` group is warm. */
  delay?: TooltipDelay;
  /** Renders the trigger with no tooltip behaviour attached at all. */
  disabled?: boolean;
  /** Minimum gap kept between the tooltip and the viewport edge, in px. */
  collisionPadding?: number;
}

/* -------------------------------------------------------------------------- */

const TooltipImpl = forwardRef<HTMLDivElement, TooltipProps>(function TooltipImpl(
  {
    trigger,
    children,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    placement = 'top',
    offset: offsetProp = 6,
    arrow: showArrow = false,
    delay: delayProp = { open: 400, close: 120 },
    disabled = false,
    collisionPadding = 8,
    className,
    style,
    ...rest
  },
  forwardedRef,
) {
  if (import.meta.env?.DEV && !isValidElement(trigger)) {
    console.error(
      '[stratum] <Tooltip trigger> must be a single React element that forwards its ref and ' +
        'spreads unknown props — `aria-describedby` is attached to it.',
    );
  }

  const nodeId = useFloatingNodeId();
  const arrowRef = useRef<HTMLSpanElement | null>(null);

  const [openState, setOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });
  const open = openState && !disabled;

  const middleware = useMemo(() => {
    const list = [
      offsetMiddleware(showArrow ? offsetProp + TOOLTIP_ARROW_PROTRUSION : offsetProp),
      // Cross-axis overhang is shift()'s problem; letting flip() react to it
      // sends a tooltip on a left-edge button out to the side for no reason.
      flip({
        padding: collisionPadding,
        crossAxis: 'alignment',
        fallbackAxisSideDirection: 'start',
      }),
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
    strategy: 'fixed',
    // Positioning via top/left leaves `transform` free for the entrance scale.
    transform: false,
    whileElementsMounted: autoUpdate,
    middleware,
  });

  const { delayRef, isInstantPhase, hasProvider } = useNextDelayGroup(context, {
    enabled: !disabled,
  });

  const hover = useHover(context, {
    enabled: !disabled,
    // Touch has no hover. Without this a tap opens a tooltip that then has no
    // pointer-leave to close it.
    mouseOnly: true,
    // Required companion to `referencePress` below, and it stops a tooltip
    // reopening from a stray mousemove after it was dismissed.
    move: false,
    // WCAG 2.2 SC 1.4.13 "Hoverable": the pointer must be able to travel onto
    // the tooltip without it vanishing. safePolygon keeps it open along that
    // path only, and never blocks pointer events on the rest of the page.
    handleClose: safePolygon({ blockPointerEvents: false }),
    delay: () => (hasProvider ? delayRef.current : delayProp),
  });

  // Keyboard parity: a tooltip that only appears on hover is invisible to a
  // keyboard user. `useFocus` opens on focus-visible, so it does not fire for
  // a mouse click on the trigger.
  const focus = useFocus(context, { enabled: !disabled });

  const dismiss = useDismiss(context, {
    enabled: !disabled,
    // WCAG 2.2 SC 1.4.13 "Dismissible": Escape must remove the tooltip without
    // moving pointer or focus.
    escapeKey: true,
    // Activating the trigger hides the tooltip so it cannot sit on top of
    // whatever that activation opened.
    referencePress: true,
    referencePressEvent: 'click',
    // A tooltip is not a layer you dismiss by clicking away from it.
    outsidePress: false,
    // Escape belongs to the innermost layer: a tooltip inside a dialog must not
    // close the dialog too.
    bubbles: { escapeKey: false },
  });

  const role = useRole(context, { role: 'tooltip', enabled: !disabled });

  const { getReferenceProps, getFloatingProps } = useInteractions([hover, focus, dismiss, role]);

  const presence = usePresence(open);
  const { side, align } = splitPlacement(resolvedPlacement);
  const transformOrigin = resolveTransformOrigin(
    { side, align },
    middlewareData.arrow?.x,
    middlewareData.arrow?.y,
    TOOLTIP_ARROW_SIZE,
  );

  const triggerElement = trigger as ReactElement<Record<string, unknown>>;
  const setTriggerRef = useMergeRefs<HTMLElement>([
    refs.setReference,
    isValidElement(trigger) ? getTriggerRef(trigger) : undefined,
  ]);
  const setPanelRef = useMergeRefs<HTMLDivElement>([
    refs.setFloating,
    presence.ref,
    forwardedRef,
  ]);

  const panelStyle: CSSProperties = { ...floatingStyles, transformOrigin, ...style };

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
        <FloatingPortal>
          <div
            {...getFloatingProps(rest as HTMLProps<HTMLElement>)}
            ref={setPanelRef}
            data-stratum="tooltip"
            data-state={presence.state}
            data-side={side}
            data-align={align}
            data-arrow={showArrow || undefined}
            data-instant={isInstantPhase || undefined}
            className={clsx('stratum-tooltip', className)}
            style={panelStyle}
          >
            {children}
            {showArrow && (
              <span
                ref={arrowRef}
                className="stratum-tooltip__arrow"
                aria-hidden="true"
                style={{
                  left: middlewareData.arrow?.x != null ? `${middlewareData.arrow.x}px` : undefined,
                  top: middlewareData.arrow?.y != null ? `${middlewareData.arrow.y}px` : undefined,
                }}
              />
            )}
          </div>
        </FloatingPortal>
      )}
    </FloatingNode>
  );
});

/**
 * A short description anchored to a control, shown on hover and on keyboard
 * focus.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - Opens on hover AND on focus-visible. A hover-only tooltip is simply absent
 *   for keyboard users, and this is the single most common tooltip defect.
 * - Escape closes it (WCAG 2.2 SC 1.4.13, "Dismissible") without the pointer or
 *   focus having to move, and without disturbing any layer beneath it.
 * - The pointer can travel onto the tooltip and it stays open ("Hoverable"),
 *   but nothing outside it is ever made unclickable — no pointer trapping, no
 *   focus trapping, and the tooltip itself is never focusable.
 * - It is wired as `aria-describedby`, not as the accessible name. A control
 *   whose only label is a tooltip is unlabelled for a touch screen-reader
 *   user; give it an `aria-label` as well.
 * - Never put interactive content in a tooltip. There is no accessible way to
 *   reach it. Use `<Popover>` for that.
 */
export const Tooltip = forwardRef<HTMLDivElement, TooltipProps>(function Tooltip(props, ref) {
  // Root tooltips own a tree; a tooltip inside another overlay joins that one,
  // which is what keeps Escape resolving innermost-first.
  const parentId = useFloatingParentNodeId();

  if (parentId === null) {
    return (
      <FloatingTree>
        <TooltipImpl {...props} ref={ref} />
      </FloatingTree>
    );
  }

  return <TooltipImpl {...props} ref={ref} />;
});

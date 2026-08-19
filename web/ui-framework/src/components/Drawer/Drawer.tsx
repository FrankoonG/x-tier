import {
  forwardRef,
  useId,
  useRef,
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
import { usePresence } from '../../hooks/usePresence';
import { useScrollLock } from '../../hooks/useScrollLock';
import {
  resolveReturnFocusTarget,
  scheduleReturnFocus,
  useIsomorphicLayoutEffect,
} from '../Popover/overlayFocus';
import './Drawer.css';

export type DrawerSide = 'left' | 'right' | 'top' | 'bottom';
export type DrawerSize = 'sm' | 'md' | 'lg' | 'xl' | 'full';

export interface DrawerProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  open: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Edge the sheet is docked to. */
  side?: DrawerSide;
  /** Width for `left`/`right`, height for `top`/`bottom`. */
  size?: DrawerSize;
  /** Accessible name for the sheet. Required — a dialog without one is unusable. */
  title: ReactNode;
  description?: ReactNode;
  footer?: ReactNode;
  /** Allows Escape, an outside press and the close button to close the sheet. */
  dismissible?: boolean;
  /** Defaults to `dismissible`. */
  showCloseButton?: boolean;
  closeLabel?: string;
  /** Keeps `title` as the accessible name but removes it from the visual design. */
  hideTitle?: boolean;
  /**
   * Element focused on open. Defaults to the sheet itself, so a screen reader
   * announces its name and description before its controls.
   */
  initialFocusRef?: { current: HTMLElement | null };
  /**
   * Element focused when the sheet closes. Defaults to whatever had focus at
   * the moment it opened.
   */
  returnFocusRef?: { current: HTMLElement | null };
  children?: ReactNode;
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

const DrawerImpl = forwardRef<HTMLDivElement, DrawerProps>(function DrawerImpl(
  {
    open,
    onOpenChange,
    side = 'right',
    size = 'md',
    title,
    description,
    footer,
    dismissible = true,
    showCloseButton,
    closeLabel = 'Close',
    hideTitle = false,
    initialFocusRef,
    returnFocusRef,
    children,
    className,
    ...rest
  },
  forwardedRef,
) {
  const nodeId = useFloatingNodeId();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const capturedReturnRef = useRef<HTMLElement | null>(null);
  const wasOpenRef = useRef(false);
  const generatedId = useId();
  const titleId = `${generatedId}-title`;
  const descriptionId = `${generatedId}-description`;

  const showClose = showCloseButton ?? dismissible;

  // Geometry is LATCHED at open time and reused for the sheet's whole life,
  // including its exit animation.
  //
  // Without this, the exit slides the wrong way. A caller that stores the side
  // in the same state that drives `open` — `const [side, setSide] = useState<
  // Side | null>(null)`, closing with `setSide(null)` — hands us the default
  // `'right'` on the very render that starts the exit, while the sheet is still
  // mounted. A drawer opened from the left then flew out to the right. This is
  // the same class of bug `Dialog` avoids by capturing its origin once, and it
  // has to be solved in the component: any consumer managing state that way
  // would hit it otherwise.
  //
  // Writing a ref during render is normally forbidden here, and this is the
  // narrow exception: the write is IDEMPOTENT. React's concurrent double-invoke
  // stores the same value twice, which is indistinguishable from storing it
  // once. That is precisely what made hy2scale's `count.current++` unsafe and
  // makes this safe.
  const latchedGeometry = useRef({ side, size });
  if (open) latchedGeometry.current = { side, size };
  const effectiveSide = open ? side : latchedGeometry.current.side;
  const effectiveSize = open ? size : latchedGeometry.current.size;

  // On open this runs while `open` is already true but before `usePresence` has
  // mounted the sheet, so the reading is taken before focus is pulled inside.
  useIsomorphicLayoutEffect(() => {
    if (open === wasOpenRef.current) return undefined;
    wasOpenRef.current = open;

    if (open) {
      capturedReturnRef.current = resolveReturnFocusTarget(returnFocusRef);
      return undefined;
    }

    return scheduleReturnFocus({
      target: capturedReturnRef.current,
      panel: panelRef.current,
    });
  }, [open, returnFocusRef]);

  const { refs, context } = useFloating({
    nodeId,
    open,
    onOpenChange: (next) => onOpenChange?.(next),
  });

  const role = useRole(context, { role: 'dialog' });
  const dismiss = useDismiss(context, {
    enabled: dismissible,
    escapeKey: true,
    outsidePress: true,
    outsidePressEvent: 'mousedown',
    bubbles: { escapeKey: false, outsidePress: false },
  });

  const { getFloatingProps } = useInteractions([role, dismiss]);

  const presence = usePresence(open);

  // Floating UI's own `lockScroll` freezes `document.body` only, which does

  // nothing in an app shell where an inner element is the scroller — the page

  // behind the scrim scrolled freely. This blocks scroll intent outside the

  // panel while leaving the panel's own body scrollable.

  useScrollLock(open, panelRef);
  const setPanelRef = useMergeRefs<HTMLDivElement>([refs.setFloating, panelRef, forwardedRef]);

  const headerIsBare = hideTitle && description == null && !showClose;

  if (!presence.isPresent) return null;

  return (
    <FloatingNode id={nodeId}>
      <FloatingPortal>
        <FloatingOverlay
          lockScroll
          ref={presence.ref}
          data-stratum="drawer-overlay"
          data-state={presence.state}
          data-side={effectiveSide}
          className="stratum-drawer__overlay"
        >
          <div className="stratum-drawer__scrim" data-state={presence.state} aria-hidden="true" />

          <FloatingFocusManager
            context={context}
            modal
            disabled={!open}
            // The sheet itself, not its first control: a screen reader should
            // read what opened, not "Close, button".
            initialFocus={initialFocusRef ?? panelRef}
            returnFocus={capturedReturnRef}
            restoreFocus
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
              data-stratum="drawer"
              data-state={presence.state}
              data-side={effectiveSide}
              data-size={effectiveSize}
              className={clsx('stratum-drawer', className)}
            >
              <div className="stratum-drawer__header" data-bare={headerIsBare || undefined}>
                <div className="stratum-drawer__heading">
                  <h2
                    id={titleId}
                    className={clsx(
                      'stratum-drawer__title',
                      hideTitle && 'stratum-visually-hidden',
                    )}
                  >
                    {title}
                  </h2>
                  {description != null && (
                    <p id={descriptionId} className="stratum-drawer__description">
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
                    className="stratum-drawer__close"
                    onClick={() => onOpenChange?.(false)}
                  />
                )}
              </div>

              <div className="stratum-drawer__body">{children}</div>

              {footer != null && <div className="stratum-drawer__footer">{footer}</div>}
            </div>
          </FloatingFocusManager>
        </FloatingOverlay>
      </FloatingPortal>
    </FloatingNode>
  );
});

/**
 * A modal side sheet.
 *
 * Same contract as `<Dialog>` — focus trap, focus restore, scroll lock,
 * `aria-modal`, labelled by `title` — but docked to an edge and entering by
 * sliding in from it rather than by zooming out of its trigger. Use it when the
 * content is a list or a form long enough that a centred panel would fight the
 * page for vertical space.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - Focus moves into the sheet on open and returns to the trigger at close, not
 *   at the end of the slide-out.
 * - Escape closes the innermost layer only, so a popover opened inside the
 *   sheet is dismissed first.
 * - Under `prefers-reduced-motion` the slide collapses to nothing —
 *   `--stratum-motion-distance` zeroes the travel — and the sheet simply fades,
 *   which is the point of the preference: no large translation.
 */
export const Drawer = forwardRef<HTMLDivElement, DrawerProps>(function Drawer(props, ref) {
  const parentId = useFloatingParentNodeId();

  if (parentId === null) {
    return (
      <FloatingTree>
        <DrawerImpl {...props} ref={ref} />
      </FloatingTree>
    );
  }

  return <DrawerImpl {...props} ref={ref} />;
});

import {
  forwardRef,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';
import {
  FloatingFocusManager,
  FloatingList,
  FloatingNode,
  FloatingPortal,
  FloatingTree,
  autoUpdate,
  flip,
  shift,
  size,
  useDismiss,
  useFloating,
  useFloatingNodeId,
  useFloatingTree,
  useInteractions,
  useListNavigation,
  useMergeRefs,
  useRole,
  useTypeahead,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { MenuContext, type MenuContextValue } from '../Menu/Menu';
import { usePresence } from '../../hooks/usePresence';
import './ContextMenu.css';

const VIEWPORT_PADDING = 8;
const EVENT_CLOSE_ALL = 'stratum:menu:close-all';

export interface ContextMenuProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onSelect'> {
  /**
   * Menu content — the same `MenuItem` / `MenuSeparator` / `MenuLabel` /
   * `MenuCheckboxItem` / `MenuRadioGroup` set the dropdown `Menu` takes, and
   * nested `<Menu>` elements work as submenus.
   */
  menu: ReactNode;
  /**
   * Accessible name for the popup, and for the region itself while that region
   * is a tab stop — a stop with no name announces as nothing at all. Override
   * either one by passing `aria-label` / `aria-labelledby` through `...rest`.
   */
  label?: string;
  /** Region the gesture applies to. */
  children?: ReactNode;
  disabled?: boolean;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * Whether the region is a tab stop. Default `true`, because the keyboard
   * path (`Shift`+`F10` / the context-menu key) needs somewhere to fire from
   * when the region contains no focusable content of its own. Set `false` when
   * the region already holds focusable rows and the extra stop is noise.
   *
   * A `disabled` region is never a tab stop regardless: it opens nothing, so
   * the stop would be inert.
   */
  focusable?: boolean;
}

/**
 * A menu opened at pointer coordinates by `contextmenu`.
 *
 * KEYBOARD PARITY
 * ---------------
 * `Shift`+`F10` and the Windows context-menu key are handled explicitly rather
 * than being left to the browser's own `contextmenu` synthesis. Both are the
 * *default action* of that keydown, so preventing it and opening ourselves is
 * deterministic — otherwise engines disagree about the coordinates they
 * report (Chrome uses the focused element's box, others report `0,0`, and the
 * menu lands in the top-left corner of the viewport).
 *
 * A `contextmenu` event that still arrives without a secondary-button press is
 * treated as keyboard-initiated and anchored to the focused element rather
 * than to `0,0`, which covers engines that fire it from other key bindings.
 */
export const ContextMenu = forwardRef<HTMLDivElement, ContextMenuProps>(function ContextMenu(
  props,
  ref,
) {
  return (
    <FloatingTree>
      <ContextMenuRoot {...props} ref={ref} />
    </FloatingTree>
  );
});

const ContextMenuRoot = forwardRef<HTMLDivElement, ContextMenuProps>(function ContextMenuRoot(
  {
    menu,
    label = 'Context menu',
    children,
    disabled = false,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    focusable = true,
    className,
    onContextMenu,
    onKeyDown,
    ...rest
  },
  forwardedRef,
) {
  const [isOpen, setIsOpen] = useControllableState<boolean>({
    value: openProp,
    defaultValue: defaultOpen,
    onChange: onOpenChange,
  });
  const [activeIndex, setActiveIndex] = useState<number | null>(null);

  const elementsRef = useRef<Array<HTMLElement | null>>([]);
  const labelsRef = useRef<Array<string | null>>([]);
  const targetRef = useRef<HTMLDivElement | null>(null);

  const tree = useFloatingTree();
  const nodeId = useFloatingNodeId();

  const { refs, floatingStyles, context } = useFloating<HTMLElement>({
    nodeId,
    open: isOpen,
    onOpenChange: setIsOpen,
    placement: 'bottom-start',
    strategy: 'fixed',
    // Position with top/left, not a transform. Floating UI's default writes
    // `transform: translate(x, y)` inline, which collides with this surface's
    // entrance animation twice over: the inline value overrides the CSS
    // `transform`, and `transition: transform` then animates the very first
    // positioning step from `translate(0, 0)` — so the panel visibly flew in
    // from the top-left corner on its first open and only looked right on
    // subsequent ones, once a previous value existed to interpolate from.
    transform: false,
    whileElementsMounted: autoUpdate,
    middleware: [
      // A pointer anchor is a zero-area point, so the menu is flipped to the
      // opposite corner near an edge rather than merely nudged: sliding it
      // would put it under the cursor and swallow the first click.
      flip({
        padding: VIEWPORT_PADDING,
        fallbackPlacements: ['bottom-end', 'top-start', 'top-end'],
      }),
      shift({ padding: VIEWPORT_PADDING }),
      size({
        padding: VIEWPORT_PADDING,
        apply({ availableHeight, elements }) {
          elements.floating.style.setProperty('--_available-height', `${availableHeight}px`);
        },
      }),
    ],
  });

  const role = useRole(context, { role: 'menu' });
  const dismiss = useDismiss(context, { bubbles: true });
  const listNavigation = useListNavigation(context, {
    listRef: elementsRef,
    activeIndex,
    onNavigate: setActiveIndex,
    loop: true,
  });
  const typeahead = useTypeahead(context, {
    listRef: labelsRef,
    onMatch: isOpen ? setActiveIndex : undefined,
    activeIndex,
  });

  const { getFloatingProps, getItemProps } = useInteractions([
    role,
    dismiss,
    listNavigation,
    typeahead,
  ]);

  useEffect(() => {
    if (!tree) return undefined;
    function handleCloseAll() {
      setIsOpen(false);
    }
    tree.events.on(EVENT_CLOSE_ALL, handleCloseAll);
    return () => tree.events.off(EVENT_CLOSE_ALL, handleCloseAll);
  }, [tree, setIsOpen]);

  const openAtPoint = useCallback(
    (x: number, y: number) => {
      refs.setPositionReference({
        getBoundingClientRect: () => ({
          width: 0,
          height: 0,
          x,
          y,
          top: y,
          right: x,
          bottom: y,
          left: x,
        }),
      });
      setActiveIndex(null);
      setIsOpen(true);
    },
    [refs, setIsOpen],
  );

  const openAtElement = useCallback(
    (element: Element) => {
      // A real element keeps its rect live, so the menu follows if the row it
      // was invoked from scrolls.
      refs.setPositionReference(element);
      // This is the keyboard path, and there is no pointer to imply a starting
      // row. Seeding the first item means the menu opens already highlighted
      // and the first ArrowDown advances instead of re-selecting.
      setActiveIndex(0);
      setIsOpen(true);
    },
    [refs, setIsOpen],
  );

  const menuContextValue = useMemo<MenuContextValue>(
    () => ({
      activeIndex,
      setActiveIndex,
      getItemProps,
      setHasFocusInside: () => undefined,
      isOpen,
      closeAll: () => tree?.events.emit(EVENT_CLOSE_ALL),
    }),
    [activeIndex, getItemProps, isOpen, tree],
  );

  const targetMergedRef = useMergeRefs([targetRef, forwardedRef]);
  // Keeps the surface mounted for the length of its exit transition — see the
  // note on the exit rules in Menu.css (this surface reuses .stratum-menu).
  const presence = usePresence(isOpen);
  const surfaceRef = useMergeRefs([refs.setFloating, presence.ref]);
  const [side = 'bottom', align = 'start'] = context.placement.split('-');

  // A tab stop that a screen reader announces as nothing at all is a WCAG 4.1.2
  // failure, and this wrapper is the component's own keyboard entry point. So
  // whenever it *is* a stop it gets a role that accepts a name plus that name.
  // `group` is a structure role and therefore legitimate on a plain div, unlike
  // the `aria-haspopup`/`aria-expanded` pair noted below. A disabled region
  // opens nothing (see handleContextMenu), so it is not a stop either.
  const isTabStop = focusable && !disabled;
  const targetRole = rest.role ?? (isTabStop ? 'group' : undefined);
  const targetLabel =
    rest['aria-label'] ??
    (isTabStop && rest['aria-labelledby'] == null && targetRole != null ? label : undefined);

  function handleContextMenu(event: ReactMouseEvent<HTMLDivElement>) {
    onContextMenu?.(event);
    if (disabled || event.defaultPrevented) return;
    event.preventDefault();

    // `button === 2` is the only cross-engine reliable marker of a real
    // secondary-button press; anything else reached us from the keyboard.
    if (event.button === 2) {
      openAtPoint(event.clientX, event.clientY);
      return;
    }
    const focused = document.activeElement;
    openAtElement(focused instanceof Element && event.currentTarget.contains(focused)
      ? focused
      : event.currentTarget);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    onKeyDown?.(event);
    if (disabled || event.defaultPrevented) return;
    if (event.key !== 'ContextMenu' && !(event.key === 'F10' && event.shiftKey)) return;

    // Suppressing the default action stops the engine synthesising its own
    // `contextmenu` at coordinates we did not choose.
    event.preventDefault();
    const target = event.target;
    openAtElement(target instanceof Element ? target : event.currentTarget);
  }

  return (
    <FloatingNode id={nodeId}>
      <div
        {...rest}
        ref={targetMergedRef}
        data-stratum="context-menu-target"
        data-state={isOpen ? 'open' : 'closed'}
        data-disabled={disabled || undefined}
        // No `aria-haspopup`/`aria-expanded`: neither is a global attribute,
        // and `group` does not permit them — announcing the menu that way is an
        // ARIA violation, not an improvement. A context menu is an OS-level
        // convention that native applications do not advertise either. Both of
        // these fall back to `...rest`, so your own `role` and `aria-label`
        // still win for a region that needs naming differently.
        role={targetRole}
        aria-label={targetLabel}
        tabIndex={rest.tabIndex ?? (isTabStop ? 0 : undefined)}
        className={clsx('stratum-context-menu-target', className)}
        onContextMenu={handleContextMenu}
        onKeyDown={handleKeyDown}
      >
        {children}
      </div>

      <MenuContext.Provider value={menuContextValue}>
        <FloatingList elementsRef={elementsRef} labelsRef={labelsRef}>
          {presence.isPresent && (
            /* See the note in Menu: the tab-order guards would land inside the
             * region the gesture applies to, and a menu is a single tab stop
             * navigated by arrow keys. */
            <FloatingPortal preserveTabOrder={false}>
              <FloatingFocusManager context={context} modal={false} initialFocus={0}>
                <div
                  ref={surfaceRef}
                  style={floatingStyles}
                  data-stratum="menu"
                  data-context-menu=""
                  data-state={presence.state}
                  data-side={side}
                  data-align={align}
                  className="stratum-menu"
                  {...getFloatingProps({
                    // `useRole` points `aria-labelledby` at the DOM reference,
                    // and a pointer-anchored menu has none. User props win the
                    // merge, so this replaces the dangling reference outright.
                    'aria-label': label,
                    'aria-labelledby': undefined,
                    onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
                      // See the note in Menu: one tab stop, so Tab closes
                      // rather than wandering into a focus guard.
                      if (event.key !== 'Tab') return;
                      event.preventDefault();
                      tree?.events.emit(EVENT_CLOSE_ALL);
                    },
                  })}
                >
                  {menu}
                </div>
              </FloatingFocusManager>
            </FloatingPortal>
          )}
        </FloatingList>
      </MenuContext.Provider>
    </FloatingNode>
  );
});

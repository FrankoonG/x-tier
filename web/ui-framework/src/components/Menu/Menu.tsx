import {
  cloneElement,
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  version as reactVersion,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type FocusEvent as ReactFocusEvent,
  type HTMLAttributes,
  type HTMLProps,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactElement,
  type ReactNode,
  type Ref,
} from 'react';
import {
  FloatingFocusManager,
  FloatingList,
  FloatingNode,
  FloatingPortal,
  FloatingTree,
  autoUpdate,
  flip,
  offset,
  safePolygon,
  shift,
  size,
  useClick,
  useDismiss,
  useFloating,
  useFloatingNodeId,
  useFloatingParentNodeId,
  useFloatingTree,
  useHover,
  useInteractions,
  useListItem,
  useListNavigation,
  useMergeRefs,
  useRole,
  useTypeahead,
  type Placement,
} from '@floating-ui/react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { usePresence } from '../../hooks/usePresence';
import './Menu.css';

/* ---------------------------------------------------------------------------
 * Positioning geometry.
 *
 * These are the only raw numbers in the file. Floating UI's middleware takes
 * numeric pixel offsets — there is no way to hand it a custom property without
 * losing collision detection, because `flip`/`shift` must know the real
 * distance before layout. They mirror the 4px/8px steps of the spacing scale.
 * ------------------------------------------------------------------------- */
const MENU_GAP = 4;
const SUBMENU_MAIN_GAP = 0;
const SUBMENU_CROSS_GAP = -4;
const VIEWPORT_PADDING = 8;
/** Hover-with-intent: long enough that dragging across a submenu row on the
 *  way to a lower item does not open it, short enough to feel immediate. */
const SUBMENU_OPEN_DELAY = 90;

/** Namespaced so we never collide with another Floating UI tree consumer. */
const EVENT_CLOSE_ALL = 'stratum:menu:close-all';
const EVENT_MENU_OPEN = 'stratum:menu:open';

interface SubmenuOpenEvent {
  nodeId: string | undefined;
  parentId: string | null;
}

export type MenuGetItemProps = (
  userProps?: Omit<HTMLProps<HTMLElement>, 'selected' | 'active'> & {
    active?: boolean;
    selected?: boolean;
  },
) => Record<string, unknown>;

export interface MenuContextValue {
  activeIndex: number | null;
  setActiveIndex: (index: number | null) => void;
  getItemProps: MenuGetItemProps;
  setHasFocusInside: (value: boolean) => void;
  isOpen: boolean;
  /**
   * Collapses every menu in the tree. Called after an item whose
   * `closeOnSelect` is `true` has run its handler.
   */
  closeAll: () => void;
}

/**
 * Shared by `Menu` and `ContextMenu` so both can host the same item set.
 * The default `getItemProps` is a pass-through rather than `() => ({})` so a
 * root trigger's own props survive when there is no parent menu.
 */
export const MenuContext = createContext<MenuContextValue>({
  activeIndex: null,
  setActiveIndex: () => undefined,
  getItemProps: (userProps) => (userProps ?? {}) as Record<string, unknown>,
  setHasFocusInside: () => undefined,
  isOpen: false,
  closeAll: () => undefined,
});

interface MenuRadioGroupContextValue {
  value: string | undefined;
  setValue: (value: string) => void;
}

const MenuRadioGroupContext = createContext<MenuRadioGroupContextValue | null>(null);

/* ---------------------------------------------------------------------------
 * Icons. Decorative only — every one is `aria-hidden` at the call site.
 * ------------------------------------------------------------------------- */
function ChevronRightIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false" aria-hidden="true">
      <path
        d="m6 4 4 4-4 4"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" focusable="false" aria-hidden="true">
      <path
        d="m3.5 8.5 3 3 6-7"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function DotIcon() {
  return (
    <svg viewBox="0 0 16 16" focusable="false" aria-hidden="true">
      <circle cx="8" cy="8" r="3.25" fill="currentColor" />
    </svg>
  );
}

/**
 * React 19 moved `ref` into `props`; React 18 keeps it on the element and
 * warns loudly in 19 if you read the legacy slot. Read props first, and only
 * fall back on the legacy slot when the running React is actually 18.
 */
function getElementRef(element: ReactElement): Ref<HTMLElement> | undefined {
  const props = element.props as { ref?: Ref<HTMLElement> } | null | undefined;
  if (props?.ref != null) return props.ref;
  const major = Number.parseInt(reactVersion, 10);
  if (Number.isFinite(major) && major >= 19) return undefined;
  return (element as unknown as { ref?: Ref<HTMLElement> }).ref;
}

/* =========================================================================
 * Menu
 * ======================================================================= */

export interface MenuProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onSelect'> {
  /**
   * The element that opens the menu. Required at the top level, ignored in a
   * nested menu — a submenu renders its own row inside its parent.
   *
   * It is cloned with the reference props (`aria-expanded`, `aria-haspopup`,
   * `id`, handlers, ref), so it must forward its ref to a real element.
   */
  trigger?: ReactElement;
  /**
   * Text of the submenu row. **Required on a nested `Menu`** — it is both the
   * visible label and, via `aria-labelledby`, the submenu's accessible name.
   */
  label?: string;
  /** Leading adornment on the submenu row. Nested menus only. */
  icon?: ReactNode;
  /** Preferred side/alignment. Ignored for submenus, which always open inline. */
  placement?: Placement;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Blocks opening. On a submenu the row is also marked `aria-disabled`. */
  disabled?: boolean;
  /** Applied to the submenu row. Nested menus only. */
  triggerClassName?: string;
  children?: ReactNode;
}

/**
 * A dropdown action menu.
 *
 * KEYBOARD
 * --------
 * `ArrowDown`/`ArrowUp` move with wrap, `Home`/`End` jump to the ends,
 * printable characters type-ahead, `Enter`/`Space` activate, `ArrowRight`
 * opens a submenu, `ArrowLeft` closes it and returns to its row, `Escape`
 * closes the menu and restores focus to the trigger.
 *
 * FOCUS, NOT `aria-activedescendant`
 * ---------------------------------
 * Menu items take real DOM focus (roving `tabindex`). Items are `<button>`s,
 * so `Enter` and `Space` activate them through the platform rather than
 * through a hand-written key handler — which is also what makes them work
 * with speech control and switch access, neither of which synthesises key
 * events.
 *
 * WHY THERE IS NO EXIT ANIMATION
 * ------------------------------
 * The menu owns focus while it is open, so it must unmount in the same task
 * that closes it: `FloatingFocusManager` restores focus in its cleanup, and
 * deferring that behind a 120ms transition puts the restore in a race with
 * whatever the activated item just did. Entry is animated with
 * `@starting-style` — which needs no JS at all — and closing is immediate.
 * See `Menu.css`.
 */
export const Menu = forwardRef<HTMLDivElement, MenuProps>(function Menu(props, ref) {
  const parentId = useFloatingParentNodeId();

  // The root menu owns the tree that submenus register into.
  if (parentId === null) {
    return (
      <FloatingTree>
        <MenuSurface {...props} ref={ref} />
      </FloatingTree>
    );
  }

  return <MenuSurface {...props} ref={ref} />;
});

const MenuSurface = forwardRef<HTMLDivElement, MenuProps>(function MenuSurface(
  {
    trigger,
    label,
    icon,
    placement,
    open: openProp,
    defaultOpen = false,
    onOpenChange,
    disabled = false,
    triggerClassName,
    className,
    style,
    children,
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
  const [hasFocusInside, setHasFocusInside] = useState(false);

  const elementsRef = useRef<Array<HTMLElement | null>>([]);
  const labelsRef = useRef<Array<string | null>>([]);

  const parent = useContext(MenuContext);
  const tree = useFloatingTree();
  const nodeId = useFloatingNodeId();
  const parentId = useFloatingParentNodeId();
  const isNested = parentId != null;

  // Registers the submenu row in the parent's list so arrow navigation reaches
  // it. At the root there is no enclosing FloatingList and this is inert.
  const item = useListItem({ label: isNested ? (disabled ? null : label) : null });

  if (import.meta.env?.DEV) {
    if (!isNested && !trigger) {
      console.error(
        '[stratum] <Menu> requires a `trigger` element at the top level. ' +
          'A nested <Menu> uses `label` instead and renders its own row.',
      );
    }
    if (isNested && !label) {
      console.error(
        '[stratum] A nested <Menu> requires `label`. It is both the visible ' +
          'submenu row text and the submenu’s accessible name.',
      );
    }
  }

  const { refs, floatingStyles, context } = useFloating<HTMLElement>({
    nodeId,
    open: isOpen,
    onOpenChange: setIsOpen,
    placement: isNested ? 'right-start' : (placement ?? 'bottom-start'),
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
      offset({
        mainAxis: isNested ? SUBMENU_MAIN_GAP : MENU_GAP,
        alignmentAxis: isNested ? SUBMENU_CROSS_GAP : 0,
      }),
      flip({ padding: VIEWPORT_PADDING }),
      shift({ padding: VIEWPORT_PADDING }),
      size({
        padding: VIEWPORT_PADDING,
        apply({ availableHeight, elements }) {
          // Handed to CSS rather than applied directly, so the ceiling stays
          // a style decision and this stays a measurement.
          elements.floating.style.setProperty('--_available-height', `${availableHeight}px`);
        },
      }),
    ],
  });

  // `safePolygon` keeps a submenu open while the pointer travels diagonally
  // toward it — the difference between a usable nested menu and one that
  // closes out from under the cursor. The open delay is the "intent" half:
  // dragging across a submenu row on the way to a lower item must not open it.
  const hover = useHover(context, {
    enabled: isNested && !disabled,
    delay: { open: SUBMENU_OPEN_DELAY },
    handleClose: safePolygon({ blockPointerEvents: true }),
  });
  const click = useClick(context, {
    event: 'mousedown',
    toggle: !isNested,
    ignoreMouse: isNested,
    enabled: !disabled,
  });
  const role = useRole(context, { role: 'menu' });
  // `bubbles` closes the whole stack on Escape and on outside press. Closing
  // only the innermost submenu would strand focus, because a submenu returns
  // focus through its parent row rather than owning the restore itself;
  // ArrowLeft is the APG path for "close just this level".
  const dismiss = useDismiss(context, { bubbles: true });
  const listNavigation = useListNavigation(context, {
    listRef: elementsRef,
    activeIndex,
    nested: isNested,
    onNavigate: setActiveIndex,
    loop: true,
  });
  const typeahead = useTypeahead(context, {
    listRef: labelsRef,
    onMatch: isOpen ? setActiveIndex : undefined,
    activeIndex,
  });

  const { getReferenceProps, getFloatingProps, getItemProps } = useInteractions([
    hover,
    click,
    role,
    dismiss,
    listNavigation,
    typeahead,
  ]);

  // Close on any sibling opening, and on a tree-wide close request.
  useEffect(() => {
    if (!tree) return undefined;

    function handleCloseAll() {
      setIsOpen(false);
    }
    function handleSubmenuOpen(event: SubmenuOpenEvent) {
      if (event.nodeId !== nodeId && event.parentId === parentId) {
        setIsOpen(false);
      }
    }

    tree.events.on(EVENT_CLOSE_ALL, handleCloseAll);
    tree.events.on(EVENT_MENU_OPEN, handleSubmenuOpen);
    return () => {
      tree.events.off(EVENT_CLOSE_ALL, handleCloseAll);
      tree.events.off(EVENT_MENU_OPEN, handleSubmenuOpen);
    };
  }, [tree, nodeId, parentId, setIsOpen]);

  useEffect(() => {
    if (isOpen && tree) {
      tree.events.emit(EVENT_MENU_OPEN, { nodeId, parentId } satisfies SubmenuOpenEvent);
    }
  }, [tree, isOpen, nodeId, parentId]);

  const menuContextValue = useMemo<MenuContextValue>(
    () => ({
      activeIndex,
      setActiveIndex,
      getItemProps,
      setHasFocusInside,
      isOpen,
      closeAll: () => tree?.events.emit(EVENT_CLOSE_ALL),
    }),
    [activeIndex, getItemProps, isOpen, tree],
  );

  // Keeps the surface mounted for the length of its exit transition — see the
  // note on the exit rules in Menu.css.
  const presence = usePresence(isOpen);
  const surfaceRef = useMergeRefs([refs.setFloating, forwardedRef, presence.ref]);
  const surfaceStyle: CSSProperties = { ...floatingStyles, ...style };
  const [side = 'bottom', align = 'start'] = context.placement.split('-');

  const triggerRef = useMergeRefs([
    refs.setReference,
    item.ref,
    trigger && !isNested ? getElementRef(trigger) : undefined,
  ]);

  const isActiveRow = isNested && parent.activeIndex === item.index && item.index !== -1;

  const referenceProps = getReferenceProps(
    parent.getItemProps({
      ...(!isNested && trigger ? (trigger.props as HTMLProps<HTMLElement>) : {}),
      onFocus(event: ReactFocusEvent<HTMLElement>) {
        if (!isNested && trigger) {
          (trigger.props as HTMLProps<HTMLElement>).onFocus?.(event);
        }
        setHasFocusInside(false);
        parent.setHasFocusInside(true);
      },
    }),
  );

  let triggerNode: ReactNode;
  if (isNested) {
    triggerNode = (
      <button
        ref={triggerRef as Ref<HTMLButtonElement>}
        type="button"
        data-stratum="menu-item"
        data-submenu-trigger=""
        data-indicator={icon ? '' : undefined}
        data-open={isOpen || undefined}
        data-active={isActiveRow || undefined}
        data-focus-inside={hasFocusInside || undefined}
        aria-disabled={disabled || undefined}
        tabIndex={isActiveRow ? 0 : -1}
        className={clsx('stratum-menu__item', 'stratum-focus-inset', triggerClassName)}
        {...referenceProps}
      >
        <span className="stratum-menu__indicator" aria-hidden="true">
          {icon}
        </span>
        <span className="stratum-menu__item-label">{label}</span>
        <span className="stratum-menu__chevron" aria-hidden="true">
          <ChevronRightIcon />
        </span>
      </button>
    );
  } else if (trigger) {
    triggerNode = cloneElement(trigger as ReactElement<Record<string, unknown>>, {
      ...referenceProps,
      ref: triggerRef,
    });
  } else {
    triggerNode = null;
  }

  return (
    <FloatingNode id={nodeId}>
      {triggerNode}
      <MenuContext.Provider value={menuContextValue}>
        <FloatingList elementsRef={elementsRef} labelsRef={labelsRef}>
          {presence.isPresent && (
            /* `preserveTabOrder` renders two tabbable, `aria-hidden` guard
             * spans at this position in the React tree. For a submenu that
             * position is *inside* the parent's `role="menu"`, which both
             * breaks `aria-required-children` and puts a hidden tab stop
             * between two menu items. A menu is a single tab stop navigated by
             * arrow keys, so the feature buys nothing here anyway. */
            <FloatingPortal preserveTabOrder={false}>
              <FloatingFocusManager
                context={context}
                modal={false}
                initialFocus={isNested ? -1 : 0}
                returnFocus={!isNested}
              >
                <div
                  // `rest` goes THROUGH the getter rather than alongside it:
                  // useListNavigation/useTypeahead/useDismiss all return
                  // onKeyDown/onKeyUp/onPointerMove on the floating element, so
                  // a sibling `{...rest}` spread is overwritten key for key and
                  // the consumer's handlers never fire. Floating UI chains the
                  // handlers it is given as user props.
                  {...getFloatingProps({
                    ...(rest as HTMLProps<HTMLElement>),
                    onKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
                      // See the matching note in Combobox: Floating UI may have
                      // already prevented the default for a navigation key, so
                      // only a *new* prevention by the consumer stops us.
                      const preventedUpstream = event.defaultPrevented;
                      onKeyDown?.(event as ReactKeyboardEvent<HTMLDivElement>);
                      if (event.key !== 'Tab') return;
                      if (event.defaultPrevented && !preventedUpstream) return;
                      // A menu is one tab stop. Left to the browser, Tab walks
                      // into the focus-guard spans and the menu is still open
                      // with focus nowhere useful. Closing the whole stack and
                      // letting the focus manager restore the trigger is the
                      // only outcome that can never strand a keyboard user;
                      // the next Tab then continues from the trigger.
                      event.preventDefault();
                      menuContextValue.closeAll();
                    },
                  })}
                  ref={surfaceRef}
                  data-stratum="menu"
                  data-state={presence.state}
                  data-side={side}
                  data-align={align}
                  data-nested={isNested || undefined}
                  className={clsx('stratum-menu', className)}
                  // Merged rather than assigned, so a consumer's `style` is not
                  // wiped out by the positioning styles.
                  style={surfaceStyle}
                >
                  {children}
                </div>
              </FloatingFocusManager>
            </FloatingPortal>
          )}
        </FloatingList>
      </MenuContext.Provider>
    </FloatingNode>
  );
});

/* =========================================================================
 * MenuItem
 * ======================================================================= */

export interface MenuItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onSelect' | 'type'> {
  /** Leading adornment. Hidden from assistive tech; the label carries meaning. */
  icon?: ReactNode;
  /** Trailing hint such as `Ctrl+R`. Presentational — it binds nothing. */
  shortcut?: string;
  /** Destructive styling. Paired with an icon slot so colour is never the only cue. */
  danger?: boolean;
  disabled?: boolean;
  /**
   * Whether activating this item collapses the menu. Default `true`.
   *
   * Set it to `false` for an item that opens another overlay. Closing the menu
   * synchronously unmounts its portal and hands focus back to the trigger in
   * the same task the dialog is mounting in, and the two focus managers then
   * race — the dialog wins on one render and loses on the next. Keeping the
   * menu mounted removes the race entirely; close it yourself (via the
   * controlled `open` prop) once the dialog has settled.
   */
  closeOnSelect?: boolean;
  /**
   * Fired on activation by pointer, `Enter` or `Space`. Call
   * `event.preventDefault()` to keep the menu open for this activation only.
   */
  onSelect?: (event: ReactMouseEvent<HTMLButtonElement>) => void;
  /**
   * Type-ahead string. Defaults to the item's text content, which is usually
   * right; set it when `children` is not plain text or when a shortcut hint
   * would pollute the match.
   */
  textValue?: string;
}

export const MenuItem = forwardRef<HTMLButtonElement, MenuItemProps>(function MenuItem(
  {
    icon,
    shortcut,
    danger = false,
    disabled = false,
    closeOnSelect = true,
    onSelect,
    textValue,
    className,
    children,
    onClick,
    onFocus,
    ...rest
  },
  forwardedRef,
) {
  const menu = useContext(MenuContext);
  const typeaheadLabel = disabled
    ? null
    : (textValue ?? (typeof children === 'string' ? children : undefined));
  const item = useListItem({ label: typeaheadLabel });
  const ref = useMergeRefs([item.ref, forwardedRef]);
  const isActive = item.index === menu.activeIndex && item.index !== -1;

  return (
    <button
      ref={ref}
      type="button"
      role="menuitem"
      data-stratum="menu-item"
      data-active={isActive || undefined}
      data-danger={danger || undefined}
      data-indicator={icon ? '' : undefined}
      aria-disabled={disabled || undefined}
      // The visible hint is aria-hidden so it is not read as part of the label,
      // which would otherwise leave the shortcut with no announcement channel
      // at all. Declared before the spread so a consumer can still override it.
      aria-keyshortcuts={shortcut}
      tabIndex={isActive ? 0 : -1}
      className={clsx('stratum-menu__item', 'stratum-focus-inset', className)}
      {...menu.getItemProps({
        ...rest,
        onClick(event: ReactMouseEvent<HTMLButtonElement>) {
          if (disabled) {
            event.preventDefault();
            return;
          }
          onClick?.(event);
          onSelect?.(event);
          if (closeOnSelect && !event.defaultPrevented) {
            menu.closeAll();
          }
        },
        onFocus(event: ReactFocusEvent<HTMLButtonElement>) {
          onFocus?.(event);
          menu.setHasFocusInside(true);
        },
      })}
    >
      <span className="stratum-menu__indicator" aria-hidden="true">
        {icon}
      </span>
      <span className="stratum-menu__item-label">{children}</span>
      {shortcut && (
        <span className="stratum-menu__shortcut" aria-hidden="true">
          {shortcut}
        </span>
      )}
    </button>
  );
});

/* =========================================================================
 * MenuCheckboxItem
 * ======================================================================= */

export interface MenuCheckboxItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onSelect' | 'type' | 'defaultChecked'> {
  checked?: boolean;
  defaultChecked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
  disabled?: boolean;
  /**
   * Default `false`, unlike `MenuItem`. A checkbox row is a setting rather
   * than a command, and closing the menu after every toggle makes a group of
   * related switches unusable.
   */
  closeOnSelect?: boolean;
  textValue?: string;
}

export const MenuCheckboxItem = forwardRef<HTMLButtonElement, MenuCheckboxItemProps>(
  function MenuCheckboxItem(
    {
      checked: checkedProp,
      defaultChecked = false,
      onCheckedChange,
      disabled = false,
      closeOnSelect = false,
      textValue,
      className,
      children,
      onClick,
      onFocus,
      ...rest
    },
    forwardedRef,
  ) {
    const menu = useContext(MenuContext);
    const [checked, setChecked] = useControllableState<boolean>({
      value: checkedProp,
      defaultValue: defaultChecked,
      onChange: onCheckedChange,
    });
    const typeaheadLabel = disabled
      ? null
      : (textValue ?? (typeof children === 'string' ? children : undefined));
    const item = useListItem({ label: typeaheadLabel });
    const ref = useMergeRefs([item.ref, forwardedRef]);
    const isActive = item.index === menu.activeIndex && item.index !== -1;

    return (
      <button
        ref={ref}
        type="button"
        role="menuitemcheckbox"
        data-stratum="menu-checkbox-item"
        data-active={isActive || undefined}
        data-indicator=""
        aria-checked={checked}
        aria-disabled={disabled || undefined}
        tabIndex={isActive ? 0 : -1}
        className={clsx('stratum-menu__item', 'stratum-focus-inset', className)}
        {...menu.getItemProps({
          ...rest,
          onClick(event: ReactMouseEvent<HTMLButtonElement>) {
            if (disabled) {
              event.preventDefault();
              return;
            }
            setChecked(!checked);
            onClick?.(event);
            if (closeOnSelect && !event.defaultPrevented) {
              menu.closeAll();
            }
          },
          onFocus(event: ReactFocusEvent<HTMLButtonElement>) {
            onFocus?.(event);
            menu.setHasFocusInside(true);
          },
        })}
      >
        <span className="stratum-menu__indicator" aria-hidden="true">
          {checked && <CheckIcon />}
        </span>
        <span className="stratum-menu__item-label">{children}</span>
      </button>
    );
  },
);

/* =========================================================================
 * MenuRadioGroup / MenuRadioItem
 * ======================================================================= */

export interface MenuRadioGroupProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue'> {
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  /** Accessible name for the group. Pair it with a visible `MenuLabel`. */
  label?: string;
}

export const MenuRadioGroup = forwardRef<HTMLDivElement, MenuRadioGroupProps>(
  function MenuRadioGroup(
    { value: valueProp, defaultValue = '', onValueChange, label, className, children, ...rest },
    forwardedRef,
  ) {
    const [value, setValue] = useControllableState<string>({
      value: valueProp,
      defaultValue,
      onChange: onValueChange,
    });

    const contextValue = useMemo<MenuRadioGroupContextValue>(
      () => ({ value, setValue }),
      [value, setValue],
    );

    return (
      <div
        {...rest}
        ref={forwardedRef}
        role="group"
        // Written after the spread, so a bare `aria-label={label}` would delete
        // a consumer's own name whenever `label` is omitted.
        aria-label={rest['aria-label'] ?? label}
        data-stratum="menu-radio-group"
        className={clsx('stratum-menu__group', className)}
      >
        <MenuRadioGroupContext.Provider value={contextValue}>
          {children}
        </MenuRadioGroupContext.Provider>
      </div>
    );
  },
);

export interface MenuRadioItemProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'onSelect' | 'type' | 'value'> {
  value: string;
  disabled?: boolean;
  /** Default `true`: choosing a value is a terminal decision. */
  closeOnSelect?: boolean;
  textValue?: string;
}

export const MenuRadioItem = forwardRef<HTMLButtonElement, MenuRadioItemProps>(
  function MenuRadioItem(
    {
      value,
      disabled = false,
      closeOnSelect = true,
      textValue,
      className,
      children,
      onClick,
      onFocus,
      ...rest
    },
    forwardedRef,
  ) {
    const menu = useContext(MenuContext);
    const group = useContext(MenuRadioGroupContext);

    if (import.meta.env?.DEV && !group) {
      console.error('[stratum] <MenuRadioItem> must be rendered inside a <MenuRadioGroup>.');
    }

    const typeaheadLabel = disabled
      ? null
      : (textValue ?? (typeof children === 'string' ? children : undefined));
    const item = useListItem({ label: typeaheadLabel });
    const ref = useMergeRefs([item.ref, forwardedRef]);
    const isActive = item.index === menu.activeIndex && item.index !== -1;
    const checked = group?.value === value;

    return (
      <button
        ref={ref}
        type="button"
        role="menuitemradio"
        data-stratum="menu-radio-item"
        data-active={isActive || undefined}
        data-indicator=""
        aria-checked={checked}
        aria-disabled={disabled || undefined}
        tabIndex={isActive ? 0 : -1}
        className={clsx('stratum-menu__item', 'stratum-focus-inset', className)}
        {...menu.getItemProps({
          ...rest,
          onClick(event: ReactMouseEvent<HTMLButtonElement>) {
            if (disabled) {
              event.preventDefault();
              return;
            }
            group?.setValue(value);
            onClick?.(event);
            if (closeOnSelect && !event.defaultPrevented) {
              menu.closeAll();
            }
          },
          onFocus(event: ReactFocusEvent<HTMLButtonElement>) {
            onFocus?.(event);
            menu.setHasFocusInside(true);
          },
        })}
      >
        <span className="stratum-menu__indicator" aria-hidden="true">
          {checked && <DotIcon />}
        </span>
        <span className="stratum-menu__item-label">{children}</span>
      </button>
    );
  },
);

/* =========================================================================
 * MenuSeparator / MenuLabel
 * ======================================================================= */

export type MenuSeparatorProps = HTMLAttributes<HTMLDivElement>;

export const MenuSeparator = forwardRef<HTMLDivElement, MenuSeparatorProps>(
  function MenuSeparator({ className, ...rest }, forwardedRef) {
    return (
      <div
        {...rest}
        ref={forwardedRef}
        role="separator"
        aria-orientation="horizontal"
        data-stratum="menu-separator"
        className={clsx('stratum-menu__separator', className)}
      />
    );
  },
);

export type MenuLabelProps = HTMLAttributes<HTMLDivElement>;

/**
 * A visual section heading inside a menu. Never focusable, never a stop for
 * arrow keys.
 *
 * It is marked `role="presentation"` deliberately. `role="menu"` permits only
 * `menuitem`, `menuitemcheckbox`, `menuitemradio`, `group` and `separator`
 * children, so a bare `<div>` here makes the whole menu fail
 * `aria-required-children` — and screen readers do not reliably announce a
 * loose text node inside a menu anyway, so nothing is actually lost. To give a
 * section a name assistive tech will report, put its items in a
 * `MenuRadioGroup` with `label`, or wrap them yourself in an element with
 * `role="group"` and `aria-label`.
 */
export const MenuLabel = forwardRef<HTMLDivElement, MenuLabelProps>(function MenuLabel(
  { className, children, ...rest },
  forwardedRef,
) {
  return (
    <div
      {...rest}
      ref={forwardedRef}
      role="presentation"
      data-stratum="menu-label"
      className={clsx('stratum-menu__section-label', className)}
    >
      {children}
    </div>
  );
});

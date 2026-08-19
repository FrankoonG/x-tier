import {
  createContext,
  forwardRef,
  useCallback,
  useContext,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import './Tabs.css';

export type TabsVariant = 'underline' | 'pill' | 'enclosed';
export type TabsSize = 'sm' | 'md' | 'lg';
export type TabsOrientation = 'horizontal' | 'vertical';

/**
 * `automatic` selects a tab as soon as it receives focus — correct when panels
 * are cheap. `manual` requires Enter/Space, which is the APG recommendation
 * whenever revealing a panel costs a request, because arrowing across five
 * tabs would otherwise fire five loads.
 */
export type TabsActivationMode = 'automatic' | 'manual';

interface TabsContextValue {
  value: string;
  /**
   * Which tab holds the group's single tab stop. Normally `value`, but when a
   * controlled `value` matches no rendered tab it falls back to the first, so
   * the list never ends up with zero tab stops (SC 2.1.1).
   */
  focusValue: string;
  select: (value: string) => void;
  orientation: TabsOrientation;
  activationMode: TabsActivationMode;
  variant: TabsVariant;
  size: TabsSize;
  fitted: boolean;
  /** +1 moving forward through the list, -1 backward, 0 on first paint. */
  direction: number;
  keepMounted: boolean;
  publishOrder: (values: string[]) => void;
  tabId: (value: string) => string;
  panelId: (value: string) => string;
}

const TabsContext = createContext<TabsContextValue | null>(null);

export function useTabsContext(component: string): TabsContextValue {
  const context = useContext(TabsContext);
  if (!context) {
    throw new Error(`[stratum] <${component}> must be rendered inside <Tabs>.`);
  }
  return context;
}

/** Values become part of DOM ids, so anything unsafe there is folded to `_`. */
const idSafe = (value: string) => value.replace(/[^A-Za-z0-9_-]/g, '_');

/* ========================================================================== */
/* Tabs                                                                        */
/* ========================================================================== */

export interface TabsProps extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue'> {
  /** Controlled active tab value. */
  value?: string;
  /** Initial active tab for the uncontrolled case. Defaults to the first tab. */
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  orientation?: TabsOrientation;
  variant?: TabsVariant;
  size?: TabsSize;
  activationMode?: TabsActivationMode;
  /** Tabs share the list width equally instead of hugging their labels. */
  fitted?: boolean;
  /** Keep every panel mounted and hide the inactive ones. */
  keepMounted?: boolean;
  children?: ReactNode;
}

/**
 * Tabbed navigation.
 *
 * THE SLIDING INDICATOR
 * ---------------------
 * The active-tab marker is a single pseudo-element on the tab list, moved by
 * two custom properties (`--_indicator-pos`, `--_indicator-size`) that a layout
 * effect writes from the active tab's `offsetLeft`/`offsetWidth`. It is
 * deliberately not a per-tab element that fades in and out, and deliberately
 * not a layout animation: one element that transitions `translate` and `width`
 * composites cleanly and cannot desynchronise from the tab it is marking.
 *
 * The physical `left: 0` + `translate` pairing (rather than
 * `inset-inline-start`) is intentional — `offsetLeft` is a physical measurement
 * and stays physical in RTL, so pairing it with a logical property would put
 * the indicator on the wrong side of a mirrored list.
 *
 * DIRECTION
 * ---------
 * The slide direction handed to `TabPanel` is derived during render using
 * React's documented "adjust state when a prop changes" pattern — never by
 * mutating a ref during render, which double-counts under concurrent React's
 * double-invoke and was the specific defect in hy2scale's TabPanel.
 */
export const Tabs = forwardRef<HTMLDivElement, TabsProps>(function Tabs(
  {
    value: valueProp,
    defaultValue,
    onValueChange,
    orientation = 'horizontal',
    variant = 'underline',
    size = 'md',
    activationMode = 'automatic',
    fitted = false,
    keepMounted = false,
    id,
    className,
    children,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const baseId = id ?? `stratum-tabs-${reactId}`;

  const [value, setValue] = useControllableState<string>({
    value: valueProp,
    defaultValue: defaultValue ?? '',
    onChange: onValueChange,
  });

  // DOM order of the rendered tabs, published by TabList after every commit.
  const [order, setOrder] = useState<string[]>([]);

  // Derived-during-render direction. React re-renders this component
  // immediately and throws away the first pass, so the panel that mounts in
  // this same commit already sees the correct value — which is exactly what a
  // post-commit effect could not guarantee.
  const [previousValue, setPreviousValue] = useState(value);
  const [direction, setDirection] = useState(0);
  if (previousValue !== value) {
    const from = order.indexOf(previousValue);
    const to = order.indexOf(value);
    setPreviousValue(value);
    setDirection(from === -1 || to === -1 || from === to ? 0 : to > from ? 1 : -1);
  }

  const publishOrder = useCallback((next: string[]) => {
    setOrder((prev) =>
      prev.length === next.length && prev.every((item, index) => item === next[index]) ? prev : next,
    );
  }, []);

  // Fall back to the first tab when nothing is selected, or when the selected
  // tab has been removed. Only in uncontrolled mode: silently overriding a
  // controlled value would fight the consumer's state.
  useLayoutEffect(() => {
    if (valueProp !== undefined) return;
    if (value !== '' && order.includes(value)) return;
    const first = order[0];
    if (first !== undefined && first !== value) setValue(first);
  }, [order, value, valueProp, setValue]);

  // The uncontrolled path self-heals through the layout effect above, but the
  // controlled path deliberately does not — so a controlled value naming a tab
  // that is not rendered would leave every tab at `tabIndex={-1}` and make the
  // list unreachable by keyboard. The tab stop is therefore derived separately
  // from selection: nothing reports as selected, but Tab still lands here.
  const focusValue = order.length === 0 || order.includes(value) ? value : (order[0] ?? value);

  const context = useMemo<TabsContextValue>(
    () => ({
      value,
      focusValue,
      select: setValue,
      orientation,
      activationMode,
      variant,
      size,
      fitted,
      direction,
      keepMounted,
      publishOrder,
      tabId: (tabValue: string) => `${baseId}-tab-${idSafe(tabValue)}`,
      panelId: (tabValue: string) => `${baseId}-panel-${idSafe(tabValue)}`,
    }),
    [
      value,
      focusValue,
      setValue,
      orientation,
      activationMode,
      variant,
      size,
      fitted,
      direction,
      keepMounted,
      publishOrder,
      baseId,
    ],
  );

  return (
    <TabsContext.Provider value={context}>
      <div
        {...rest}
        ref={ref}
        id={id}
        data-stratum="tabs"
        data-orientation={orientation}
        data-variant={variant}
        data-size={size}
        className={clsx('stratum-tabs', className)}
      >
        {children}
      </div>
    </TabsContext.Provider>
  );
});

/* ========================================================================== */
/* TabList                                                                     */
/* ========================================================================== */

export interface TabListProps extends HTMLAttributes<HTMLDivElement> {
  children?: ReactNode;
}

export const TabList = forwardRef<HTMLDivElement, TabListProps>(function TabList(
  { className, children, onKeyDown, ...rest },
  ref,
) {
  const ctx = useTabsContext('TabList');
  const listRef = useRef<HTMLDivElement | null>(null);

  const setRefs = useCallback(
    (node: HTMLDivElement | null) => {
      listRef.current = node;
      if (typeof ref === 'function') ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  const ownTabs = useCallback((list: HTMLElement) => {
    // `closest` filters out tabs belonging to a nested <Tabs>.
    return Array.from(list.querySelectorAll<HTMLElement>('[data-stratum="tab"]')).filter(
      (element) => element.closest('[data-stratum="tab-list"]') === list,
    );
  }, []);

  // Runs after every commit on purpose: publishing DOM order and re-measuring
  // are both cheap for a handful of tabs, and any dependency list precise
  // enough to be safe here would have to include `children`, which changes
  // identity every render anyway.
  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;

    ctx.publishOrder(
      ownTabs(list)
        .map((element) => element.dataset['value'])
        .filter((item): item is string => typeof item === 'string'),
    );

    const measure = () => {
      const active = ownTabs(list).find((element) => element.dataset['active'] === 'true');
      if (!active) {
        list.removeAttribute('data-indicator');
        return;
      }
      const vertical = ctx.orientation === 'vertical';
      const position = vertical ? active.offsetTop : active.offsetLeft;
      const extent = vertical ? active.offsetHeight : active.offsetWidth;
      list.style.setProperty('--_indicator-pos', `${position}px`);
      list.style.setProperty('--_indicator-size', `${extent}px`);
      if (!list.hasAttribute('data-indicator')) list.setAttribute('data-indicator', 'measured');
    };

    measure();

    // The latch is the attribute itself, not a one-shot ref. A ref set by
    // whichever rAF happened to fire first would promote a list that had no
    // active tab to measure (children arriving async, a controlled value not
    // yet in the list) — the marker would then animate in from x=0 at zero
    // width, exactly the artefact the delay exists to prevent — and, once
    // `measure` had removed the attribute mid-life, nothing would ever
    // re-schedule the promotion, so the indicator would jump rather than
    // slide for the rest of the component's life.
    let raf = 0;
    if (list.getAttribute('data-indicator') === 'measured') {
      raf = requestAnimationFrame(() => {
        const current = listRef.current;
        if (current?.getAttribute('data-indicator') === 'measured') {
          current.setAttribute('data-indicator', 'ready');
        }
      });
    }

    if (typeof ResizeObserver === 'undefined') {
      return () => cancelAnimationFrame(raf);
    }

    // The list alone is not enough: a badge count changing inside the active
    // tab resizes the tab without resizing a width-constrained list.
    const observer = new ResizeObserver(measure);
    observer.observe(list);
    const active = ownTabs(list).find((element) => element.dataset['active'] === 'true');
    if (active) observer.observe(active);

    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
    };
  });

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented) return;

    const list = listRef.current;
    const target = event.target as HTMLElement | null;
    if (!list || !target || target.dataset['stratum'] !== 'tab') return;

    const tabs = ownTabs(list);
    const current = tabs.indexOf(target);
    if (current === -1 || tabs.length === 0) return;

    const vertical = ctx.orientation === 'vertical';
    const forwardKey = vertical ? 'ArrowDown' : 'ArrowRight';
    const backwardKey = vertical ? 'ArrowUp' : 'ArrowLeft';

    let next: number;
    switch (event.key) {
      case forwardKey:
        next = (current + 1) % tabs.length;
        break;
      case backwardKey:
        next = (current - 1 + tabs.length) % tabs.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = tabs.length - 1;
        break;
      default:
        return;
    }

    const element = tabs[next];
    if (!element) return;
    event.preventDefault();
    element.focus();
    // Instant, not smooth: a keyboard user arrowing quickly through an
    // overflowing list must not be chasing a scroll animation.
    element.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  };

  return (
    <div
      {...rest}
      ref={setRefs}
      role="tablist"
      aria-orientation={ctx.orientation}
      data-stratum="tab-list"
      data-orientation={ctx.orientation}
      data-variant={ctx.variant}
      data-size={ctx.size}
      data-fitted={ctx.fitted || undefined}
      className={clsx('stratum-tab-list', className)}
      onKeyDown={handleKeyDown}
    >
      {children}
    </div>
  );
});

/* ========================================================================== */
/* Tab                                                                         */
/* ========================================================================== */

export interface TabProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'value'> {
  /** Ties this tab to the `TabPanel` with the same value. */
  value: string;
  /** Leading adornment. Decorative — the label carries the meaning. */
  icon?: ReactNode;
  /** Trailing adornment, typically a count. */
  badge?: ReactNode;
  disabled?: boolean;
  children?: ReactNode;
}

/**
 * A disabled tab keeps `tabindex` and reports `aria-disabled` rather than using
 * the `disabled` attribute. APG recommends this for composite widgets: a tab
 * removed from arrow-key rotation is a tab a screen reader user cannot discover
 * exists, and "this section is unavailable" is information worth having.
 */
export const Tab = forwardRef<HTMLButtonElement, TabProps>(function Tab(
  { value, icon, badge, disabled = false, className, children, onClick, onFocus, ...rest },
  ref,
) {
  const ctx = useTabsContext('Tab');
  const isActive = ctx.value === value;

  // The icon wrapper is aria-hidden and the label span is skipped without
  // children, so an icon-only tab would reach assistive tech unnamed.
  if (
    import.meta.env?.DEV &&
    (children == null || children === false) &&
    icon != null &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      `[stratum] <Tab value="${value}"> renders icon-only and needs \`aria-label\` or \`aria-labelledby\`. A \`title\` attribute is not a reliable substitute.`,
    );
  }

  return (
    <button
      {...rest}
      ref={ref}
      type="button"
      role="tab"
      id={ctx.tabId(value)}
      data-stratum="tab"
      data-value={value}
      data-active={isActive || undefined}
      data-disabled={disabled || undefined}
      data-variant={ctx.variant}
      data-size={ctx.size}
      data-orientation={ctx.orientation}
      aria-selected={isActive}
      aria-controls={ctx.panelId(value)}
      aria-disabled={disabled || rest['aria-disabled']}
      tabIndex={ctx.focusValue === value ? 0 : -1}
      className={clsx('stratum-tab', 'stratum-focus-inset', className)}
      onClick={(event) => {
        if (disabled) {
          event.preventDefault();
          return;
        }
        onClick?.(event);
        if (!event.defaultPrevented) ctx.select(value);
      }}
      onFocus={(event) => {
        onFocus?.(event);
        if (ctx.activationMode === 'automatic' && !disabled) ctx.select(value);
      }}
    >
      {icon && (
        <span className="stratum-tab__icon" aria-hidden="true">
          {icon}
        </span>
      )}
      {children != null && children !== false && (
        <span className="stratum-tab__label">{children}</span>
      )}
      {badge != null && badge !== false && <span className="stratum-tab__badge">{badge}</span>}
    </button>
  );
});

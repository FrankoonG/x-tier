import {
  Fragment,
  forwardRef,
  useRef,
  useState,
  type HTMLAttributes,
  type MouseEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import {
  FloatingFocusManager,
  FloatingPortal,
  autoUpdate,
  flip,
  offset,
  shift,
  size as floatingSize,
  useClick,
  useDismiss,
  useFloating,
  useInteractions,
  useListNavigation,
  useMergeRefs,
  useRole,
} from '@floating-ui/react';
import { usePresence } from '../../hooks/usePresence';
import './Breadcrumb.css';

export type BreadcrumbSize = 'sm' | 'md';

export interface BreadcrumbLinkRenderProps extends HTMLAttributes<HTMLElement> {
  className: string;
  children: ReactNode;
  'aria-current'?: 'page' | undefined;
  onClick?: ((event: MouseEvent<HTMLElement>) => void) | undefined;
  /**
   * Supplied only for crumbs rendered inside the overflow menu. Attach it to
   * the element you render — the menu's arrow-key navigation reads the DOM
   * node through it, and a crumb that drops the ref is silently skipped.
   */
  ref?: ((node: HTMLElement | null) => void) | undefined;
}

export interface BreadcrumbItem {
  /** Stable identity. Used as the React key. */
  id: string;
  label: ReactNode;
  href?: string;
  /** Leading adornment. Decorative. */
  icon?: ReactNode;
  onClick?: (event: MouseEvent<HTMLElement>) => void;
  /**
   * Renders the crumb yourself — the escape hatch for router links
   * (`<Link>`, `<NavLink>`) that must not be plain anchors. Used for the
   * visible trail and inside the overflow menu alike; in the menu the props
   * additionally carry `ref` and `role`, which must both be forwarded or the
   * crumb drops out of arrow-key navigation.
   */
  render?: (props: BreadcrumbLinkRenderProps) => ReactNode;
  /** Accessible name, when `label` is not plain text. */
  ariaLabel?: string;
}

export interface BreadcrumbProps extends Omit<HTMLAttributes<HTMLElement>, 'onSelect'> {
  items: BreadcrumbItem[];
  /**
   * Collapse to an overflow menu once the trail is longer than this. Omit to
   * never collapse.
   */
  maxItems?: number;
  /** Crumbs kept before the overflow menu. */
  itemsBeforeCollapse?: number;
  /** Crumbs kept after the overflow menu, including the current page. */
  itemsAfterCollapse?: number;
  separator?: ReactNode;
  size?: BreadcrumbSize;
  /** Fires for every crumb activation, after the item's own `onClick`. */
  onItemClick?: (item: BreadcrumbItem, index: number, event: MouseEvent<HTMLElement>) => void;
  /** `aria-label` for the surrounding nav landmark. */
  label?: string;
  /** Accessible name of the overflow trigger. */
  labelExpand?: string;
}

/* `fill` is set per shape, not on the <svg>: the reset layer declares
 * `svg { fill: currentColor }`, which beats a presentational root attribute. */
const ChevronSeparator = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <path
      d="m6.5 3.5 4 4.5-4 4.5"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

const EllipsisIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <circle cx="3.5" cy="8" r="1.35" fill="currentColor" />
    <circle cx="8" cy="8" r="1.35" fill="currentColor" />
    <circle cx="12.5" cy="8" r="1.35" fill="currentColor" />
  </svg>
);

/* ========================================================================== */
/* Overflow menu                                                               */
/* ========================================================================== */

interface OverflowMenuProps {
  items: BreadcrumbItem[];
  /** Index of each hidden item in the original array, for `onItemClick`. */
  offsets: number[];
  label: string;
  onItemClick?: BreadcrumbProps['onItemClick'];
}

/**
 * Positioning, dismissal, roving focus and focus return are all delegated to
 * `@floating-ui/react`. Hand-rolling any of them is where breadcrumb overflow
 * menus normally go wrong: the trigger sits at the top of a scrolling page, so
 * the menu has to re-anchor on scroll and flip near the viewport edge, and both
 * are exactly what `autoUpdate` + `flip` already handle correctly.
 */
function OverflowMenu({ items, offsets, label, onItemClick }: OverflowMenuProps) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const listRef = useRef<Array<HTMLElement | null>>([]);

  const { refs, floatingStyles, context } = useFloating({
    open,
    onOpenChange: setOpen,
    placement: 'bottom-start',
    // These two options are not incidental, and this was the one floating
    // surface in the framework that was missing them.
    //
    // Floating UI defaults to `transform: true`, which means `floatingStyles`
    // carries the position as `transform: translate(x, y)` — on the very same
    // element whose stylesheet rule transitions `transform`. The first render
    // happens before the position is known, so the inline value is
    // `translate(0px, 0px)`; when the real coordinates arrive the transition
    // has something to interpolate and the menu SLIDES IN FROM THE TOP-LEFT
    // CORNER of its offset parent instead of appearing at the trigger. It only
    // does it once, because the hook keeps the last coordinates for the next
    // open — which is exactly what made the bug look intermittent.
    //
    // `transform: false` positions with top/left, which nothing transitions, so
    // `transform` is left free for the entrance animation to own. Every other
    // overlay here already did this; this one drifted.
    //
    // `fixed` comes along for the ride, and matters just as much: the absolute
    // strategy resolves against the nearest positioned ancestor, so a menu
    // inside a transformed or `contain`ed panel — routine in an app shell —
    // lands in the wrong place entirely.
    strategy: 'fixed',
    transform: false,
    middleware: [
      offset(6),
      flip({ padding: 8 }),
      shift({ padding: 8 }),
      floatingSize({
        padding: 8,
        apply({ availableHeight, elements }) {
          elements.floating.style.setProperty('--_max-h', `${Math.max(availableHeight, 96)}px`);
        },
      }),
    ],
    whileElementsMounted: autoUpdate,
  });

  const click = useClick(context);
  const dismiss = useDismiss(context);
  const role = useRole(context, { role: 'menu' });
  const listNavigation = useListNavigation(context, {
    listRef,
    activeIndex,
    onNavigate: setActiveIndex,
    loop: true,
    focusItemOnOpen: 'auto',
  });

  const { getReferenceProps, getFloatingProps, getItemProps } = useInteractions([
    click,
    dismiss,
    role,
    listNavigation,
  ]);

  // Keeps the menu mounted through its exit transition. usePresence reads the
  // real running animations, so the duration stays in CSS.
  const presence = usePresence(open);
  const floatingRef = useMergeRefs([refs.setFloating, presence.ref]);

  return (
    <>
      <button
        ref={refs.setReference}
        type="button"
        data-stratum="breadcrumb-overflow"
        aria-label={label}
        className="stratum-breadcrumb__overflow"
        {...getReferenceProps()}
      >
        <EllipsisIcon />
      </button>

      {presence.isPresent && (
        <FloatingPortal>
          <FloatingFocusManager context={context} modal={false}>
            <div
              ref={floatingRef}
              style={floatingStyles}
              data-stratum="breadcrumb-menu"
              data-state={presence.state}
              className="stratum-breadcrumb__menu"
              {...getFloatingProps()}
            >
              {items.map((item, index) => {
                const itemProps = getItemProps({
                  onClick(event: MouseEvent<HTMLElement>) {
                    item.onClick?.(event);
                    onItemClick?.(item, offsets[index] ?? index, event);
                    setOpen(false);
                  },
                });
                const content = (
                  <>
                    {item.icon && (
                      <span className="stratum-breadcrumb__icon" aria-hidden="true">
                        {item.icon}
                      </span>
                    )}
                    <span className="stratum-breadcrumb__label">{item.label}</span>
                  </>
                );
                const shared = {
                  ref: (node: HTMLElement | null) => {
                    listRef.current[index] = node;
                  },
                  role: 'menuitem',
                  className: 'stratum-breadcrumb__menu-item',
                  ...(item.ariaLabel ? { 'aria-label': item.ariaLabel } : {}),
                  ...itemProps,
                };

                // `render` is checked first, exactly as in the visible trail.
                // Falling through to a plain <a href> here would turn a router
                // link into a full-page navigation the moment the crumb is
                // collapsed, and a render-only crumb with no `href` would
                // become an inert <button>.
                if (item.render) {
                  return (
                    <Fragment key={item.id}>
                      {item.render({ ...shared, children: content })}
                    </Fragment>
                  );
                }

                return item.href ? (
                  <a key={item.id} {...shared} href={item.href}>
                    {content}
                  </a>
                ) : (
                  <button key={item.id} {...shared} type="button">
                    {content}
                  </button>
                );
              })}
            </div>
          </FloatingFocusManager>
        </FloatingPortal>
      )}
    </>
  );
}

/* ========================================================================== */
/* Breadcrumb                                                                  */
/* ========================================================================== */

/**
 * A hierarchical trail.
 *
 * The last crumb is `aria-current="page"` and is rendered as text, not a link:
 * a link to the page you are already on is a well-known screen-reader
 * annoyance and gives a keyboard user a tab stop that does nothing.
 *
 * The separator lives inside its crumb's `<li>` rather than in an `<li>` of its
 * own. An extra list item per separator would double the announced item count —
 * "list, 7 items" for a four-level trail.
 */
export const Breadcrumb = forwardRef<HTMLElement, BreadcrumbProps>(function Breadcrumb(
  {
    items,
    maxItems,
    itemsBeforeCollapse = 1,
    itemsAfterCollapse = 2,
    separator,
    size = 'md',
    onItemClick,
    label = 'Breadcrumb',
    labelExpand = 'Show hidden breadcrumbs',
    className,
    ...rest
  },
  ref,
) {
  const total = items.length;
  const sep = separator ?? <ChevronSeparator />;

  let head: BreadcrumbItem[] = items;
  let hidden: BreadcrumbItem[] = [];
  let hiddenOffsets: number[] = [];
  let tail: BreadcrumbItem[] = [];
  let tailOffset = 0;

  if (maxItems !== undefined && maxItems > 0 && total > maxItems) {
    const before = Math.max(0, Math.min(itemsBeforeCollapse, total - 1));
    const after = Math.max(1, Math.min(itemsAfterCollapse, total - before - 1));
    const hiddenCount = total - before - after;
    // Hiding a single crumb behind a menu costs a click and saves nothing.
    if (hiddenCount > 1) {
      head = items.slice(0, before);
      hidden = items.slice(before, before + hiddenCount);
      hiddenOffsets = hidden.map((_, index) => before + index);
      tail = items.slice(before + hiddenCount);
      tailOffset = before + hiddenCount;
    }
  }

  const collapsed = hidden.length > 0;

  const renderCrumb = (item: BreadcrumbItem, index: number, isCurrent: boolean) => {
    const linkClass = clsx(
      'stratum-breadcrumb__link',
      isCurrent && 'stratum-breadcrumb__link--current',
    );
    const content = (
      <>
        {item.icon && (
          <span className="stratum-breadcrumb__icon" aria-hidden="true">
            {item.icon}
          </span>
        )}
        <span className="stratum-breadcrumb__label">{item.label}</span>
      </>
    );

    const handleClick = (event: MouseEvent<HTMLElement>) => {
      item.onClick?.(event);
      onItemClick?.(item, index, event);
    };

    if (item.render) {
      return item.render({
        className: linkClass,
        children: content,
        'aria-current': isCurrent ? 'page' : undefined,
        onClick: handleClick,
      });
    }

    if (isCurrent) {
      return (
        <span className={linkClass} aria-current="page" aria-label={item.ariaLabel}>
          {content}
        </span>
      );
    }

    if (item.href) {
      return (
        <a className={linkClass} href={item.href} aria-label={item.ariaLabel} onClick={handleClick}>
          {content}
        </a>
      );
    }

    if (item.onClick || onItemClick) {
      return (
        <button
          type="button"
          className={linkClass}
          aria-label={item.ariaLabel}
          onClick={handleClick}
        >
          {content}
        </button>
      );
    }

    return (
      <span className={clsx(linkClass, 'stratum-breadcrumb__link--static')} aria-label={item.ariaLabel}>
        {content}
      </span>
    );
  };

  const crumb = (item: BreadcrumbItem, index: number, position: number) => (
    <li key={item.id} className="stratum-breadcrumb__item">
      {position > 0 && (
        <span className="stratum-breadcrumb__separator" aria-hidden="true">
          {sep}
        </span>
      )}
      {renderCrumb(item, index, index === total - 1)}
    </li>
  );

  // Rendered positions are derived, never accumulated in a mutable counter —
  // a counter incremented inside JSX is a render-phase side effect.
  const overflowPosition = head.length;
  const tailStartPosition = overflowPosition + (collapsed ? 1 : 0);

  return (
    <nav
      // Before the spread: an attribute written after `...rest` wins in JSX,
      // so a consumer passing `aria-label` directly would silently get the
      // framework default instead.
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="breadcrumb"
      data-size={size}
      data-collapsed={collapsed || undefined}
      className={clsx('stratum-breadcrumb', className)}
    >
      <ol className="stratum-breadcrumb__list">
        {head.map((item, index) => crumb(item, index, index))}

        {collapsed && (
          <li key="stratum-overflow" className="stratum-breadcrumb__item">
            {overflowPosition > 0 && (
              <span className="stratum-breadcrumb__separator" aria-hidden="true">
                {sep}
              </span>
            )}
            <OverflowMenu
              items={hidden}
              offsets={hiddenOffsets}
              label={labelExpand}
              onItemClick={onItemClick}
            />
          </li>
        )}

        {tail.map((item, index) => crumb(item, tailOffset + index, tailStartPosition + index))}
      </ol>
    </nav>
  );
});

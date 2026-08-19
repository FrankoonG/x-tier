import {
  forwardRef,
  useEffect,
  useId,
  useState,
  type HTMLAttributes,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useMediaQuery } from '../../hooks/useMediaQuery';
import './AppShell.css';

export interface AppShellProps extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  /** Persistent primary navigation. Collapses to an overlay on narrow screens. */
  sidebar?: ReactNode;
  /** Fixed header row. */
  topbar?: ReactNode;
  children: ReactNode;
  /** Collapse the sidebar to icon width. Controlled. */
  sidebarCollapsed?: boolean;
  /** Open state of the mobile sidebar overlay. Controlled. */
  sidebarOpen?: boolean;
  onSidebarOpenChange?: (open: boolean) => void;
  /** Viewport width below which the sidebar becomes an overlay. Default 768. */
  mobileBreakpoint?: number;
  /** Accessible label for the skip link. Default `'Skip to content'`. */
  skipLinkLabel?: string;
  /** Accessible label for the scrim's close action. Default `'Close navigation'`. */
  closeNavigationLabel?: string;
  /**
   * Accessible name for the scrolling content region. It is a tab stop — see
   * the note on `<main>` below — so it needs a name of its own.
   * Default `'Main content'`.
   */
  contentLabel?: string;
}

/**
 * The application frame: sidebar, topbar and a scrolling content region.
 *
 * Layout notes:
 * - Uses a CSS grid with `min-height: 0` on the scrolling cell. Without that,
 *   a grid child refuses to shrink below its content and the page grows a
 *   second scrollbar instead of scrolling internally — the classic reason a
 *   dense table inside a shell scrolls the whole document.
 * - The content region is the scroll container, not `<body>`. Keeping the frame
 *   fixed means a sticky table header actually sticks to the frame.
 *
 * Accessibility:
 * - Ships a skip link as the first focusable element, satisfying WCAG 2.4.1.
 * - The mobile sidebar is a `role="dialog"` with `aria-modal`, closes on
 *   Escape, and restores focus to whatever opened it.
 * - The content region carries `tabIndex={0}`, not `-1`. It is the page's only
 *   scroll container — the shell itself is `overflow: hidden` — so under
 *   SC 2.1.1 a keyboard user has to be able to reach it to scroll it, and
 *   `-1` makes it programmatically focusable without making it reachable.
 *   `0` still satisfies the skip link (`.focus()` works on anything focusable)
 *   and costs one tab stop at the top of the content, which is the same trade
 *   ScrollArea already makes. That tab stop is named via `contentLabel`,
 *   because an unnamed one announces as nothing.
 */
export const AppShell = forwardRef<HTMLDivElement, AppShellProps>(function AppShell(
  {
    sidebar,
    topbar,
    children,
    sidebarCollapsed = false,
    sidebarOpen = false,
    onSidebarOpenChange,
    mobileBreakpoint = 768,
    skipLinkLabel = 'Skip to content',
    closeNavigationLabel = 'Close navigation',
    contentLabel = 'Main content',
    className,
    ...rest
  },
  ref,
) {
  const contentId = useId();
  const isMobile = useMediaQuery(`(max-width: ${mobileBreakpoint - 1}px)`);
  const [restoreTo, setRestoreTo] = useState<HTMLElement | null>(null);

  const overlayOpen = isMobile && sidebarOpen;

  // Remember what had focus when the overlay opened so it can be handed back.
  useEffect(() => {
    if (overlayOpen) {
      setRestoreTo(document.activeElement as HTMLElement | null);
    } else if (restoreTo) {
      restoreTo.focus?.();
      setRestoreTo(null);
    }
    // `restoreTo` is intentionally not a dependency: including it would re-run
    // on the state update this effect itself performs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [overlayOpen]);

  useEffect(() => {
    if (!overlayOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onSidebarOpenChange?.(false);
    };
    window.addEventListener('keydown', onKey);
    // Lock the underlying page while the overlay is up.
    const prev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = prev;
    };
  }, [overlayOpen, onSidebarOpenChange]);

  // Collapsing while the overlay is open would strand the user in an
  // icon-width dialog.
  const collapsed = sidebarCollapsed && !overlayOpen;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="app-shell"
      data-collapsed={collapsed || undefined}
      data-mobile={isMobile || undefined}
      data-overlay-open={overlayOpen || undefined}
      className={clsx('stratum-app-shell', className)}
    >
      <a className="stratum-app-shell__skip" href={`#${contentId}`}>
        {skipLinkLabel}
      </a>

      {sidebar && (
        <>
          <div
            className="stratum-app-shell__sidebar"
            {...(overlayOpen
              ? { role: 'dialog', 'aria-modal': true, 'aria-label': 'Navigation' }
              : {})}
            // Hidden from the a11y tree and from tab order when off-canvas, so
            // a keyboard user never tabs into an invisible drawer. React 19
            // maps a boolean `inert` to the attribute correctly; passing the
            // empty string (the raw HTML form) makes React treat it as false.
            inert={isMobile && !sidebarOpen}
          >
            {sidebar}
          </div>
          {overlayOpen && (
            <button
              type="button"
              className="stratum-app-shell__scrim"
              aria-label={closeNavigationLabel}
              onClick={() => onSidebarOpenChange?.(false)}
            />
          )}
        </>
      )}

      {topbar && <header className="stratum-app-shell__topbar">{topbar}</header>}

      <main
        id={contentId}
        className="stratum-app-shell__content stratum-focus-inset"
        // 0, not -1: this element is the scroll container, so it must be
        // keyboard-reachable to be scrollable (SC 2.1.1). See the note above.
        tabIndex={0}
        aria-label={contentLabel}
      >
        {children}
      </main>
    </div>
  );
});

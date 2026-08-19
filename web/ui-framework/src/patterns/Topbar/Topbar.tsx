import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Topbar.css';

export interface TopbarProps extends HTMLAttributes<HTMLDivElement> {
  /** Leading slot — menu toggle on narrow screens, current context, breadcrumb. */
  start?: ReactNode;
  /** Centre slot. Stays optically centred regardless of the side widths. */
  center?: ReactNode;
  /** Trailing slot — search, notifications, account, theme switch. */
  end?: ReactNode;
  children?: ReactNode;
}

/**
 * The application header row.
 *
 * The three slots use a `1fr auto 1fr` grid rather than `space-between`, so the
 * centre slot stays optically centred even when the two sides have very
 * different widths. With `space-between` a wide trailing group silently pushes
 * a centred search box off-centre, which reads as a layout bug.
 */
export const Topbar = forwardRef<HTMLDivElement, TopbarProps>(function Topbar(
  { start, center, end, children, className, ...rest },
  ref,
) {
  // A single-child usage should not pay for the three-column grid.
  const slotted = start !== undefined || center !== undefined || end !== undefined;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="topbar"
      data-slotted={slotted || undefined}
      className={clsx('stratum-topbar', className)}
    >
      {slotted ? (
        <>
          <div className="stratum-topbar__start">{start}</div>
          <div className="stratum-topbar__center">{center}</div>
          <div className="stratum-topbar__end">{end}</div>
        </>
      ) : (
        children
      )}
    </div>
  );
});

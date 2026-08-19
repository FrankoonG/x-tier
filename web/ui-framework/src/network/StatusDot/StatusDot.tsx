import { forwardRef, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import './StatusDot.css';

/**
 * The state a dot reports.
 *
 * `unknown` is NOT a failure and NOT a default. It means "we have no
 * observation" — unprobed, unreported, or irrelevant. `inactive` is the
 * opposite claim: we know the thing is not running. Collapsing the two is the
 * single most common way a panel ends up asserting something the data never
 * said, so they are separate statuses with separate shapes and separate
 * colours, and neither is ever inferred from the other.
 *
 * `pending` means an operation is in flight — a transitional state, not a
 * verdict.
 */
export type StatusDotStatus = 'ok' | 'degraded' | 'failed' | 'unknown' | 'inactive' | 'pending';

export type StatusDotSize = 'xs' | 'sm' | 'md' | 'lg';

export interface StatusDotProps extends Omit<HTMLAttributes<HTMLSpanElement>, 'children'> {
  /**
   * Observed state. Deliberately defaults to `unknown`: a dot with no status
   * is a dot with no observation, which is not the same as a healthy one.
   */
  status?: StatusDotStatus;
  size?: StatusDotSize;
  /**
   * Loops a soft halo behind the dot. Purely opt-in — a resting component never
   * animates, so this is for a live feed or an unacknowledged anomaly only.
   * Stops completely under `prefers-reduced-motion: reduce`.
   */
  pulse?: boolean;
  /**
   * Accessible name. Without one the dot is treated as decorative and hidden
   * from assistive technology, because an unnamed coloured circle conveys
   * nothing to a screen-reader user and announcing "image" is worse than
   * silence. If the dot is the only carrier of the state, pass a label.
   */
  label?: string;
  /** Renders `label` as visible text beside the dot rather than only for AT. */
  labelVisible?: boolean;
}

/**
 * A small multi-channel state indicator.
 *
 * COLOUR IS NEVER THE ONLY CHANNEL
 * --------------------------------
 * Every status has a distinct silhouette as well as a distinct hue, so the six
 * states stay separable for colour-vision-deficient users and under
 * `forced-colors`, where the OS discards our palette entirely (WCAG 2.2 SC
 * 1.4.1):
 *
 *   ok        filled disc
 *   degraded  thick hollow ring
 *   failed    filled square
 *   unknown   DASHED ring, hollow          <- "not observed"
 *   inactive  thin hollow ring             <- "observed, not running"
 *   pending   thin ring with a centre dot
 *
 * The `unknown` / `inactive` pair is the one that matters most. A dashed
 * outline reads as provisional; a thin solid outline reads as settled. They
 * must never be swapped, merged, or defaulted into one another.
 *
 * LAYOUT STABILITY
 * ----------------
 * Every status occupies exactly the same box, and the pulse halo is absolutely
 * positioned, so switching status or turning the pulse on never reflows the
 * row it sits in.
 */
export const StatusDot = forwardRef<HTMLSpanElement, StatusDotProps>(function StatusDot(
  { status = 'unknown', size = 'md', pulse = false, label, labelVisible = false, className, ...rest },
  ref,
) {
  // A consumer who names the dot with `aria-label` instead of `label` has named
  // it just as well, so both count — otherwise the dot would be force-hidden
  // from the very reader the caller was labelling it for.
  const providedLabel = label ?? rest['aria-label'];
  const named = providedLabel !== undefined && providedLabel !== '';

  return (
    <span
      // These three sit BEFORE the spread so a consumer-supplied value always
      // wins: after it, `aria-hidden="true"` would override the caller's own
      // `aria-label` and silence a dot they deliberately named.
      //
      // A dot with no name carries nothing an assistive technology can use;
      // hiding it is more useful than announcing an unlabelled graphic.
      aria-hidden={named ? undefined : 'true'}
      role={named && !labelVisible ? 'img' : undefined}
      aria-label={named && !labelVisible ? providedLabel : undefined}
      {...rest}
      ref={ref}
      data-stratum="status-dot"
      data-status={status}
      data-size={size}
      data-pulse={pulse || undefined}
      data-labelled={named || undefined}
      className={clsx('stratum-status-dot', className)}
    >
      {/* Painted first so the mark, which is positioned and later in DOM
       * order, sits on top of it without needing a stacking context. */}
      {pulse && <span className="stratum-status-dot__halo" aria-hidden="true" />}
      <span className="stratum-status-dot__mark" aria-hidden="true" />
      {named && labelVisible && <span className="stratum-status-dot__label">{label}</span>}
    </span>
  );
});

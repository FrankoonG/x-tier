import clsx from 'clsx';
import { Mark } from '../Mark/Mark';
import './Spinner.css';

export type SpinnerSize = 'xs' | 'sm' | 'md' | 'lg';

export interface SpinnerProps {
  size?: SpinnerSize;
  className?: string;
  /**
   * Accessible status text. When omitted the spinner is `aria-hidden`, which
   * is correct whenever a visible label already says what is happening —
   * announcing "Loading" twice is noise. Provide it only when the spinner is
   * the sole indication.
   */
  label?: string | undefined;
}

/** Pixel size per step. The mark is drawn at these, not scaled to them. */
const SIZE_PX: Record<SpinnerSize, number> = {
  xs: 14,
  sm: 16,
  md: 20,
  lg: 28,
};

/**
 * Indeterminate activity indicator — the brand mark, orbiting.
 *
 * It used to be a generic rotating arc. Every busy state in the library goes
 * through here, so that was the single largest surface in the product showing
 * a shape that could have belonged to anything; making it the mark costs
 * nothing and means the brand appears wherever the product is working.
 *
 * TONE. Always `inherit`, never `brand`. A spinner turns up inside buttons,
 * inside filled accent surfaces, inside disabled controls and inside text, and
 * in every one of those it has to be whatever colour the thing around it is.
 * `inherit` paints the whole mark in `currentColor`, which is also what makes
 * it survive dark-mode browser extensions — those substitute `currentColor`
 * reliably and frequently fail to resolve `var()` inside SVG paint.
 *
 * TEMPO. The mark's own, unmodified. A shortened cycle was tried on the
 * argument that the formation hold reads as a stall at 16px, but a spinner
 * beating faster than the same mark on the loading screen behind it is a
 * worse problem than a pause: one brand, one tempo. `cycle` remains available
 * for a caller with a specific reason.
 */
export function Spinner({ size = 'md', className, label }: SpinnerProps) {
  return (
    <span
      data-stratum="spinner"
      data-size={size}
      className={clsx('stratum-spinner', className)}
      role={label ? 'status' : undefined}
      aria-hidden={label ? undefined : true}
    >
      <Mark size={SIZE_PX[size]} tone="inherit" loading="swarm" />
      {label && <span className="stratum-visually-hidden">{label}</span>}
    </span>
  );
}

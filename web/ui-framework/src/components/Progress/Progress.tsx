import { forwardRef, useId, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Progress.css';

export type ProgressVariant =
  | 'accent'
  | 'success'
  | 'warning'
  | 'danger'
  | 'info'
  | 'neutral';

export type ProgressSize = 'xs' | 'sm' | 'md';

export interface ProgressProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  /**
   * Current value. Omit — or pass `null` — for an indeterminate bar, which is
   * the correct representation of "running, total unknown". Guessing a
   * percentage is worse than admitting you do not have one.
   */
  value?: number | null;
  min?: number;
  max?: number;
  variant?: ProgressVariant;
  size?: ProgressSize;
  /** Visible label rendered above the bar and wired up as the accessible name. */
  label?: ReactNode;
  /** Accessible name when there is no visible `label`. */
  ariaLabel?: string;
  /** Renders the formatted value at the end of the label row. */
  showValue?: boolean;
  /**
   * Formats the value for display AND for `aria-valuetext`. Default is a
   * whole percentage of the min..max span.
   */
  formatValue?: (value: number, min: number, max: number) => string;
  /** Overrides `aria-valuetext` only. */
  valueText?: string;
  /** Announced instead of a value while indeterminate. */
  indeterminateLabel?: string;
  /** Fills the whole track with the tone at low opacity behind the fill. */
  inverted?: boolean;
}

function clamp(value: number, min: number, max: number): number {
  if (Number.isNaN(value)) return min;
  return Math.min(Math.max(value, min), max);
}

const defaultFormat = (value: number, min: number, max: number): string => {
  const span = max - min;
  if (span <= 0) return '0%';
  return `${Math.round(((value - min) / span) * 100)}%`;
};

/**
 * A determinate or indeterminate progress bar.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - `role="progressbar"` with `aria-valuemin` / `aria-valuemax` always, and
 *   `aria-valuenow` only when determinate. Omitting `aria-valuenow` is the
 *   documented way to say "indeterminate"; setting it to `0` instead makes a
 *   screen reader announce a task that is permanently stuck at zero.
 * - `aria-valuetext` carries the human form, because "72" alone is
 *   meaningless where "72%" or "72 of 400 routes" is not.
 * - A progressbar is not focusable and must not be. It is a status, not a
 *   control; making it tabbable adds a stop that leads nowhere.
 *
 * MOTION
 * ------
 * The indeterminate sweep is the only looping animation here, and it stops
 * entirely under `prefers-reduced-motion: reduce`, where the bar falls back to
 * a static tinted track. Losing the loop does not lose the meaning: the bar is
 * still visibly in a busy state, and `aria-busy` says so regardless.
 */
export const Progress = forwardRef<HTMLDivElement, ProgressProps>(function Progress(
  {
    value,
    min = 0,
    max = 100,
    variant = 'accent',
    size = 'sm',
    label,
    ariaLabel,
    showValue = false,
    formatValue = defaultFormat,
    valueText,
    indeterminateLabel = 'Working',
    inverted = false,
    className,
    // Pulled off `rest` deliberately: naming attributes belong on the element
    // that carries `role="progressbar"`, not on the layout wrapper.
    'aria-label': ariaLabelAttr,
    'aria-labelledby': ariaLabelledBy,
    'aria-describedby': ariaDescribedBy,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const labelId = `${reactId}-label`;

  const safeMax = max > min ? max : min + 1;
  const indeterminate = value == null;
  const current = indeterminate ? min : clamp(value, min, safeMax);
  const fraction = indeterminate ? 0 : (current - min) / (safeMax - min);
  const display = indeterminate ? indeterminateLabel : formatValue(current, min, safeMax);

  const name = ariaLabel ?? ariaLabelAttr;

  if (import.meta.env?.DEV && label == null && !name && !ariaLabelledBy) {
    console.error(
      '[stratum] <Progress> needs an accessible name. Pass `label` for a visible one, ' +
        'or `ariaLabel` / `aria-labelledby` when the surrounding text already says what ' +
        'is progressing.',
    );
  }

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="progress"
      data-variant={variant}
      data-size={size}
      data-indeterminate={indeterminate || undefined}
      data-inverted={inverted || undefined}
      className={clsx('stratum-progress', className)}
    >
      {(label != null || showValue) && (
        <div className="stratum-progress__header">
          {label != null && (
            <span className="stratum-progress__label" id={labelId}>
              {label}
            </span>
          )}
          {showValue && (
            <span className="stratum-progress__value stratum-numeric">{display}</span>
          )}
        </div>
      )}

      <div
        className="stratum-progress__track"
        role="progressbar"
        aria-busy={indeterminate || undefined}
        aria-valuemin={min}
        aria-valuemax={safeMax}
        {...(indeterminate ? null : { 'aria-valuenow': current })}
        aria-valuetext={valueText ?? display}
        aria-label={label == null ? name : undefined}
        // Composed, not chosen: a bar is commonly labelled by both its own
        // caption and an enclosing section heading, and discarding the
        // consumer's id whenever a visible `label` is present loses the
        // second half of that name for no reason.
        aria-labelledby={
          [label != null ? labelId : null, ariaLabelledBy].filter(Boolean).join(' ') || undefined
        }
        aria-describedby={ariaDescribedBy}
      >
        <div
          className="stratum-progress__fill"
          style={indeterminate ? undefined : { ['--_fraction' as string]: String(fraction) }}
        />
      </div>
    </div>
  );
});

import { forwardRef, useId, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Meter.css';

/** Which band the current value falls into, after the threshold rules. */
export type MeterLevel = 'optimum' | 'suboptimal' | 'poor' | 'none';

export type MeterSize = 'xs' | 'sm' | 'md';

export interface MeterProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  value: number;
  min?: number;
  max?: number;
  /** Upper edge of the low band. */
  low?: number;
  /** Lower edge of the high band. */
  high?: number;
  /**
   * Where "good" lives. Defaults to `min` when `low`/`high` are supplied,
   * which is the right default for the things this component is for — disk,
   * quota, connection count — where less used is better.
   */
  optimum?: number;
  size?: MeterSize;
  label?: ReactNode;
  ariaLabel?: string;
  showValue?: boolean;
  /** Formats the display value AND `aria-valuetext`. Default is a percentage. */
  formatValue?: (value: number, min: number, max: number) => string;
  valueText?: string;
  /** Draws hairline ticks at `low` and `high`. Default `true` when set. */
  showThresholds?: boolean;
  /**
   * Text for each threshold band, appended to the announced value so the
   * judgement reaches a screen reader instead of living only in the fill
   * colour. Defaults to `{ suboptimal: 'above threshold', poor: 'critical' }`;
   * `optimum` and `none` have no default because "everything is fine" is not
   * worth announcing on every read. Pass `{}` to suppress it entirely.
   */
  labelLevel?: Partial<Record<MeterLevel, string>>;
}

const DEFAULT_LEVEL_LABELS: Partial<Record<MeterLevel, string>> = {
  suboptimal: 'above threshold',
  poor: 'critical',
};

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
 * Resolves the HTML `<meter>` three-band rule.
 *
 * The direction is decided by where `optimum` sits, exactly as the HTML
 * specification defines it, rather than by assuming "more is worse":
 *
 *   optimum below low   -> low is good, high is bad   (disk usage, error rate)
 *   optimum above high  -> high is good, low is bad   (battery, signal)
 *   optimum in between  -> the middle band is good, either side merely
 *                          suboptimal — never "poor", because being outside a
 *                          two-sided target is not the same as being at the
 *                          far end of a one-sided one.
 */
export function meterLevel(
  value: number,
  low: number | undefined,
  high: number | undefined,
  optimum: number | undefined,
): MeterLevel {
  if (low === undefined && high === undefined) return 'none';
  const lo = low ?? Number.NEGATIVE_INFINITY;
  const hi = high ?? Number.POSITIVE_INFINITY;
  const opt = optimum ?? Number.NEGATIVE_INFINITY;

  if (opt < lo) {
    if (value <= lo) return 'optimum';
    return value <= hi ? 'suboptimal' : 'poor';
  }
  if (opt > hi) {
    if (value >= hi) return 'optimum';
    return value >= lo ? 'suboptimal' : 'poor';
  }
  return value >= lo && value <= hi ? 'optimum' : 'suboptimal';
}

/**
 * A bounded scalar measurement — disk, quota, capacity, pool utilisation.
 *
 * WHY NOT `<progress>` OR `<meter>`
 * ---------------------------------
 * The native `<meter>` element is unstyleable in a portable way: Chrome,
 * Firefox and Safari each expose a different set of non-standard pseudo
 * elements, and none of them lets you set the bar colour from a custom
 * property. So the semantics are reproduced with `role="meter"`, which has
 * identical ARIA behaviour, on markup this framework can actually theme.
 *
 * PROGRESS VS METER
 * -----------------
 * `Progress` is a task that will finish. `Meter` is a level that just is.
 * "84% of quota used" never completes, and announcing it as progress tells a
 * screen reader user to wait for something that is not coming.
 *
 * ACCESSIBILITY NOTES
 * -------------------
 * - Threshold state reaches every user through text. `data-level` and the tick
 *   marks are CSS and visual channels only — neither is in the accessibility
 *   tree — so the band name from `labelLevel` is appended to `aria-valuetext`.
 *   Without it the optimum/suboptimal/poor judgement would be carried by fill
 *   hue alone, which is a WCAG 1.4.1 failure.
 * - `aria-valuetext` gets the formatted string; `aria-valuenow` gets the raw
 *   number. A raw "83" announced with no unit is not information.
 */
export const Meter = forwardRef<HTMLDivElement, MeterProps>(function Meter(
  {
    value,
    min = 0,
    max = 100,
    low,
    high,
    optimum,
    size = 'sm',
    label,
    ariaLabel,
    showValue = false,
    formatValue = defaultFormat,
    valueText,
    showThresholds = true,
    labelLevel = DEFAULT_LEVEL_LABELS,
    className,
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
  const current = clamp(value, min, safeMax);
  const span = safeMax - min;
  const fraction = (current - min) / span;

  const resolvedOptimum = optimum ?? (low !== undefined || high !== undefined ? min : undefined);
  const level = meterLevel(current, low, high, resolvedOptimum);
  const display = formatValue(current, min, safeMax);
  const name = ariaLabel ?? ariaLabelAttr;
  const levelText = labelLevel[level];
  const announcedValue = [display, levelText].filter(Boolean).join(', ');

  if (import.meta.env?.DEV && label == null && !name && !ariaLabelledBy) {
    console.error(
      '[stratum] <Meter> needs an accessible name. Pass `label` for a visible one, or ' +
        '`ariaLabel` / `aria-labelledby`.',
    );
  }

  const ticks: Array<{ key: string; at: number }> = [];
  if (showThresholds) {
    if (low !== undefined && low > min && low < safeMax) {
      ticks.push({ key: 'low', at: (low - min) / span });
    }
    if (high !== undefined && high > min && high < safeMax) {
      ticks.push({ key: 'high', at: (high - min) / span });
    }
  }

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="meter"
      data-level={level}
      data-size={size}
      className={clsx('stratum-meter', className)}
    >
      {(label != null || showValue) && (
        <div className="stratum-meter__header">
          {label != null && (
            <span className="stratum-meter__label" id={labelId}>
              {label}
            </span>
          )}
          {showValue && (
            <span className="stratum-meter__value stratum-numeric">{display}</span>
          )}
        </div>
      )}

      <div
        className="stratum-meter__track"
        role="meter"
        aria-valuenow={current}
        aria-valuemin={min}
        aria-valuemax={safeMax}
        aria-valuetext={valueText ?? announcedValue}
        aria-label={label == null ? name : undefined}
        // Composed rather than chosen — see Progress. A meter is routinely
        // named by both its own caption and the section heading above it.
        aria-labelledby={
          [label != null ? labelId : null, ariaLabelledBy].filter(Boolean).join(' ') || undefined
        }
        aria-describedby={ariaDescribedBy}
      >
        <div
          className="stratum-meter__fill"
          style={{ ['--_fraction' as string]: String(fraction) }}
        />
        {ticks.map((tick) => (
          <span
            key={tick.key}
            className="stratum-meter__tick"
            data-threshold={tick.key}
            aria-hidden="true"
            style={{ ['--_at' as string]: `${tick.at * 100}%` }}
          />
        ))}
      </div>
    </div>
  );
});

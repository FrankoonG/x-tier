import { forwardRef, useMemo, type CSSProperties, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import { UNOBSERVED, formatCount, formatPercent } from '../format';
import './HealthBar.css';

/**
 * Bucket a population is summarised into.
 *
 * `unknown` is a bucket in its own right and is drawn hatched rather than
 * filled — it is never folded into a failure bucket, because "3 peers we could
 * not reach a verdict on" and "3 peers that are down" are different facts and
 * only one of them warrants a page.
 */
export type HealthStatus =
  | 'ok'
  | 'degraded'
  | 'failed'
  | 'unknown'
  | 'inactive'
  | 'pending'
  | 'info';

export interface HealthSegment {
  id: string;
  label: string;
  /**
   * How many members are in this bucket.
   *
   * `null` / `undefined` means NOT OBSERVED and is never treated as zero. Such
   * a segment contributes nothing to the bar, is reported as unobserved in the
   * legend, and suppresses percentage arithmetic unless an explicit `total`
   * supplies a trustworthy denominator.
   */
  count?: number | null;
  status?: HealthStatus;
  /** Longer explanation surfaced on hover and to assistive technology. */
  note?: string;
}

export type HealthBarSize = 'sm' | 'md' | 'lg';

export interface HealthBarProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  segments: HealthSegment[];
  /**
   * Known population size. When it exceeds the sum of the observed counts, the
   * shortfall is drawn as an explicit *unaccounted* band rather than being
   * silently absorbed into the last bucket.
   */
  total?: number | null;
  size?: HealthBarSize;
  /** Renders the swatch/label/count key beneath the bar. Default `true`. */
  showLegend?: boolean;
  /** Shows counts in the legend. Default `true`. */
  showCounts?: boolean;
  /** Accessible name for the bar. */
  label?: string;
  /** Shown when nothing at all has been counted. Default `'nothing counted'`. */
  emptyLabel?: string;
  /** Name of the shortfall band. Default `'unaccounted'`. */
  unaccountedLabel?: string;
  /** Text for a bucket with no reading. Default `'not observed'`. */
  unobservedLabel?: string;
  locale?: string;
}

interface Band {
  key: string;
  label: string;
  status: HealthStatus | 'unaccounted';
  count: number;
  note?: string | undefined;
}

/**
 * A stacked bar summarising a population by status.
 *
 * WHAT THE BAR IS NOT ALLOWED TO DO
 * ---------------------------------
 * 1. It never invents a denominator. If any bucket has an unobserved count and
 *    no explicit `total` was supplied, the true population size is unknown, so
 *    no percentages are shown anywhere — not in the tooltip, not in the
 *    legend. A percentage over a denominator you guessed is a lie with a
 *    decimal point on it.
 * 2. It never folds `unknown` into a failure bucket. Unknown is hatched, not
 *    filled, and reads as texture rather than as a colour verdict.
 * 3. It never renders "nothing observed" as a full healthy bar. With no counts
 *    at all the rail stays empty and says so.
 * 4. A shortfall against an explicit `total` becomes a visible *unaccounted*
 *    band. Members you did not classify stay visible as members you did not
 *    classify.
 *
 * SIZING
 * ------
 * Bands are flex items grown in proportion to their counts, with a minimum
 * width, so a 1-in-500 failure is still a visible sliver rather than a
 * sub-pixel nothing. Flexbox honours the minimum by shrinking its neighbours,
 * which keeps the total width exact.
 */
export const HealthBar = forwardRef<HTMLDivElement, HealthBarProps>(function HealthBar(
  {
    segments,
    total,
    size = 'md',
    showLegend = true,
    showCounts = true,
    label,
    emptyLabel = 'nothing counted',
    unaccountedLabel = 'unaccounted',
    unobservedLabel = 'not observed',
    locale,
    className,
    ...rest
  },
  ref,
) {
  const model = useMemo(() => {
    let counted = 0;
    let hasUnobserved = false;

    for (const segment of segments) {
      const value = segment.count;
      if (value == null || !Number.isFinite(value)) {
        hasUnobserved = true;
        continue;
      }
      counted += Math.max(0, value);
    }

    const totalGiven = total != null && Number.isFinite(total) ? Math.max(0, total) : null;
    const unaccounted = totalGiven != null ? Math.max(0, totalGiven - counted) : 0;

    const bands: Band[] = [];
    for (const segment of segments) {
      const value = segment.count;
      if (value == null || !Number.isFinite(value) || value <= 0) continue;
      bands.push({
        key: segment.id,
        label: segment.label,
        status: segment.status ?? 'info',
        count: value,
        note: segment.note,
      });
    }
    if (unaccounted > 0) {
      bands.push({
        key: '__unaccounted',
        label: unaccountedLabel,
        status: 'unaccounted',
        count: unaccounted,
      });
    }

    // The denominator only exists when it is actually known. An explicit total
    // is authoritative; otherwise the sum is trustworthy ONLY if every bucket
    // was observed.
    const denominator = totalGiven ?? (hasUnobserved ? null : counted);

    return { bands, counted, hasUnobserved, denominator, unaccounted };
  }, [segments, total, unaccountedLabel]);

  const share = (count: number): string | null =>
    model.denominator != null && model.denominator > 0
      ? formatPercent(count / model.denominator)
      : null;

  const bandTitle = (band: Band): string => {
    const pct = share(band.count);
    return [`${band.label}: ${formatCount(band.count, locale)}${pct ? ` (${pct})` : ''}`, band.note]
      .filter(Boolean)
      .join(' · ');
  };

  // A single sentence covering every bucket, including the ones with no
  // reading — the bar's entire content, available without sight or colour.
  const summary = useMemo(() => {
    const parts = segments.map((segment) => {
      const value = segment.count;
      if (value == null || !Number.isFinite(value)) {
        return `${segment.label}: ${unobservedLabel}`;
      }
      return `${segment.label}: ${formatCount(value, locale)}`;
    });
    if (model.unaccounted > 0) {
      parts.push(`${unaccountedLabel}: ${formatCount(model.unaccounted, locale)}`);
    }
    if (parts.length === 0 || (model.counted === 0 && model.unaccounted === 0)) {
      parts.push(emptyLabel);
    }
    return parts.join(', ');
  }, [segments, model.unaccounted, model.counted, unaccountedLabel, unobservedLabel, emptyLabel, locale]);

  const isEmpty = model.bands.length === 0;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="health-bar"
      data-size={size}
      data-empty={isEmpty || undefined}
      data-incomplete={model.hasUnobserved || undefined}
      className={clsx('stratum-health-bar', className)}
    >
      <div
        className="stratum-health-bar__rail"
        role="img"
        aria-label={label ? `${label}. ${summary}` : summary}
      >
        {isEmpty ? (
          <span className="stratum-health-bar__empty" aria-hidden="true" />
        ) : (
          model.bands.map((band) => (
            <span
              key={band.key}
              className="stratum-health-bar__band"
              data-status={band.status}
              title={bandTitle(band)}
              style={{ flexGrow: band.count } as CSSProperties}
            />
          ))
        )}
      </div>

      {showLegend && (
        <div className="stratum-health-bar__legend">
          {segments.map((segment) => {
            const value = segment.count;
            const observed = value != null && Number.isFinite(value);
            const pct = observed ? share(value) : null;
            return (
              <span
                key={segment.id}
                className="stratum-health-bar__legend-item"
                data-status={segment.status ?? 'info'}
                data-unobserved={observed ? undefined : true}
                title={[
                  `${segment.label}: ${observed ? formatCount(value, locale) : unobservedLabel}${
                    pct ? ` (${pct})` : ''
                  }`,
                  segment.note,
                ]
                  .filter(Boolean)
                  .join(' · ')}
              >
                <span className="stratum-health-bar__swatch" aria-hidden="true" />
                <span className="stratum-health-bar__legend-label">{segment.label}</span>
                {showCounts && (
                  <span className="stratum-health-bar__legend-count">
                    {observed ? formatCount(value, locale) : UNOBSERVED}
                  </span>
                )}
                {!observed && (
                  <span className="stratum-visually-hidden">, {unobservedLabel}</span>
                )}
              </span>
            );
          })}

          {model.unaccounted > 0 && (
            <span
              className="stratum-health-bar__legend-item"
              data-status="unaccounted"
              title={`${unaccountedLabel}: ${formatCount(model.unaccounted, locale)}${
                share(model.unaccounted) ? ` (${share(model.unaccounted)})` : ''
              }`}
            >
              <span className="stratum-health-bar__swatch" aria-hidden="true" />
              <span className="stratum-health-bar__legend-label">{unaccountedLabel}</span>
              {showCounts && (
                <span className="stratum-health-bar__legend-count">
                  {formatCount(model.unaccounted, locale)}
                </span>
              )}
            </span>
          )}

          {isEmpty && <span className="stratum-health-bar__note">{emptyLabel}</span>}
        </div>
      )}
    </div>
  );
});

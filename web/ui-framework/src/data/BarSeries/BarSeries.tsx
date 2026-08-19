import { forwardRef, useMemo, type CSSProperties, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { UNOBSERVED, formatCount, formatPercent } from '../../network/format';
import { ChartTable, clamp, type ChartTableRow, type SeriesColor } from '../chart';
import './BarSeries.css';

/** Severity of a row. Overrides the default single-colour treatment. */
export type BarStatus = 'ok' | 'warning' | 'danger' | 'info' | 'neutral';

export interface BarDatum {
  id: string;
  label?: string;
  /**
   * `null` means NOT OBSERVED. The row keeps its place and shows an empty
   * dashed track — it is never collapsed to a zero-length bar, which would
   * claim a measurement of nothing.
   */
  value: number | null | undefined;
  /** 1-based slot into `--stratum-series-N`, or any CSS colour. */
  color?: SeriesColor;
  status?: BarStatus;
  /** Secondary text beside the label. */
  meta?: ReactNode;
}

export interface BarSeriesProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'color' | 'onSelect'> {
  data: BarDatum[];
  /** Domain maximum. Without it, the largest observed value. */
  max?: number;
  /** Enables a share-of-total readout. Not used for bar length. */
  total?: number;
  /** Formats the value beside each bar. */
  formatValue?: (value: number | null | undefined) => string;
  /** Keeps the top N rows and folds the rest. */
  limit?: number;
  /** Label for the folded remainder row. */
  otherLabel?: string;
  /** Sorts descending by observed value before applying `limit`. */
  sort?: 'value' | 'none';
  /** Bar thickness in CSS pixels. */
  barHeight?: number;
  showValue?: boolean;
  /** Makes rows activatable. Rows become buttons and gain a focus ring. */
  onSelect?: (id: string) => void;
  selectedId?: string;
  /** Reserved width for the label column. */
  labelWidth?: string;
  label?: string;
  emptyLabel?: string;
  unobservedLabel?: string;
  tableCaption?: string;
  valueColumnLabel?: string;
  shareColumnLabel?: string;
  accessibleTable?: boolean;
}

/** A datum after normalisation: `undefined` has been folded into `null`. */
type NormalisedDatum = Omit<BarDatum, 'value'> & { value: number | null };

interface Row extends NormalisedDatum {
  /** Bar length as a fraction of `max`, or `null` when unobserved. */
  fraction: number | null;
  /** Number of source rows folded into this one; 0 for a normal row. */
  folded: number;
  /** How many of the folded rows had no observation. */
  foldedUnobserved: number;
}

/**
 * A compact horizontal bar list — top talkers, per-application traffic,
 * per-peer counts.
 *
 * ONE COLOUR, NOT A RAMP
 * ----------------------
 * Every bar is `--stratum-series-1` unless the caller says otherwise. Shading
 * bars darker-where-longer double-encodes length as hue: it spends the only
 * free channel on information the bar already shows, and it implies an
 * ordering among categories — applications, peers, endpoints — that have none.
 * Severity (`status`) and per-entity colour are both available where the
 * colour genuinely means something.
 *
 * THE TAIL FOLDS, IT DOES NOT CYCLE
 * ---------------------------------
 * Past `limit`, rows collapse into a single remainder rather than continuing
 * to draw hues that repeat. The remainder reports how many rows it covers AND
 * how many of them were unobserved, because a total assembled from partially
 * missing data is a partial total and saying so is the difference between a
 * summary and a fabrication.
 *
 * UNOBSERVED ROWS KEEP THEIR PLACE
 * --------------------------------
 * A row with no reading renders as a dashed empty track with the unobserved
 * dash for a value. Dropping it would hide that the entity exists; drawing a
 * zero-length bar would assert it was measured at zero. Neither is true.
 */
export const BarSeries = forwardRef<HTMLDivElement, BarSeriesProps>(function BarSeries(
  {
    data,
    max,
    total,
    formatValue = (v) => (v == null || !Number.isFinite(v) ? UNOBSERVED : formatCount(v)),
    limit,
    otherLabel = 'Other',
    sort = 'value',
    barHeight = 8,
    showValue = true,
    onSelect,
    selectedId,
    labelWidth = '9rem',
    label,
    emptyLabel = 'No data',
    unobservedLabel = 'not observed',
    tableCaption = 'Bar chart data',
    valueColumnLabel = 'Value',
    shareColumnLabel = 'Share',
    accessibleTable = true,
    className,
    style,
    ...rest
  },
  ref,
) {
  const model = useMemo(() => {
    const normalised: NormalisedDatum[] = data.map((d) => ({
      ...d,
      value: d.value == null || !Number.isFinite(d.value) ? null : d.value,
    }));

    const ordered =
      sort === 'value'
        ? [...normalised].sort((a, b) => {
            // Unobserved rows sink to the bottom rather than sorting as zero,
            // which would interleave them with genuinely idle entities.
            if (a.value == null && b.value == null) return 0;
            if (a.value == null) return 1;
            if (b.value == null) return -1;
            return b.value - a.value;
          })
        : normalised;

    let kept = ordered;
    let remainder: Row | null = null;

    if (limit != null && limit > 0 && ordered.length > limit) {
      kept = ordered.slice(0, limit);
      const tail = ordered.slice(limit);
      let sum = 0;
      let seen = 0;
      let missing = 0;
      for (const t of tail) {
        if (t.value == null) {
          missing += 1;
          continue;
        }
        sum += t.value;
        seen += 1;
      }
      remainder = {
        id: '__stratum_other__',
        label: otherLabel,
        value: seen > 0 ? sum : null,
        status: 'neutral',
        fraction: null,
        folded: tail.length,
        foldedUnobserved: missing,
      };
    }

    let observedMax = 0;
    let observedCount = 0;
    for (const d of normalised) {
      if (d.value == null) continue;
      observedCount += 1;
      if (d.value > observedMax) observedMax = d.value;
    }
    if (remainder?.value != null && remainder.value > observedMax) observedMax = remainder.value;

    const domainMax = max ?? observedMax;

    const toRow = (d: NormalisedDatum, folded: number, foldedMissing: number): Row => ({
      ...d,
      folded,
      foldedUnobserved: foldedMissing,
      fraction:
        d.value == null ? null : domainMax <= 0 ? 0 : clamp(d.value / domainMax, 0, 1),
    });

    const rows: Row[] = kept.map((d) => toRow(d, 0, 0));
    if (remainder) rows.push(toRow(remainder, remainder.folded, remainder.foldedUnobserved));

    return { rows, domainMax, observedCount, hiddenCount: ordered.length - kept.length };
  }, [data, sort, limit, otherLabel, max]);

  const hasData = model.observedCount > 0;

  const summary = useMemo(() => {
    const head = label ? `${label}. ` : '';
    if (!hasData) return `${head}${emptyLabel}`;
    const missing = data.length - model.observedCount;
    const top = model.rows.find((r) => r.value != null);
    return (
      `${head}Bar chart, ${data.length} items` +
      (missing > 0 ? `, ${missing} ${unobservedLabel}` : '') +
      (top ? `, largest ${top.label ?? top.id} at ${formatValue(top.value)}` : '') +
      '.'
    );
  }, [label, hasData, emptyLabel, data.length, model.observedCount, model.rows, formatValue, unobservedLabel]);

  const tableRows = useMemo<ChartTableRow[]>(() => {
    if (!accessibleTable || !hasData) return [];
    return model.rows.map((r) => {
      const cells = [r.value == null ? unobservedLabel : formatValue(r.value)];
      if (total != null && total > 0) {
        cells.push(r.value == null ? unobservedLabel : formatPercent(r.value / total));
      }
      return { key: r.id, header: r.label ?? r.id, cells };
    });
  }, [accessibleTable, hasData, model.rows, formatValue, total, unobservedLabel]);

  const interactive = typeof onSelect === 'function';

  return (
    <div
      // NOT `role="img"`. Unlike the SVG charts in this family, a bar list
      // renders its labels and values as real text, and `img` would make every
      // one of them — and the row buttons — unreachable. The summary is still
      // supplied as the group's accessible name.
      //
      // Both sit before the spread so a consumer-supplied `role` or
      // `aria-label` wins instead of being silently replaced by the default.
      role="group"
      aria-label={summary}
      {...rest}
      ref={ref}
      data-stratum="bar-series"
      data-empty={!hasData || undefined}
      data-interactive={interactive || undefined}
      className={clsx('stratum-bar-series', className)}
      style={
        {
          '--_label-w': labelWidth,
          '--_bar-h': `${barHeight}px`,
          ...style,
        } as CSSProperties
      }
    >
      {!hasData && <p className="stratum-bar-series__empty">{emptyLabel}</p>}

      {hasData && (
        <ul className="stratum-bar-series__list">
          {model.rows.map((r) => {
            const pct = r.fraction == null ? 0 : r.fraction * 100;
            const unobserved = r.value == null;
            const share = total != null && total > 0 && r.value != null ? r.value / total : null;

            const name = r.label ?? r.id;
            // The remainder row states how many rows it covers AND how many of
            // those had no reading, because a total assembled from partially
            // missing data is a partial total.
            const foldNote =
              r.folded > 0
                ? `${r.folded} more${
                    r.foldedUnobserved > 0 ? `, ${r.foldedUnobserved} ${unobservedLabel}` : ''
                  }`
                : null;

            const body = (
              <>
                <span
                  className="stratum-bar-series__label"
                  title={foldNote ? `${name} — ${foldNote}` : name}
                >
                  {name}
                  {foldNote && <span className="stratum-bar-series__meta">{foldNote}</span>}
                  {r.meta != null && <span className="stratum-bar-series__meta">{r.meta}</span>}
                </span>

                <span className="stratum-bar-series__track" data-unobserved={unobserved || undefined}>
                  {/* No viewBox: one SVG user unit is one CSS pixel, so the
                      bar's rounded end keeps its shape at every width. A
                      viewBox with `preserveAspectRatio="none"` would stretch
                      the corner radius horizontally as the column resizes. */}
                  <svg
                    className="stratum-bar-series__svg"
                    width="100%"
                    height={barHeight}
                    aria-hidden="true"
                    focusable="false"
                  >
                    <rect
                      className="stratum-bar-series__rail"
                      x={0}
                      y={0}
                      width="100%"
                      height={barHeight}
                      fill="currentColor"
                    />
                    {!unobserved && (
                      <rect
                        className="stratum-bar-series__fill"
                        x={0}
                        y={0}
                        width={`${pct}%`}
                        height={barHeight}
                        fill="currentColor"
                      />
                    )}
                  </svg>
                </span>

                {showValue && (
                  <span
                    className="stratum-bar-series__value"
                    data-unobserved={unobserved || undefined}
                  >
                    {unobserved ? unobservedLabel : formatValue(r.value)}
                    {share != null && (
                      <span className="stratum-bar-series__share">{formatPercent(share)}</span>
                    )}
                  </span>
                )}
              </>
            );

            // One colour for the whole set unless the caller asked otherwise.
            // Shading by rank would double-encode length as hue and imply an
            // ordering that nominal categories do not have.
            const slot =
              typeof r.color === 'number'
                ? ((r.color - 1) % 8) + 1
                : r.status || typeof r.color === 'string'
                  ? undefined
                  : 1;

            return (
              <li
                key={r.id}
                className="stratum-bar-series__row"
                data-status={r.status ?? undefined}
                data-series={slot}
                data-selected={selectedId === r.id || undefined}
                {...(typeof r.color === 'string'
                  ? { 'data-custom-color': '', style: { color: r.color } }
                  : {})}
              >
                {interactive ? (
                  <button
                    type="button"
                    className="stratum-bar-series__button"
                    aria-pressed={selectedId === r.id}
                    onClick={() => onSelect?.(r.id)}
                  >
                    {body}
                  </button>
                ) : (
                  body
                )}
              </li>
            );
          })}
        </ul>
      )}

      {accessibleTable && hasData && (
        <ChartTable
          caption={label ? `${label} — ${tableCaption}` : tableCaption}
          rowHeader="Item"
          columns={total != null && total > 0 ? [valueColumnLabel, shareColumnLabel] : [valueColumnLabel]}
          rows={tableRows}
          {...(model.hiddenCount > 0
            ? { note: `${model.hiddenCount} further items are folded into "${otherLabel}".` }
            : {})}
        />
      )}
    </div>
  );
});

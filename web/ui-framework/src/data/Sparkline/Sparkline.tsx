import { forwardRef, useId, useMemo, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import { formatCount } from '../../network/format';
import {
  ChartTable,
  areaPath,
  linePath,
  markerPath,
  resolveSeriesPaint,
  toSegments,
  type ChartTableRow,
  type MarkerShape,
  type Pt,
  type SeriesColor,
} from '../chart';
import './Sparkline.css';

/** A single sample. `null` means NOT OBSERVED — never zero. */
export type SparklineDatum = number | null | undefined;

export interface SparklineSeries {
  values: SparklineDatum[];
  /** Names the series in the text alternative. */
  label?: string;
  /** 1-based slot into `--stratum-series-N`, or any CSS colour. */
  color?: SeriesColor;
  /** Overrides the slot's dash pattern. `'none'` forces a solid line. */
  dash?: string | 'none';
  /** Overrides the slot's marker shape. */
  marker?: MarkerShape | 'none';
}

export interface SparklineProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'color'> {
  /** One series as a bare array, or several as `{ values, label, color }`. */
  data: SparklineDatum[] | SparklineSeries[];
  /** Intrinsic width in viewBox units, which are CSS pixels at natural size. */
  width?: number;
  height?: number;
  /** Line weight. Held constant under scaling by `non-scaling-stroke`. */
  strokeWidth?: number;
  /** Washes the area under the line. Applies to single-series only. */
  showArea?: boolean;
  /** Marks the most recent observed sample of each series. */
  showLastPoint?: boolean;
  /** Lower bound override. Without it the domain is the observed extent. */
  min?: number;
  /** Upper bound override. */
  max?: number;
  /**
   * Draws a reference line at this value — normally 0. The value is folded
   * into the domain, since a baseline outside the visible range is not a
   * baseline. Areas fill down to it rather than to the frame.
   */
  baseline?: number | null;
  /** Formats values in the label and the table. Default: thousands-separated. */
  formatValue?: (value: number) => string;
  /** Accessible name. The data summary is appended to it. */
  label?: string;
  /** Announced when there is not enough data to draw a trend. */
  emptyLabel?: string;
  /** Word for a hole in the series, used in the table. */
  unobservedLabel?: string;
  /** Caption for the visually-hidden data table. */
  tableCaption?: string;
  /** Column header for the sample index. */
  tableIndexLabel?: string;
  /** Emits the visually-hidden data table. */
  accessibleTable?: boolean;
  /**
   * Marks the sparkline as decoration whose value is already stated in text
   * beside it. Removes it from the accessibility tree and suppresses the
   * table, so a stat tile does not announce its number twice.
   */
  decorative?: boolean;
}

interface Prepared {
  label: string;
  slot: number;
  custom: string | undefined;
  dash: string;
  marker: MarkerShape;
  values: SparklineDatum[];
}

function normalise(data: SparklineDatum[] | SparklineSeries[]): SparklineSeries[] {
  if (data.length === 0) return [];
  const first = data[0];
  if (first == null || typeof first === 'number') {
    return [{ values: data as SparklineDatum[] }];
  }
  return data as SparklineSeries[];
}

/**
 * A trend line small enough to live inside a table cell.
 *
 * WHAT IT REFUSES TO DO
 * ---------------------
 * A sparkline is read at a glance, in about 200 ms, usually next to a number.
 * That makes it unusually good at lying, so three cases are handled explicitly
 * rather than left to a default:
 *
 * - **Fewer than two observations: nothing is drawn.** One point is not a
 *   trend, and a lone dot in a box reads as one. The count still appears in
 *   the accessible name, so the observation is not lost — only the claim that
 *   it forms a trend is withheld.
 *
 * - **All values equal: a flat line through the vertical centre.** The naive
 *   normalisation divides by a zero range and pins the line to the top, the
 *   bottom, or `NaN`. A steady value is real information and should look
 *   steady.
 *
 * - **Holes break the line.** A `null` is a sample that was never taken. Every
 *   mainstream charting default connects straight across it, drawing a
 *   confident segment through a period nobody measured and making it
 *   indistinguishable from real data. Here the run ends and a new one begins.
 *   An isolated observation between two holes is drawn as a lone marker rather
 *   than dropped — discarding a measurement is its own kind of lie.
 *
 * The frame renders even with no data, so a column of sparklines in a live
 * table never reflows as series arrive.
 */
export const Sparkline = forwardRef<HTMLDivElement, SparklineProps>(function Sparkline(
  {
    data,
    width = 96,
    height = 24,
    strokeWidth = 1.5,
    showArea = false,
    showLastPoint = false,
    min,
    max,
    baseline = null,
    formatValue = (v) => formatCount(v),
    label,
    emptyLabel = 'No trend data',
    unobservedLabel = 'not observed',
    tableCaption = 'Trend data',
    tableIndexLabel = 'Sample',
    accessibleTable = true,
    decorative = false,
    className,
    ...rest
  },
  ref,
) {
  const uid = useId();

  const series = useMemo<Prepared[]>(
    () =>
      normalise(data).map((s, i) => {
        const overrides: { dash?: string | 'none'; marker?: MarkerShape | 'none' } = {};
        if (s.dash !== undefined) overrides.dash = s.dash;
        if (s.marker !== undefined) overrides.marker = s.marker;
        const paint = resolveSeriesPaint(s.color, i, overrides);
        return {
          label: s.label ?? `Series ${i + 1}`,
          slot: paint.slot,
          custom: paint.custom,
          dash: paint.dash,
          marker: paint.marker,
          values: s.values,
        };
      }),
    [data],
  );

  const markerRadius = Math.max(1.4, strokeWidth * 1.15);
  const pad = Math.max(strokeWidth / 2, showLastPoint ? markerRadius + strokeWidth / 2 : 0);

  const model = useMemo(() => {
    let observed = 0;
    let lo = Infinity;
    let hi = -Infinity;
    let columns = 0;

    for (const s of series) {
      if (s.values.length > columns) columns = s.values.length;
      for (const v of s.values) {
        if (v == null || !Number.isFinite(v)) continue;
        observed += 1;
        if (v < lo) lo = v;
        if (v > hi) hi = v;
      }
    }

    const hasBaseline = baseline != null && Number.isFinite(baseline);
    if (hasBaseline && baseline != null) {
      lo = Math.min(lo, baseline);
      hi = Math.max(hi, baseline);
    }

    const domainMin = min ?? (Number.isFinite(lo) ? lo : 0);
    const domainMax = max ?? (Number.isFinite(hi) ? hi : 0);
    const span = domainMax - domainMin;

    const innerW = Math.max(0, width - pad * 2);
    const innerH = Math.max(0, height - pad * 2);
    const denom = Math.max(1, columns - 1);

    const xAt = (i: number): number => (columns <= 1 ? width / 2 : pad + (i / denom) * innerW);
    // A flat series has zero span. Centring is the only reading that does not
    // imply the value sat at an extreme of a range which does not exist.
    const yAt = (v: number): number =>
      span === 0 ? height / 2 : pad + innerH - ((v - domainMin) / span) * innerH;

    // Two observations is the floor for a trend. Below it, nothing is drawn.
    const drawable = observed >= 2 && columns >= 2;

    const baselineY = hasBaseline && baseline != null ? yAt(baseline) : pad + innerH;

    const shapes = drawable
      ? series.map((s) => {
          const points: (Pt | null)[] = s.values.map((v, i) =>
            v == null || !Number.isFinite(v) ? null : { x: xAt(i), y: yAt(v) },
          );
          const segments = toSegments(points);
          let last: Pt | null = null;
          for (let i = points.length - 1; i >= 0; i -= 1) {
            const p = points[i];
            if (p) {
              last = p;
              break;
            }
          }
          return {
            paint: s,
            runs: segments.filter((seg) => seg.length >= 2),
            islands: segments.filter((seg) => seg.length === 1).map((seg) => seg[0]!),
            last,
          };
        })
      : [];

    return { observed, columns, hasBaseline, baselineY, drawable, shapes };
  }, [series, min, max, baseline, width, height, pad]);

  const summary = useMemo(() => {
    if (!model.drawable) return label ? `${label}. ${emptyLabel}` : emptyLabel;
    const parts: string[] = [];
    for (const s of series) {
      let lo = Infinity;
      let hi = -Infinity;
      let latest: number | undefined;
      let gaps = 0;
      for (const v of s.values) {
        if (v == null || !Number.isFinite(v)) {
          gaps += 1;
          continue;
        }
        if (v < lo) lo = v;
        if (v > hi) hi = v;
        latest = v;
      }
      const head = series.length > 1 ? `${s.label}: ` : '';
      const bits = [`${s.values.length} samples`];
      if (gaps > 0) bits.push(`${gaps} ${unobservedLabel}`);
      if (Number.isFinite(lo)) bits.push(`low ${formatValue(lo)}`);
      if (Number.isFinite(hi)) bits.push(`high ${formatValue(hi)}`);
      if (latest !== undefined) bits.push(`latest ${formatValue(latest)}`);
      parts.push(head + bits.join(', '));
    }
    return `${label ? `${label}. ` : ''}${parts.join('. ')}`;
  }, [model.drawable, series, label, emptyLabel, unobservedLabel, formatValue]);

  const tableRows = useMemo<ChartTableRow[]>(() => {
    if (!accessibleTable || decorative || !model.drawable) return [];
    const rows: ChartTableRow[] = [];
    for (let i = 0; i < model.columns; i += 1) {
      rows.push({
        key: `r${i}`,
        header: String(i + 1),
        cells: series.map((s) => {
          const v = s.values[i];
          return v == null || !Number.isFinite(v) ? unobservedLabel : formatValue(v);
        }),
      });
    }
    return rows;
  }, [accessibleTable, decorative, model.drawable, model.columns, series, unobservedLabel, formatValue]);

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="sparkline"
      data-empty={!model.drawable || undefined}
      data-multi={series.length > 1 || undefined}
      className={clsx('stratum-sparkline', className)}
      {...(decorative ? { 'aria-hidden': true } : {})}
    >
      <svg
        className="stratum-sparkline__svg"
        viewBox={`0 0 ${width} ${height}`}
        width={width}
        height={height}
        focusable="false"
        {...(decorative ? {} : { role: 'img', 'aria-label': summary })}
      >
        {/* Nothing to plot. A dotted rule at the vertical centre reads as
            "not measured", and is deliberately unlike a flat line at zero. */}
        {!model.drawable && (
          <line
            className="stratum-sparkline__empty"
            x1={pad}
            x2={width - pad}
            y1={height / 2}
            y2={height / 2}
            stroke="currentColor"
            strokeDasharray="2 3"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        )}

        {model.hasBaseline && model.drawable && (
          <line
            className="stratum-sparkline__baseline"
            x1={0}
            x2={width}
            y1={model.baselineY}
            y2={model.baselineY}
            stroke="currentColor"
            vectorEffect="non-scaling-stroke"
          />
        )}

        {model.shapes.map((r, i) => (
          <g
            key={`${uid}-s${i}`}
            className="stratum-sparkline__series"
            data-series={r.paint.slot}
            {...(r.paint.custom ? { style: { color: r.paint.custom } } : {})}
          >
            {showArea &&
              series.length === 1 &&
              r.runs.map((seg, j) => (
                <path
                  key={`a${j}`}
                  className="stratum-sparkline__area"
                  d={areaPath(seg, model.baselineY)}
                  fill="currentColor"
                />
              ))}

            {r.runs.map((seg, j) => (
              <path
                key={`l${j}`}
                className="stratum-sparkline__line"
                d={linePath(seg)}
                fill="none"
                stroke="currentColor"
                strokeWidth={strokeWidth}
                strokeLinecap="round"
                strokeLinejoin="round"
                {...(r.paint.dash && series.length > 1
                  ? { strokeDasharray: r.paint.dash }
                  : {})}
                vectorEffect="non-scaling-stroke"
              />
            ))}

            {/* An observation with no neighbours. Drawn, never dropped. */}
            {r.islands.map((p, j) => (
              <path
                key={`i${j}`}
                className="stratum-sparkline__island"
                d={markerPath(r.paint.marker, p.x, p.y, markerRadius)}
                fill="currentColor"
              />
            ))}

            {showLastPoint && r.last && (
              <path
                className="stratum-sparkline__last"
                d={markerPath(r.paint.marker, r.last.x, r.last.y, markerRadius)}
                fill="currentColor"
              />
            )}
          </g>
        ))}
      </svg>

      {!decorative && accessibleTable && model.drawable && (
        <ChartTable
          caption={label ? `${label} — ${tableCaption}` : tableCaption}
          rowHeader={tableIndexLabel}
          columns={series.map((s) => s.label)}
          rows={tableRows}
        />
      )}
    </div>
  );
});

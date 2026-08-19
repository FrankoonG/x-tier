import {
  forwardRef,
  useCallback,
  useId,
  useMemo,
  useRef,
  useState,
  type HTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { UNOBSERVED, formatCount, formatTimestamp } from '../../network/format';
import {
  ChartTable,
  areaPath,
  axisTimeFormatter,
  clamp,
  linePath,
  markerPath,
  niceTicks,
  niceTimeTicks,
  resolveSeriesPaint,
  toSegments,
  type ChartTableRow,
  type MarkerShape,
  type Pt,
  type SeriesColor,
} from '../chart';
import './TimeSeriesChart.css';

export interface TimeSeriesPoint {
  /** Usually epoch milliseconds, but any monotonic number works. */
  x: number;
  /** `null` means NOT OBSERVED at this x. The line breaks; it never bridges. */
  y: number | null | undefined;
}

export interface TimeSeriesSeries {
  id: string;
  label?: string;
  points: TimeSeriesPoint[];
  /** 1-based slot into `--stratum-series-N`, or any CSS colour. */
  color?: SeriesColor;
  /** Fills the area under the line with a wash of the series colour. */
  area?: boolean;
  /** Overrides the slot's dash pattern. `'none'` forces solid. */
  dash?: string | 'none';
  /** Overrides the slot's marker shape. `'none'` suppresses markers. */
  marker?: MarkerShape | 'none';
}

export interface TimeSeriesPadding {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

export interface TimeSeriesChartProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'color' | 'onChange'> {
  series: TimeSeriesSeries[];
  /** viewBox width. The chart scales to its container; strokes do not. */
  width?: number;
  height?: number;
  /** Reserved gutters, in viewBox units. The axis bands live inside `height`. */
  padding?: Partial<TimeSeriesPadding>;
  /** Fixes the x range. Without it, the observed extent is used. */
  xDomain?: [number, number];
  /** Fixes the y range. Without it, the observed extent padded to nice ticks. */
  yDomain?: [number, number];
  /** Includes zero in the y domain even when no sample is near it. */
  includeZero?: boolean;
  /**
   * How x tick positions are chosen. `'time'` snaps to human intervals —
   * seconds, minutes, quarter hours, days — and is right whenever x is a
   * timestamp. `'linear'` uses 1/2/5 rounding, for a non-temporal x.
   */
  xScale?: 'time' | 'linear';
  /**
   * X tick text. Defaults to a clock or date chosen from the plotted span —
   * seconds for a five-minute window, dates for a multi-day one.
   */
  formatX?: (value: number) => string;
  /** Locale for the default tick formatter. */
  locale?: string;
  /** Y tick text. Default: thousands-separated. */
  formatY?: (value: number) => string;
  /** X in the tooltip and table. Default: full localised timestamp. */
  formatXLong?: (value: number) => string;
  /** Y in the tooltip and table. `null` must render as unobserved. */
  formatValue?: (value: number | null | undefined) => string;
  xTickCount?: number;
  yTickCount?: number;
  /** Minimum gap between rendered x tick labels, in viewBox units. */
  minXTickSpacing?: number;
  grid?: 'none' | 'x' | 'y' | 'both';
  /** Point markers. `'auto'` draws them when the series are sparse enough. */
  markers?: boolean | 'auto';
  /** Threshold for `markers='auto'`. */
  markerThreshold?: number;
  showLegend?: boolean;
  /** Ids of series hidden by the legend (controlled). */
  hiddenSeries?: string[];
  defaultHiddenSeries?: string[];
  onHiddenSeriesChange?: (hidden: string[]) => void;
  /** Accessible name for the picture. */
  label?: string;
  xAxisLabel?: string;
  yAxisLabel?: string;
  legendLabel?: string;
  /** Accessible name for the keyboard scrubber. */
  scrubLabel?: string;
  /** Hint appended to the scrubber's description. */
  scrubHint?: string;
  emptyLabel?: string;
  unobservedLabel?: string;
  tableCaption?: string;
  accessibleTable?: boolean;
  /** Caps table rows. Truncation is declared in the table footer. */
  tableRowLimit?: number;
  /** Fired as the crosshair moves. `null` when it leaves the plot. */
  onActiveChange?: (x: number | null) => void;
}

const DEFAULT_PADDING: TimeSeriesPadding = { top: 10, right: 14, bottom: 24, left: 46 };

interface PreparedSeries {
  id: string;
  label: string;
  slot: number;
  custom: string | undefined;
  dash: string;
  marker: MarkerShape;
  suppressMarker: boolean;
  area: boolean;
  points: TimeSeriesPoint[];
  byX: Map<number, number | null>;
}

/**
 * A small multi-series line/area chart with axes, a crosshair and a legend.
 *
 * ONE AXIS, ALWAYS
 * ----------------
 * There is deliberately no second y-scale. A dual-axis chart lets the author
 * choose where two unrelated scales cross, and that choice manufactures a
 * correlation the data never contained — it is the single most misleading
 * thing a line chart can do. Two measures of different magnitude belong in two
 * charts, or indexed to a common base.
 *
 * COLOUR IS NOT THE ONLY CHANNEL
 * ------------------------------
 * Each series slot carries a dash pattern and a marker shape as well as a hue,
 * so the chart survives colour-vision deficiency, grayscale print and
 * `forced-colors`. Slot assignment follows the series' position in the ORIGINAL
 * list, so hiding one from the legend never repaints the survivors — a reader
 * who learned "relay-b is the dashed teal one" keeps being right.
 *
 * THE CROSSHAIR IS KEYBOARD-OPERABLE
 * ----------------------------------
 * A tooltip that only responds to a pointer makes the values it holds
 * unreachable by keyboard, which means the chart has data that some users
 * cannot get at all. Here the plot is one tab stop; arrow keys scrub sample by
 * sample, `Home`/`End` jump to the ends, `PageUp`/`PageDown` move by a tenth
 * of the record, and `Escape` releases the cursor. Each move is announced
 * through a polite live region. Pointer movement deliberately does NOT write
 * to that region — mouse users are already reading the tooltip, and a live
 * region updating on every mousemove is unusable noise.
 *
 * A series with no sample at the crosshair's x reads as unobserved rather than
 * being omitted from the tooltip. Its absence at that instant is a fact worth
 * stating, and silently dropping the row invites the reader to assume the
 * series simply had the same value as its neighbour.
 */
export const TimeSeriesChart = forwardRef<HTMLDivElement, TimeSeriesChartProps>(
  function TimeSeriesChart(
    {
      series,
      width = 480,
      height = 200,
      padding,
      xDomain,
      yDomain,
      includeZero = false,
      xScale = 'time',
      formatX,
      locale,
      formatY = (v) => formatCount(v),
      formatXLong = (v) => formatTimestamp(v, locale !== undefined ? { locale } : {}),
      formatValue = (v) => (v == null || !Number.isFinite(v) ? UNOBSERVED : formatCount(v)),
      xTickCount = 5,
      yTickCount = 4,
      minXTickSpacing = 64,
      grid = 'y',
      markers = 'auto',
      markerThreshold = 40,
      showLegend = true,
      hiddenSeries,
      defaultHiddenSeries,
      onHiddenSeriesChange,
      label,
      xAxisLabel,
      yAxisLabel,
      legendLabel = 'Toggle series',
      scrubLabel = 'Chart cursor',
      scrubHint = 'Use the arrow keys to move between samples.',
      emptyLabel = 'No data',
      unobservedLabel = 'not observed',
      tableCaption = 'Chart data',
      accessibleTable = true,
      tableRowLimit = 200,
      onActiveChange,
      className,
      ...rest
    },
    ref,
  ) {
    const uid = useId();
    const plotRef = useRef<HTMLDivElement | null>(null);

    const [hidden, setHidden] = useControllableState<string[]>({
      value: hiddenSeries,
      defaultValue: defaultHiddenSeries ?? [],
      onChange: onHiddenSeriesChange,
    });

    const [activeIndex, setActiveIndex] = useState<number | null>(null);
    const [keyboardMode, setKeyboardMode] = useState(false);

    const pad: TimeSeriesPadding = { ...DEFAULT_PADDING, ...padding };
    const plotW = Math.max(1, width - pad.left - pad.right);
    const plotH = Math.max(1, height - pad.top - pad.bottom);

    /* -- Prepare ---------------------------------------------------------- */

    const prepared = useMemo<PreparedSeries[]>(
      () =>
        series.map((s, i) => {
          const overrides: { dash?: string | 'none'; marker?: MarkerShape | 'none' } = {};
          if (s.dash !== undefined) overrides.dash = s.dash;
          if (s.marker !== undefined && s.marker !== 'none') overrides.marker = s.marker;
          const paint = resolveSeriesPaint(s.color, i, overrides);
          const byX = new Map<number, number | null>();
          for (const p of s.points) {
            byX.set(p.x, p.y == null || !Number.isFinite(p.y) ? null : p.y);
          }
          return {
            id: s.id,
            label: s.label ?? s.id,
            slot: paint.slot,
            custom: paint.custom,
            dash: paint.dash,
            marker: paint.marker,
            suppressMarker: s.marker === 'none',
            area: s.area ?? false,
            points: s.points,
            byX,
          };
        }),
      [series],
    );

    const visible = useMemo(
      () => prepared.filter((s) => !hidden.includes(s.id)),
      [prepared, hidden],
    );

    const model = useMemo(() => {
      let xLo = Infinity;
      let xHi = -Infinity;
      let yLo = Infinity;
      let yHi = -Infinity;
      let observed = 0;
      let maxPoints = 0;
      const lattice = new Set<number>();

      for (const s of visible) {
        if (s.points.length > maxPoints) maxPoints = s.points.length;
        for (const p of s.points) {
          if (!Number.isFinite(p.x)) continue;
          lattice.add(p.x);
          if (p.x < xLo) xLo = p.x;
          if (p.x > xHi) xHi = p.x;
          if (p.y == null || !Number.isFinite(p.y)) continue;
          observed += 1;
          if (p.y < yLo) yLo = p.y;
          if (p.y > yHi) yHi = p.y;
        }
      }

      if (includeZero) {
        yLo = Math.min(yLo, 0);
        yHi = Math.max(yHi, 0);
      }

      const hasData = observed > 0 && lattice.size > 0;

      const xd: [number, number] = xDomain ?? [
        Number.isFinite(xLo) ? xLo : 0,
        Number.isFinite(xHi) ? xHi : 1,
      ];
      let yd: [number, number];
      if (yDomain) {
        yd = yDomain;
      } else if (!hasData) {
        yd = [0, 1];
      } else if (yLo === yHi) {
        // A perfectly flat set has no range. Open a symmetric window around it
        // so the line sits in the middle instead of on an edge.
        const bump = Math.abs(yLo) > 0 ? Math.abs(yLo) * 0.1 : 1;
        yd = [yLo - bump, yHi + bump];
      } else {
        yd = [yLo, yHi];
      }

      const yTicksRaw = niceTicks(yd[0], yd[1], yTickCount);
      const first = yTicksRaw[0];
      const lastTick = yTicksRaw[yTicksRaw.length - 1];
      const yStart = yDomain ? yd[0] : (first ?? yd[0]);
      const yEnd = yDomain ? yd[1] : (lastTick ?? yd[1]);

      const xSpan = xd[1] - xd[0];
      const ySpan = yEnd - yStart;

      const xAt = (v: number): number =>
        xSpan === 0 ? pad.left + plotW / 2 : pad.left + ((v - xd[0]) / xSpan) * plotW;
      const yAt = (v: number): number =>
        ySpan === 0 ? pad.top + plotH / 2 : pad.top + plotH - ((v - yStart) / ySpan) * plotH;

      const xs = Array.from(lattice).sort((a, b) => a - b);

      // The default tick format follows the window: a five-minute chart wants
      // seconds, a fortnight wants dates.
      const tickText =
        formatX ??
        (xScale === 'time'
          ? axisTimeFormatter(xSpan, locale)
          : (v: number) => formatCount(v));

      const xTicks = !hasData
        ? []
        : xSpan === 0
          ? xs.slice(0, 1)
          : xScale === 'time'
            ? niceTimeTicks(xd[0], xd[1], xTickCount)
            : niceTicks(xd[0], xd[1], xTickCount);

      // Two filters, both about legibility rather than data:
      //  - ticks whose labels would collide are dropped, since an axis with
      //    overlapping text carries less than one with half as many ticks;
      //  - a tick whose label repeats the previous one is dropped too. Coarse
      //    formatters ("2 hrs ago") collapse neighbouring instants, and three
      //    identical labels in a row read as a rendering fault.
      const keptXTicks: { v: number; label: string }[] = [];
      let lastX = -Infinity;
      let lastLabel: string | null = null;
      for (const t of xTicks) {
        if (t < xd[0] || t > xd[1]) continue;
        const px = xAt(t);
        if (px - lastX < minXTickSpacing) continue;
        const text = tickText(t);
        if (text === lastLabel) continue;
        keptXTicks.push({ v: t, label: text });
        lastX = px;
        lastLabel = text;
      }

      const yTicks = !hasData
        ? []
        : (yDomain ? niceTicks(yStart, yEnd, yTickCount) : yTicksRaw).filter(
            (t) => t >= yStart - 1e-9 && t <= yEnd + 1e-9,
          );

      const shapes = visible.map((s) => {
        const pts: (Pt | null)[] = s.points.map((p) =>
          p.y == null || !Number.isFinite(p.y) || !Number.isFinite(p.x)
            ? null
            : { x: xAt(p.x), y: yAt(p.y) },
        );
        const segments = toSegments(pts);
        return {
          meta: s,
          runs: segments.filter((seg) => seg.length >= 2),
          islands: segments.filter((seg) => seg.length === 1).map((seg) => seg[0]!),
          allPoints: pts,
        };
      });

      return {
        hasData,
        observed,
        maxPoints,
        xs,
        xDomainResolved: xd,
        yStart,
        yEnd,
        xAt,
        yAt,
        xTicks: keptXTicks,
        yTicks,
        shapes,
      };
    }, [
      visible,
      xDomain,
      yDomain,
      includeZero,
      xScale,
      formatX,
      locale,
      xTickCount,
      yTickCount,
      minXTickSpacing,
      pad.left,
      pad.top,
      plotW,
      plotH,
    ]);

    const showMarkers =
      markers === true || (markers === 'auto' && model.maxPoints > 0 && model.maxPoints <= markerThreshold);

    /* -- Crosshair -------------------------------------------------------- */

    const activeX = activeIndex != null ? model.xs[activeIndex] : undefined;

    // The last index handed to `onActiveChange`. Kept in a ref so the notify
    // happens in the event handler rather than inside a state updater —
    // updaters must stay pure or React's double-invoke fires the callback
    // twice in development.
    const notifiedRef = useRef<number | null>(null);
    const emitActive = useEventCallback(onActiveChange);

    const setActive = useCallback(
      (next: number | null, fromKeyboard: boolean) => {
        setKeyboardMode(fromKeyboard);
        setActiveIndex(next);
        if (notifiedRef.current !== next) {
          notifiedRef.current = next;
          emitActive(next == null ? null : (model.xs[next] ?? null));
        }
      },
      [model.xs, emitActive],
    );

    const nearestIndex = useCallback(
      (clientX: number): number | null => {
        const node = plotRef.current;
        if (!node || model.xs.length === 0) return null;
        const rect = node.getBoundingClientRect();
        if (rect.width === 0) return null;
        const vx = ((clientX - rect.left) / rect.width) * width;
        let best = 0;
        let bestDist = Infinity;
        for (let i = 0; i < model.xs.length; i += 1) {
          const xv = model.xs[i];
          if (xv === undefined) continue;
          const d = Math.abs(model.xAt(xv) - vx);
          if (d < bestDist) {
            bestDist = d;
            best = i;
          }
        }
        return best;
      },
      [model, width],
    );

    const handlePointerMove = useCallback(
      (event: ReactPointerEvent<HTMLDivElement>) => {
        const i = nearestIndex(event.clientX);
        if (i != null) setActive(i, false);
      },
      [nearestIndex, setActive],
    );

    const handleKeyDown = useCallback(
      (event: ReactKeyboardEvent<HTMLDivElement>) => {
        const n = model.xs.length;
        if (n === 0) return;
        const page = Math.max(1, Math.round(n / 10));
        const cur = activeIndex ?? 0;
        let next: number | null = null;

        switch (event.key) {
          case 'ArrowRight':
          case 'ArrowUp':
            next = activeIndex == null ? 0 : clamp(cur + 1, 0, n - 1);
            break;
          case 'ArrowLeft':
          case 'ArrowDown':
            next = activeIndex == null ? n - 1 : clamp(cur - 1, 0, n - 1);
            break;
          case 'Home':
            next = 0;
            break;
          case 'End':
            next = n - 1;
            break;
          case 'PageUp':
            next = clamp(cur + page, 0, n - 1);
            break;
          case 'PageDown':
            next = clamp(cur - page, 0, n - 1);
            break;
          case 'Escape':
            if (activeIndex == null) return;
            event.preventDefault();
            setActive(null, true);
            return;
          default:
            return;
        }

        event.preventDefault();
        setActive(next, true);
      },
      [activeIndex, model.xs.length, setActive],
    );

    const readout = useMemo(() => {
      if (activeX === undefined) return '';
      const rows = visible.map((s) => {
        const v = s.byX.has(activeX) ? s.byX.get(activeX) : undefined;
        return `${s.label} ${v == null ? unobservedLabel : formatValue(v)}`;
      });
      return `${formatXLong(activeX)}. ${rows.join('. ')}`;
    }, [activeX, visible, formatValue, formatXLong, unobservedLabel]);

    /* -- Text alternative -------------------------------------------------- */

    const summary = useMemo(() => {
      if (!model.hasData) return label ? `${label}. ${emptyLabel}` : emptyLabel;
      const head = label ? `${label}. ` : '';
      const range =
        model.xs.length > 0
          ? `${formatXLong(model.xs[0]!)} to ${formatXLong(model.xs[model.xs.length - 1]!)}`
          : '';
      const bits = visible.map((s) => {
        let lo = Infinity;
        let hi = -Infinity;
        let latest: number | null | undefined;
        let gaps = 0;
        for (const p of s.points) {
          if (p.y == null || !Number.isFinite(p.y)) {
            gaps += 1;
            continue;
          }
          if (p.y < lo) lo = p.y;
          if (p.y > hi) hi = p.y;
          latest = p.y;
        }
        const parts = [`${s.label}`];
        if (Number.isFinite(lo)) parts.push(`low ${formatValue(lo)}`);
        if (Number.isFinite(hi)) parts.push(`high ${formatValue(hi)}`);
        if (latest != null) parts.push(`latest ${formatValue(latest)}`);
        if (gaps > 0) parts.push(`${gaps} ${unobservedLabel}`);
        return parts.join(', ');
      });
      const hiddenCount = prepared.length - visible.length;
      return (
        `${head}Line chart, ${visible.length} series` +
        (hiddenCount > 0 ? `, ${hiddenCount} hidden` : '') +
        (range ? `, ${range}` : '') +
        `. ${bits.join('. ')}`
      );
    }, [model.hasData, model.xs, label, emptyLabel, visible, prepared.length, formatValue, formatXLong, unobservedLabel]);

    const tableRows = useMemo<ChartTableRow[]>(() => {
      if (!accessibleTable || !model.hasData) return [];
      const xs = model.xs.slice(0, tableRowLimit);
      return xs.map((x) => ({
        key: `x${x}`,
        header: formatXLong(x),
        cells: visible.map((s) => {
          const v = s.byX.has(x) ? s.byX.get(x) : undefined;
          return v == null ? unobservedLabel : formatValue(v);
        }),
      }));
    }, [accessibleTable, model.hasData, model.xs, tableRowLimit, visible, formatValue, formatXLong, unobservedLabel]);

    const truncated = model.xs.length > tableRowLimit;

    /* -- Render ------------------------------------------------------------ */

    const crosshairX = activeX !== undefined ? model.xAt(activeX) : null;
    const tooltipSide = crosshairX != null && crosshairX > pad.left + plotW / 2 ? 'start' : 'end';

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="time-series-chart"
        data-empty={!model.hasData || undefined}
        className={clsx('stratum-time-series', className)}
      >
        <div className="stratum-time-series__plot" ref={plotRef}>
          <svg
            className="stratum-time-series__svg"
            viewBox={`0 0 ${width} ${height}`}
            width={width}
            height={height}
            role="img"
            aria-label={summary}
            focusable="false"
          >
            {/* -- Grid. Solid hairlines, one step off the surface. --------- */}
            <g className="stratum-time-series__grid" aria-hidden="true">
              {(grid === 'y' || grid === 'both') &&
                model.yTicks.map((t) => (
                  <line
                    key={`gy${t}`}
                    x1={pad.left}
                    x2={pad.left + plotW}
                    y1={model.yAt(t)}
                    y2={model.yAt(t)}
                    stroke="currentColor"
                    vectorEffect="non-scaling-stroke"
                  />
                ))}
              {(grid === 'x' || grid === 'both') &&
                model.xTicks.map((t) => (
                  <line
                    key={`gx${t.v}`}
                    x1={model.xAt(t.v)}
                    x2={model.xAt(t.v)}
                    y1={pad.top}
                    y2={pad.top + plotH}
                    stroke="currentColor"
                    vectorEffect="non-scaling-stroke"
                  />
                ))}
            </g>

            {/* -- Axis rules ------------------------------------------------ */}
            <g className="stratum-time-series__axis" aria-hidden="true">
              <line
                x1={pad.left}
                x2={pad.left + plotW}
                y1={pad.top + plotH}
                y2={pad.top + plotH}
                stroke="currentColor"
                vectorEffect="non-scaling-stroke"
              />
            </g>

            {/* -- Series ---------------------------------------------------- */}
            {model.shapes.map((s) => (
              <g
                key={s.meta.id}
                className="stratum-time-series__series"
                data-series={s.meta.slot}
                {...(s.meta.custom ? { style: { color: s.meta.custom } } : {})}
                aria-hidden="true"
              >
                {s.meta.area &&
                  s.runs.map((seg, j) => (
                    <path
                      key={`a${j}`}
                      className="stratum-time-series__area"
                      d={areaPath(seg, pad.top + plotH)}
                      fill="currentColor"
                    />
                  ))}

                {s.runs.map((seg, j) => (
                  <path
                    key={`l${j}`}
                    className="stratum-time-series__line"
                    d={linePath(seg)}
                    fill="none"
                    stroke="currentColor"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    {...(s.meta.dash ? { strokeDasharray: s.meta.dash } : {})}
                    // Keeps line weight identical whatever the container does
                    // to the viewBox — without it a chart in a narrow column
                    // draws visibly thinner lines than the same chart wide.
                    vectorEffect="non-scaling-stroke"
                  />
                ))}

                {/* Samples with no neighbours still get drawn. */}
                {s.islands.map((p, j) => (
                  <path
                    key={`i${j}`}
                    className="stratum-time-series__island"
                    d={markerPath(s.meta.marker, p.x, p.y, 2.6)}
                    fill="currentColor"
                  />
                ))}

                {showMarkers &&
                  !s.meta.suppressMarker &&
                  s.allPoints.map((p, j) =>
                    p ? (
                      <path
                        key={`m${j}`}
                        className="stratum-time-series__marker"
                        d={markerPath(s.meta.marker, p.x, p.y, 2.6)}
                        fill="currentColor"
                        vectorEffect="non-scaling-stroke"
                      />
                    ) : null,
                  )}
              </g>
            ))}

            {/* -- Crosshair -------------------------------------------------- */}
            {crosshairX != null && (
              <g className="stratum-time-series__crosshair" aria-hidden="true">
                <line
                  x1={crosshairX}
                  x2={crosshairX}
                  y1={pad.top}
                  y2={pad.top + plotH}
                  stroke="currentColor"
                  vectorEffect="non-scaling-stroke"
                />
                {visible.map((s) => {
                  const v = activeX !== undefined && s.byX.has(activeX) ? s.byX.get(activeX) : undefined;
                  if (v == null) return null;
                  return (
                    <g
                      key={`c${s.id}`}
                      className="stratum-time-series__series"
                      data-series={s.slot}
                      {...(s.custom ? { style: { color: s.custom } } : {})}
                    >
                      <path
                        className="stratum-time-series__hit"
                        d={markerPath(s.marker, crosshairX, model.yAt(v), 3.6)}
                        fill="currentColor"
                      />
                    </g>
                  );
                })}
              </g>
            )}

            {/* -- Axis labels ------------------------------------------------ */}
            <g className="stratum-time-series__ticks" aria-hidden="true">
              {model.yTicks.map((t) => (
                <text key={`ty${t}`} x={pad.left - 6} y={model.yAt(t)} textAnchor="end" dominantBaseline="middle">
                  {formatY(t)}
                </text>
              ))}
              {model.xTicks.map((t) => (
                <text
                  key={`tx${t.v}`}
                  x={model.xAt(t.v)}
                  y={pad.top + plotH + 14}
                  textAnchor="middle"
                  dominantBaseline="middle"
                >
                  {t.label}
                </text>
              ))}
            </g>
          </svg>

          {/* The keyboard + pointer surface. One tab stop, not one per point:
              a 200-sample chart with 200 tab stops is unusable, and a chart
              with none is inaccessible. */}
          <div
            className="stratum-time-series__surface stratum-focus-inset"
            role="group"
            tabIndex={0}
            aria-label={scrubLabel}
            aria-describedby={`${uid}-readout`}
            onPointerMove={handlePointerMove}
            onPointerLeave={() => setActive(null, false)}
            onKeyDown={handleKeyDown}
            onBlur={() => {
              if (keyboardMode) setActive(null, true);
            }}
            style={{
              insetInlineStart: `${(pad.left / width) * 100}%`,
              insetInlineEnd: `${(pad.right / width) * 100}%`,
              insetBlockStart: `${(pad.top / height) * 100}%`,
              insetBlockEnd: `${(pad.bottom / height) * 100}%`,
            }}
          />

          {crosshairX != null && activeX !== undefined && (
            <div
              className="stratum-time-series__tooltip"
              data-side={tooltipSide}
              // The same values reach assistive technology through the live
              // region below; announcing them twice is worse than once.
              aria-hidden="true"
              style={{ insetInlineStart: `${(crosshairX / width) * 100}%` }}
            >
              <div className="stratum-time-series__tooltip-head">{formatXLong(activeX)}</div>
              <dl className="stratum-time-series__tooltip-rows">
                {visible.map((s) => {
                  const has = s.byX.has(activeX);
                  const v = has ? s.byX.get(activeX) : undefined;
                  return (
                    <div
                      key={s.id}
                      className="stratum-time-series__tooltip-row"
                      data-unobserved={v == null || undefined}
                    >
                      <dt>
                        <span
                          className="stratum-time-series__swatch"
                          data-series={s.slot}
                          {...(s.custom ? { style: { color: s.custom } } : {})}
                          aria-hidden="true"
                        >
                          <svg viewBox="0 0 14 10" width="14" height="10" focusable="false">
                            <line
                              x1={0}
                              x2={14}
                              y1={5}
                              y2={5}
                              stroke="currentColor"
                              strokeWidth={2}
                              {...(s.dash ? { strokeDasharray: s.dash } : {})}
                            />
                            <path d={markerPath(s.marker, 7, 5, 2.6)} fill="currentColor" />
                          </svg>
                        </span>
                        {s.label}
                      </dt>
                      <dd>{v == null ? unobservedLabel : formatValue(v)}</dd>
                    </div>
                  );
                })}
              </dl>
            </div>
          )}

          {!model.hasData && <p className="stratum-time-series__empty">{emptyLabel}</p>}
        </div>

        {(xAxisLabel || yAxisLabel) && (
          <div className="stratum-time-series__axis-labels" aria-hidden="true">
            {yAxisLabel && <span data-axis="y">{yAxisLabel}</span>}
            {xAxisLabel && <span data-axis="x">{xAxisLabel}</span>}
          </div>
        )}

        {showLegend && prepared.length > 1 && (
          <ul className="stratum-time-series__legend" aria-label={legendLabel}>
            {prepared.map((s) => {
              const off = hidden.includes(s.id);
              return (
                <li key={s.id}>
                  <button
                    type="button"
                    className="stratum-time-series__legend-item"
                    aria-pressed={!off}
                    data-off={off || undefined}
                    onClick={() =>
                      setHidden(
                        off ? hidden.filter((h) => h !== s.id) : [...hidden, s.id],
                      )
                    }
                  >
                    <span
                      className="stratum-time-series__swatch"
                      data-series={s.slot}
                      {...(s.custom ? { style: { color: s.custom } } : {})}
                      aria-hidden="true"
                    >
                      <svg viewBox="0 0 14 10" width="14" height="10" focusable="false">
                        <line
                          x1={0}
                          x2={14}
                          y1={5}
                          y2={5}
                          stroke="currentColor"
                          strokeWidth={2}
                          {...(s.dash ? { strokeDasharray: s.dash } : {})}
                        />
                        <path d={markerPath(s.marker, 7, 5, 2.6)} fill="currentColor" />
                      </svg>
                    </span>
                    {s.label}
                  </button>
                </li>
              );
            })}
          </ul>
        )}

        <div id={`${uid}-readout`} className="stratum-visually-hidden" aria-live="polite">
          {keyboardMode && readout ? readout : scrubHint}
        </div>

        {accessibleTable && model.hasData && (
          <ChartTable
            caption={label ? `${label} — ${tableCaption}` : tableCaption}
            rowHeader={xAxisLabel ?? 'Time'}
            columns={visible.map((s) => s.label)}
            rows={tableRows}
            {...(truncated
              ? {
                  note: `Showing the first ${tableRowLimit} of ${model.xs.length} samples.`,
                }
              : {})}
          />
        )}
      </div>
    );
  },
);

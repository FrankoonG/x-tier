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
import { UNOBSERVED, formatCount, latencyStatus } from '../../network/format';
import { ChartTable, clamp, type ChartTableRow } from '../chart';
import './Heatmap.css';

export interface HeatmapRow {
  id: string;
  label?: string;
}

export interface HeatmapColumn {
  id: string;
  label?: string;
}

/** Health bands, used by `scale='status'`. */
export type HeatmapStatus = 'ok' | 'degraded' | 'failed';

export interface HeatmapProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'color'> {
  rows: HeatmapRow[];
  columns: HeatmapColumn[];
  /**
   * Value at a cell. Return `null` or `undefined` for NOT OBSERVED — that is
   * rendered as an empty hatched cell, never as the bottom of the scale.
   *
   * Called `rows.length * columns.length` times per render, so memoise it in
   * the consumer if the lookup is expensive.
   */
  value: (rowId: string, columnId: string) => number | null | undefined;
  /**
   * `'sequential'` bins the value range into a single-hue ramp.
   * `'status'` bands it into ok / degraded / failed via `latencyStatus`, which
   * is the right choice for per-hop health where the thresholds mean something
   * and the magnitude does not.
   */
  scale?: 'sequential' | 'status';
  /** Fixes the sequential domain. Without it, the observed extent is used. */
  domain?: [number, number];
  /** Number of colour bins, 2-6. Past six, adjacent classes stop separating. */
  bins?: number;
  /** Upper bound of the "good" band in `status` mode, in ms. */
  good?: number;
  /** Upper bound of the "fair" band in `status` mode, in ms. */
  fair?: number;
  cellSize?: number;
  cellGap?: number;
  /** Width reserved for row labels, in viewBox units. */
  rowHeaderWidth?: number;
  /** Height reserved for column labels. Set 0 to hide them. */
  columnLabelHeight?: number;
  /** Render every Nth column label. `'auto'` fits them to the cell width. */
  columnLabelEvery?: number | 'auto';
  formatValue?: (value: number | null | undefined) => string;
  showLegend?: boolean;
  label?: string;
  legendLabel?: string;
  lowLabel?: string;
  highLabel?: string;
  /** Legend text for each band in `scale='status'`. */
  statusLabels?: Record<HeatmapStatus, string>;
  noDataLabel?: string;
  emptyLabel?: string;
  scrubLabel?: string;
  scrubHint?: string;
  tableCaption?: string;
  rowAxisLabel?: string;
  accessibleTable?: boolean;
  onCellClick?: (rowId: string, columnId: string) => void;
}

interface Cell {
  value: number | null;
  bin: number | null;
  status: HeatmapStatus | null;
}

const STATUS_ORDER: readonly HeatmapStatus[] = ['ok', 'degraded', 'failed'];

/**
 * A time × category grid.
 *
 * THE NO-DATA CELL IS THE POINT
 * -----------------------------
 * Every heatmap implementation that treats a missing value as the bottom of
 * its colour ramp is claiming a measurement it does not have. On a latency
 * grid that reads as "this hop was fast", when the truth is "this hop was
 * never probed" — the exact inversion of the fact, in the exact place an
 * operator is looking for an outage. So a missing cell here is drawn as an
 * empty, hatched, dashed-bordered tile: lighter than nothing on the ramp,
 * textured so it cannot be confused with any filled bin, and distinguishable
 * in grayscale, under colour-vision deficiency, and under `forced-colors`.
 *
 * A `null` also never enters the domain, so a run of unprobed cells cannot
 * drag the scale.
 *
 * KEYBOARD
 * --------
 * The grid is one tab stop with a two-dimensional cursor rather than one tab
 * stop per cell: a 24 × 60 grid would otherwise put 1,440 stops between the
 * chart and the next control. Arrow keys move the cursor, `Home`/`End` jump to
 * the ends of a row, `Escape` releases it, and every move is announced through
 * a polite live region. The full grid is also available as a table.
 */
export const Heatmap = forwardRef<HTMLDivElement, HeatmapProps>(function Heatmap(
  {
    rows,
    columns,
    value,
    scale = 'sequential',
    domain,
    bins = 5,
    good,
    fair,
    cellSize = 14,
    cellGap = 2,
    rowHeaderWidth = 104,
    columnLabelHeight = 16,
    columnLabelEvery = 'auto',
    formatValue = (v) => (v == null || !Number.isFinite(v) ? UNOBSERVED : formatCount(v)),
    showLegend = true,
    label,
    legendLabel = 'Value scale',
    lowLabel = 'Low',
    highLabel = 'High',
    statusLabels = { ok: 'OK', degraded: 'Degraded', failed: 'Failed' },
    noDataLabel = 'not observed',
    emptyLabel = 'No data',
    scrubLabel = 'Grid cursor',
    scrubHint = 'Use the arrow keys to move between cells.',
    tableCaption = 'Grid data',
    rowAxisLabel = 'Row',
    accessibleTable = true,
    onCellClick,
    className,
    ...rest
  },
  ref,
) {
  const uid = useId();
  const gridRef = useRef<HTMLDivElement | null>(null);
  const [cursor, setCursor] = useState<{ r: number; c: number } | null>(null);
  const [keyboardMode, setKeyboardMode] = useState(false);

  const binCount = clamp(Math.round(bins), 2, 6);
  const step = cellSize + cellGap;

  const grid = useMemo(() => {
    let lo = Infinity;
    let hi = -Infinity;
    let observed = 0;

    const raw: (number | null)[][] = rows.map((row) =>
      columns.map((col) => {
        const v = value(row.id, col.id);
        if (v == null || !Number.isFinite(v)) return null;
        observed += 1;
        if (v < lo) lo = v;
        if (v > hi) hi = v;
        return v;
      }),
    );

    const dLo = domain ? domain[0] : Number.isFinite(lo) ? lo : 0;
    const dHi = domain ? domain[1] : Number.isFinite(hi) ? hi : 1;
    const span = dHi - dLo;

    const cells: Cell[][] = raw.map((r) =>
      r.map((v) => {
        if (v == null) return { value: null, bin: null, status: null };
        if (scale === 'status') {
          const s = latencyStatus(v, {
            ...(good !== undefined ? { good } : {}),
            ...(fair !== undefined ? { fair } : {}),
          });
          const mapped: HeatmapStatus = s === 'ok' ? 'ok' : s === 'degraded' ? 'degraded' : 'failed';
          return { value: v, bin: null, status: mapped };
        }
        // A degenerate span means every observed cell holds the same value.
        // Painting them all at the top of the ramp would imply a spread that
        // does not exist, so they land mid-scale instead.
        const t = span === 0 ? 0.5 : (v - dLo) / span;
        return {
          value: v,
          bin: clamp(Math.floor(t * binCount), 0, binCount - 1),
          status: null,
        };
      }),
    );

    // Bin edges, for the legend. Only meaningful for the sequential scale.
    const edges: number[] = [];
    for (let i = 0; i <= binCount; i += 1) edges.push(dLo + (span * i) / binCount);

    return { cells, observed, domainLo: dLo, domainHi: dHi, edges };
  }, [rows, columns, value, scale, domain, binCount, good, fair]);

  const labelEvery = useMemo(() => {
    if (columnLabelEvery !== 'auto') return Math.max(1, Math.round(columnLabelEvery));
    // Roughly 44 units of text per label at the micro type size.
    return Math.max(1, Math.ceil(44 / Math.max(1, step)));
  }, [columnLabelEvery, step]);

  const svgWidth = rowHeaderWidth + columns.length * step;
  const svgHeight = columnLabelHeight + rows.length * step;
  const hasData = grid.observed > 0 && rows.length > 0 && columns.length > 0;

  /* -- Cursor ------------------------------------------------------------- */

  const moveCursor = useCallback(
    (next: { r: number; c: number } | null, fromKeyboard: boolean) => {
      setKeyboardMode(fromKeyboard);
      setCursor(next);
    },
    [],
  );

  const cellFromPointer = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>): { r: number; c: number } | null => {
      const node = gridRef.current;
      if (!node) return null;
      const rect = node.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) return null;
      const vx = ((event.clientX - rect.left) / rect.width) * svgWidth;
      const vy = ((event.clientY - rect.top) / rect.height) * svgHeight;
      const c = Math.floor((vx - rowHeaderWidth) / step);
      const r = Math.floor((vy - columnLabelHeight) / step);
      if (c < 0 || r < 0 || c >= columns.length || r >= rows.length) return null;
      return { r, c };
    },
    [svgWidth, svgHeight, rowHeaderWidth, columnLabelHeight, step, columns.length, rows.length],
  );

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (rows.length === 0 || columns.length === 0) return;
      const cur = cursor ?? { r: 0, c: 0 };
      let next: { r: number; c: number } | null = null;

      switch (event.key) {
        case 'ArrowRight':
          next = cursor ? { r: cur.r, c: clamp(cur.c + 1, 0, columns.length - 1) } : cur;
          break;
        case 'ArrowLeft':
          next = cursor ? { r: cur.r, c: clamp(cur.c - 1, 0, columns.length - 1) } : cur;
          break;
        case 'ArrowDown':
          next = cursor ? { r: clamp(cur.r + 1, 0, rows.length - 1), c: cur.c } : cur;
          break;
        case 'ArrowUp':
          next = cursor ? { r: clamp(cur.r - 1, 0, rows.length - 1), c: cur.c } : cur;
          break;
        case 'Home':
          next = { r: cur.r, c: 0 };
          break;
        case 'End':
          next = { r: cur.r, c: columns.length - 1 };
          break;
        case 'Enter':
        case ' ': {
          if (!cursor || !onCellClick) return;
          const row = rows[cursor.r];
          const col = columns[cursor.c];
          if (!row || !col) return;
          event.preventDefault();
          onCellClick(row.id, col.id);
          return;
        }
        case 'Escape':
          if (!cursor) return;
          event.preventDefault();
          moveCursor(null, true);
          return;
        default:
          return;
      }

      event.preventDefault();
      moveCursor(next, true);
    },
    [cursor, rows, columns, moveCursor, onCellClick],
  );

  const active = useMemo(() => {
    if (!cursor) return null;
    const row = rows[cursor.r];
    const col = columns[cursor.c];
    const cell = grid.cells[cursor.r]?.[cursor.c];
    if (!row || !col || !cell) return null;
    return {
      row,
      col,
      cell,
      x: rowHeaderWidth + cursor.c * step,
      y: columnLabelHeight + cursor.r * step,
    };
  }, [cursor, rows, columns, grid.cells, rowHeaderWidth, columnLabelHeight, step]);

  const readout = active
    ? `${active.row.label ?? active.row.id}, ${active.col.label ?? active.col.id}: ${
        active.cell.value == null ? noDataLabel : formatValue(active.cell.value)
      }`
    : '';

  /* -- Text alternative ---------------------------------------------------- */

  const summary = useMemo(() => {
    const head = label ? `${label}. ` : '';
    if (!hasData) return `${head}${emptyLabel}`;
    const total = rows.length * columns.length;
    const missing = total - grid.observed;
    const range =
      scale === 'sequential'
        ? `, values ${formatValue(grid.domainLo)} to ${formatValue(grid.domainHi)}`
        : '';
    return (
      `${head}Heat map, ${rows.length} rows by ${columns.length} columns${range}` +
      (missing > 0 ? `, ${missing} cells ${noDataLabel}` : '') +
      '.'
    );
  }, [label, hasData, emptyLabel, rows.length, columns.length, grid.observed, grid.domainLo, grid.domainHi, scale, formatValue, noDataLabel]);

  const tableRows = useMemo<ChartTableRow[]>(() => {
    if (!accessibleTable || !hasData) return [];
    return rows.map((row, r) => ({
      key: row.id,
      header: row.label ?? row.id,
      cells: columns.map((_col, c) => {
        const cell = grid.cells[r]?.[c];
        return cell == null || cell.value == null ? noDataLabel : formatValue(cell.value);
      }),
    }));
  }, [accessibleTable, hasData, rows, columns, grid.cells, formatValue, noDataLabel]);

  /* -- Render -------------------------------------------------------------- */

  const hatchId = `${uid}-hatch`;

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="heatmap"
      data-scale={scale}
      data-empty={!hasData || undefined}
      className={clsx('stratum-heatmap', className)}
    >
      <div className="stratum-heatmap__scroll">
        <div
          className="stratum-heatmap__grid stratum-focus-inset"
          ref={gridRef}
          role="group"
          tabIndex={0}
          aria-label={scrubLabel}
          aria-describedby={`${uid}-readout`}
          onKeyDown={handleKeyDown}
          onPointerMove={(e) => {
            const next = cellFromPointer(e);
            moveCursor(next, false);
          }}
          onPointerLeave={() => moveCursor(null, false)}
          onClick={() => {
            if (!active || !onCellClick) return;
            onCellClick(active.row.id, active.col.id);
          }}
          style={{ width: svgWidth }}
        >
          <svg
            className="stratum-heatmap__svg"
            viewBox={`0 0 ${svgWidth} ${svgHeight}`}
            width={svgWidth}
            height={svgHeight}
            role="img"
            aria-label={summary}
            focusable="false"
          >
            <defs>
              {/* The no-data texture. Diagonal at 45 degrees, never horizontal
                  or vertical — those read as gridlines. */}
              <pattern
                id={hatchId}
                className="stratum-heatmap__hatch-pattern"
                width={4}
                height={4}
                patternUnits="userSpaceOnUse"
                patternTransform="rotate(45)"
              >
                {/* `currentColor` here resolves against the pattern element's
                    own computed colour, not the referencing cell's — which is
                    what we want: the hatch is always the unknown role. */}
                <line x1={0} y1={0} x2={0} y2={4} stroke="currentColor" strokeWidth={1} />
              </pattern>
            </defs>

            {columnLabelHeight > 0 && (
              <g className="stratum-heatmap__col-labels" aria-hidden="true">
                {columns.map((col, c) =>
                  c % labelEvery === 0 ? (
                    <text
                      key={col.id}
                      x={rowHeaderWidth + c * step + cellSize / 2}
                      y={columnLabelHeight - 5}
                      textAnchor="middle"
                    >
                      {col.label ?? col.id}
                    </text>
                  ) : null,
                )}
              </g>
            )}

            <g className="stratum-heatmap__row-labels" aria-hidden="true">
              {rows.map((row, r) => (
                <text
                  key={row.id}
                  x={rowHeaderWidth - 8}
                  y={columnLabelHeight + r * step + cellSize / 2}
                  textAnchor="end"
                  dominantBaseline="middle"
                >
                  {row.label ?? row.id}
                </text>
              ))}
            </g>

            <g aria-hidden="true">
              {rows.map((row, r) =>
                columns.map((col, c) => {
                  const cell = grid.cells[r]?.[c];
                  if (!cell) return null;
                  const x = rowHeaderWidth + c * step;
                  const y = columnLabelHeight + r * step;
                  const missing = cell.value == null;

                  return (
                    <g
                      key={`${row.id}|${col.id}`}
                      className="stratum-heatmap__cell"
                      data-missing={missing || undefined}
                      {...(cell.bin != null ? { 'data-bin': cell.bin + 1 } : {})}
                      {...(cell.status ? { 'data-status': cell.status } : {})}
                    >
                      <rect
                        className="stratum-heatmap__fill"
                        x={x}
                        y={y}
                        width={cellSize}
                        height={cellSize}
                        rx={1}
                        fill="currentColor"
                      />
                      {missing && (
                        <>
                          <rect
                            className="stratum-heatmap__hatch"
                            x={x}
                            y={y}
                            width={cellSize}
                            height={cellSize}
                            rx={1}
                            fill={`url(#${hatchId})`}
                          />
                          <rect
                            className="stratum-heatmap__missing-edge"
                            x={x + 0.5}
                            y={y + 0.5}
                            width={cellSize - 1}
                            height={cellSize - 1}
                            rx={1}
                            fill="none"
                            stroke="currentColor"
                            strokeDasharray="2 2"
                            vectorEffect="non-scaling-stroke"
                          />
                        </>
                      )}
                    </g>
                  );
                }),
              )}
            </g>

            {active && (
              <rect
                className="stratum-heatmap__cursor"
                x={active.x - 1}
                y={active.y - 1}
                width={cellSize + 2}
                height={cellSize + 2}
                rx={2}
                fill="none"
                stroke="currentColor"
                vectorEffect="non-scaling-stroke"
                aria-hidden="true"
              />
            )}
          </svg>

          {active && (
            <div
              className="stratum-heatmap__tooltip"
              aria-hidden="true"
              data-side={active.x > svgWidth / 2 ? 'start' : 'end'}
              // Anchored to the grid's top or bottom edge — whichever is away
              // from the cursor — rather than floating beside the cell. A grid
              // this dense lives in a horizontal scroller, and a scroller
              // clips: a tooltip that tracked the cell vertically escaped the
              // box on the top rows and produced a stray scrollbar. Pinning it
              // to an edge keeps it inside the box at every cursor position
              // and stops it covering the row being read.
              data-vside={cursor && cursor.r < rows.length / 2 ? 'bottom' : 'top'}
              style={{ insetInlineStart: `${((active.x + cellSize / 2) / svgWidth) * 100}%` }}
            >
              <span className="stratum-heatmap__tooltip-row">
                {active.row.label ?? active.row.id}
              </span>
              <span className="stratum-heatmap__tooltip-col">
                {active.col.label ?? active.col.id}
              </span>
              <span
                className="stratum-heatmap__tooltip-value"
                data-unobserved={active.cell.value == null || undefined}
              >
                {active.cell.value == null ? noDataLabel : formatValue(active.cell.value)}
              </span>
            </div>
          )}
        </div>
      </div>

      {!hasData && <p className="stratum-heatmap__empty">{emptyLabel}</p>}

      {showLegend && hasData && (
        <div className="stratum-heatmap__legend" role="group" aria-label={legendLabel}>
          {scale === 'sequential' ? (
            <>
              <span className="stratum-heatmap__legend-end">{lowLabel}</span>
              <span className="stratum-heatmap__legend-ramp">
                {Array.from({ length: binCount }, (_, i) => (
                  <span
                    key={i}
                    className="stratum-heatmap__legend-swatch"
                    data-bin={i + 1}
                    title={`${formatValue(grid.edges[i] ?? null)} – ${formatValue(
                      grid.edges[i + 1] ?? null,
                    )}`}
                  />
                ))}
              </span>
              <span className="stratum-heatmap__legend-end">{highLabel}</span>
            </>
          ) : (
            /* Status bands are named, not just coloured: the three health
               hues are exactly the pairing that colour-vision deficiency
               collapses, so the legend has to say which is which. */
            STATUS_ORDER.map((s) => (
              <span key={s} className="stratum-heatmap__legend-band">
                <span className="stratum-heatmap__legend-swatch" data-status={s} />
                {statusLabels[s]}
              </span>
            ))
          )}

          {/* The no-data key sits apart from the ramp, not at its end — it is
              not a low value, it is the absence of one. */}
          <span className="stratum-heatmap__legend-missing">
            <span className="stratum-heatmap__legend-swatch" data-missing="" />
            {noDataLabel}
          </span>
        </div>
      )}

      <div id={`${uid}-readout`} className="stratum-visually-hidden" aria-live="polite">
        {keyboardMode && readout ? readout : scrubHint}
      </div>

      {accessibleTable && hasData && (
        <ChartTable
          caption={label ? `${label} — ${tableCaption}` : tableCaption}
          rowHeader={rowAxisLabel}
          columns={columns.map((c) => c.label ?? c.id)}
          rows={tableRows}
        />
      )}
    </div>
  );
});

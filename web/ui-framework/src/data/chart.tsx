/* ---------------------------------------------------------------------------
 * Shared chart primitives.
 *
 * The data layer draws its own SVG — there is no charting dependency — so the
 * geometry, the palette-slot resolution and the text alternative all live here
 * once rather than five times.
 *
 * THREE CONVENTIONS RUN THROUGH EVERYTHING BELOW
 *
 *  1. NULL MEANS UNOBSERVED. A gap in a series is a gap. Nothing here ever
 *     interpolates across one, substitutes zero, or drops the sample silently.
 *     `toSegments` exists precisely so a line breaks instead of inventing the
 *     value it was never given.
 *
 *  2. COLOUR IS NEVER THE ONLY CHANNEL. Every series slot carries a dash
 *     pattern and a marker shape alongside its hue, both derived from the same
 *     slot index, so two series stay separable for a colour-vision-deficient
 *     reader, in grayscale print, and under `forced-colors`.
 *
 *  3. SVG PAINT RESOLVES THROUGH `currentColor`. Dark-mode browser extensions
 *     read literal declarations and mangle `fill: var(--x)`; they substitute
 *     `color` reliably. So components set `color` in CSS on a wrapping group
 *     and every shape paints with `currentColor`. Same reasoning as
 *     `tokens.mesh.css`, applied to charts.
 * ------------------------------------------------------------------------- */

import type { ReactElement } from 'react';

/** Number of distinct data-series slots the token set defines. */
export const SERIES_SLOTS = 8;

/**
 * A series colour: a 1-based slot into `--stratum-series-N`, or any CSS colour
 * for the case where the colour belongs to the entity rather than the chart.
 */
export type SeriesColor = number | string;

/** Fillable marker shapes, one per series slot. All are closed paths. */
export type MarkerShape =
  | 'circle'
  | 'square'
  | 'triangle'
  | 'diamond'
  | 'wedge'
  | 'hexagon'
  | 'pentagon'
  | 'cross';

/**
 * Dash pattern per slot. Slot 1 is solid so a single-series chart is never
 * gratuitously dashed; the patterns then diverge in period as well as in
 * duty cycle, because two dashes of the same period read as the same line at
 * a glance regardless of gap size.
 */
export const SERIES_DASH: readonly string[] = [
  '',
  '5 3',
  '2 2',
  '9 3 2 3',
  '12 4',
  '1 3',
  '7 2 1 2 1 2',
  '3 3 10 3',
];

/** Marker shape per slot, paired with {@link SERIES_DASH} by index. */
export const SERIES_MARKER: readonly MarkerShape[] = [
  'circle',
  'square',
  'triangle',
  'diamond',
  'wedge',
  'hexagon',
  'pentagon',
  'cross',
];

export interface SeriesPaint {
  /** 1-based slot; published as `data-series` and mapped to a token in CSS. */
  slot: number;
  /** Set when the caller supplied a literal colour instead of a slot. */
  custom: string | undefined;
  dash: string;
  marker: MarkerShape;
}

/**
 * Resolves a series' visual identity.
 *
 * The index passed in must be the series' position in the ORIGINAL, unfiltered
 * list. Deriving it from a filtered list is the "recolour on filter" bug: hide
 * one series and every survivor changes colour, so a reader who learned
 * "relay-b is teal" is now being lied to.
 */
export function resolveSeriesPaint(
  color: SeriesColor | undefined,
  index: number,
  overrides?: { dash?: string | 'none'; marker?: MarkerShape | 'none' },
): SeriesPaint {
  const raw = typeof color === 'number' ? color - 1 : index;
  const slotIndex = ((raw % SERIES_SLOTS) + SERIES_SLOTS) % SERIES_SLOTS;
  const dash = overrides?.dash === 'none' ? '' : (overrides?.dash ?? SERIES_DASH[slotIndex] ?? '');
  const marker =
    overrides?.marker === 'none'
      ? 'circle'
      : (overrides?.marker ?? SERIES_MARKER[slotIndex] ?? 'circle');

  return {
    slot: slotIndex + 1,
    custom: typeof color === 'string' ? color : undefined,
    dash,
    marker,
  };
}

/* -- Geometry --------------------------------------------------------------- */

export interface Pt {
  x: number;
  y: number;
}

const round = (n: number): number => Math.round(n * 100) / 100;

/**
 * Splits a point list into contiguous runs, breaking at every hole.
 *
 * This is the whole reason charts in this library do not use a charting
 * library: the popular defaults connect across a `null`, which draws a
 * straight line through time the data never claimed anything about. A reader
 * cannot distinguish that invented segment from a measured one.
 */
export function toSegments(points: readonly (Pt | null)[]): Pt[][] {
  const out: Pt[][] = [];
  let run: Pt[] = [];
  for (const p of points) {
    if (p == null || !Number.isFinite(p.x) || !Number.isFinite(p.y)) {
      if (run.length > 0) out.push(run);
      run = [];
      continue;
    }
    run.push(p);
  }
  if (run.length > 0) out.push(run);
  return out;
}

/** Polyline through a run of points. */
export function linePath(segment: readonly Pt[]): string {
  if (segment.length === 0) return '';
  let d = '';
  for (let i = 0; i < segment.length; i += 1) {
    const p = segment[i];
    if (!p) continue;
    d += `${i === 0 ? 'M' : 'L'}${round(p.x)},${round(p.y)}`;
  }
  return d;
}

/** Closed area between a run of points and a horizontal baseline. */
export function areaPath(segment: readonly Pt[], baselineY: number): string {
  if (segment.length < 2) return '';
  const first = segment[0];
  const last = segment[segment.length - 1];
  if (!first || !last) return '';
  return `${linePath(segment)}L${round(last.x)},${round(baselineY)}L${round(first.x)},${round(
    baselineY,
  )}Z`;
}

function polygonPath(cx: number, cy: number, r: number, sides: number, rotation: number): string {
  let d = '';
  for (let i = 0; i < sides; i += 1) {
    const a = rotation + (i * 2 * Math.PI) / sides;
    const x = round(cx + r * Math.cos(a));
    const y = round(cy + r * Math.sin(a));
    d += `${i === 0 ? 'M' : 'L'}${x},${y}`;
  }
  return `${d}Z`;
}

/**
 * A closed path for one marker, centred on `cx,cy` with nominal radius `r`.
 *
 * Every shape is fillable — no stroke-only glyphs — so a marker can be painted
 * with one `fill` and ringed with one `stroke` in the surface colour, which is
 * what keeps it legible where two series cross.
 */
export function markerPath(shape: MarkerShape, cx: number, cy: number, r: number): string {
  const up = -Math.PI / 2;
  switch (shape) {
    case 'square': {
      const a = r * 0.88;
      return `M${round(cx - a)},${round(cy - a)}H${round(cx + a)}V${round(cy + a)}H${round(
        cx - a,
      )}Z`;
    }
    case 'triangle':
      return polygonPath(cx, cy, r * 1.2, 3, up);
    case 'wedge':
      return polygonPath(cx, cy, r * 1.2, 3, -up);
    case 'diamond':
      return polygonPath(cx, cy, r * 1.28, 4, up);
    case 'pentagon':
      return polygonPath(cx, cy, r * 1.15, 5, up);
    case 'hexagon':
      return polygonPath(cx, cy, r * 1.12, 6, up);
    case 'cross': {
      // A fillable plus: twelve points, arm half-width `w`.
      const a = r * 1.3;
      const w = r * 0.45;
      return (
        `M${round(cx - w)},${round(cy - a)}H${round(cx + w)}V${round(cy - w)}` +
        `H${round(cx + a)}V${round(cy + w)}H${round(cx + w)}V${round(cy + a)}` +
        `H${round(cx - w)}V${round(cy + w)}H${round(cx - a)}V${round(cy - w)}` +
        `H${round(cx - w)}Z`
      );
    }
    case 'circle':
    default:
      return (
        `M${round(cx - r)},${round(cy)}` +
        `a${round(r)},${round(r)} 0 1,0 ${round(r * 2)},0` +
        `a${round(r)},${round(r)} 0 1,0 ${round(-r * 2)},0Z`
      );
  }
}

/** Point on a circle. 0 degrees is twelve o'clock; angles increase clockwise. */
export function polar(cx: number, cy: number, r: number, angleDeg: number): Pt {
  const a = ((angleDeg - 90) * Math.PI) / 180;
  return { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
}

/** Stroked arc between two angles. Returns `''` for a degenerate sweep. */
export function arcPath(
  cx: number,
  cy: number,
  r: number,
  fromDeg: number,
  toDeg: number,
): string {
  if (!Number.isFinite(fromDeg) || !Number.isFinite(toDeg)) return '';
  if (Math.abs(toDeg - fromDeg) < 0.01) return '';
  const p0 = polar(cx, cy, r, fromDeg);
  const p1 = polar(cx, cy, r, toDeg);
  const large = Math.abs(toDeg - fromDeg) > 180 ? 1 : 0;
  const sweep = toDeg > fromDeg ? 1 : 0;
  return `M${round(p0.x)},${round(p0.y)}A${round(r)},${round(r)} 0 ${large},${sweep} ${round(
    p1.x,
  )},${round(p1.y)}`;
}

/* -- Scales ----------------------------------------------------------------- */

export function clamp(v: number, lo: number, hi: number): number {
  return v < lo ? lo : v > hi ? hi : v;
}

/**
 * Extent of the finite values in a list. Returns `null` when nothing was
 * observed — deliberately not `[0, 0]`, which would let an empty chart claim a
 * measured zero.
 */
export function extent(values: Iterable<number | null | undefined>): [number, number] | null {
  let lo = Infinity;
  let hi = -Infinity;
  let seen = false;
  for (const v of values) {
    if (v == null || !Number.isFinite(v)) continue;
    seen = true;
    if (v < lo) lo = v;
    if (v > hi) hi = v;
  }
  return seen ? [lo, hi] : null;
}

function niceNum(range: number, roundIt: boolean): number {
  if (range <= 0) return 1;
  const exp = Math.floor(Math.log10(range));
  const f = range / 10 ** exp;
  let nf: number;
  if (roundIt) nf = f < 1.5 ? 1 : f < 3 ? 2 : f < 7 ? 5 : 10;
  else nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nf * 10 ** exp;
}

/**
 * Human-readable tick values covering `[min, max]`.
 *
 * Ticks land on 1/2/5 multiples so the axis reads 0 / 500 / 1,000 rather than
 * 0 / 437 / 874 — the axis carries every value the chart did not directly
 * label, so it has to be readable at a glance.
 */
export function niceTicks(min: number, max: number, count = 5): number[] {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return [];
  if (min === max) return [min];
  if (max < min) return niceTicks(max, min, count);

  const step = niceNum(niceNum(max - min, false) / Math.max(1, count - 1), true);
  if (!Number.isFinite(step) || step <= 0) return [min, max];

  const start = Math.floor(min / step) * step;
  const end = Math.ceil(max / step) * step;
  const out: number[] = [];
  const guard = 512;
  for (let i = 0; i <= guard; i += 1) {
    const v = start + i * step;
    if (v > end + step * 0.5) break;
    // Re-rounding kills the float drift that otherwise produces 0.30000000000000004.
    out.push(Number(v.toPrecision(12)));
  }
  return out;
}

/**
 * Human time steps, ascending: milliseconds through a year.
 *
 * Time does not decimalise, which is why a generic 1/2/5 tick algorithm is the
 * wrong tool for a time axis. Applied to epoch milliseconds it produces a step
 * like 500,000 ms and lands ticks at 8 minutes 20 seconds past nothing in
 * particular; the axis then reads "1 hr ago, 39 mins ago, 31 mins ago", which
 * is both ugly and hard to reason about. These steps are the intervals people
 * actually think in.
 */
const TIME_STEPS_MS: readonly number[] = [
  1, 5, 10, 25, 50, 100, 250, 500,
  1_000, 2_000, 5_000, 10_000, 15_000, 30_000,
  60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000,
  3_600_000, 7_200_000, 10_800_000, 21_600_000, 43_200_000,
  86_400_000, 172_800_000, 604_800_000, 1_209_600_000,
  2_592_000_000, 7_776_000_000, 15_552_000_000, 31_536_000_000,
];

/**
 * Tick positions for a time axis, snapped to a human interval and aligned to
 * that interval's boundary.
 *
 * Alignment is to the UTC epoch grid, not to local midnight: honouring a local
 * calendar would mean owning a timezone, and a component library that guesses
 * one is worse than one that is predictable. For sub-day steps — which is
 * essentially every operator chart — the two are identical anyway.
 */
export function niceTimeTicks(min: number, max: number, count = 5): number[] {
  if (!Number.isFinite(min) || !Number.isFinite(max) || max <= min) return [];
  const target = (max - min) / Math.max(1, count);
  let step = TIME_STEPS_MS[TIME_STEPS_MS.length - 1] ?? 1;
  for (const s of TIME_STEPS_MS) {
    if (s >= target) {
      step = s;
      break;
    }
  }
  const out: number[] = [];
  const start = Math.ceil(min / step) * step;
  for (let i = 0; i < 512; i += 1) {
    const v = start + i * step;
    if (v > max) break;
    out.push(v);
  }
  return out;
}

/**
 * A tick formatter chosen for the span being plotted.
 *
 * A time axis needs a resolution that matches its window: seconds are noise
 * across a week and a date is useless across five minutes. `formatRelativeTime`
 * is tempting as a default and is wrong here — it rounds to whole units, so a
 * half-hour window formats three consecutive ticks as "1 hr ago" and the axis
 * loses its scale entirely. Relative time belongs in a status cell, not on an
 * axis. The precise instant is still one hover away in the tooltip, which uses
 * `formatTimestamp`.
 */
export function axisTimeFormatter(spanMs: number, locale?: string): (value: number) => string {
  const span = Number.isFinite(spanMs) ? Math.abs(spanMs) : 0;
  const opts: Intl.DateTimeFormatOptions =
    span < 300_000
      ? { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }
      : span < 86_400_000
        ? { hour: '2-digit', minute: '2-digit', hour12: false }
        : span < 31_536_000_000
          ? { month: 'short', day: 'numeric' }
          : { year: 'numeric', month: 'short' };
  const fmt = new Intl.DateTimeFormat(locale, opts);
  return (value: number) => (Number.isFinite(value) ? fmt.format(value) : '');
}

/* -- Text alternative -------------------------------------------------------- */

export interface ChartTableRow {
  key: string;
  /** Rendered as a row header when present. */
  header?: string;
  cells: string[];
}

export interface ChartTableProps {
  id?: string;
  caption: string;
  /** Column headers, excluding the row-header column. */
  columns: string[];
  rows: ChartTableRow[];
  /** Header for the row-header column. Omit when rows carry no header. */
  rowHeader?: string;
  /** Appended as a footer row — used to declare truncation. */
  note?: string;
}

/**
 * The visually-hidden data table that backs every chart.
 *
 * A chart's `aria-label` can only ever be a summary; a screen-reader user who
 * needs the third sample of the second series has no way to get it from prose.
 * The table is the WCAG-clean twin of the picture, and it is on by default
 * because an opt-in accessibility feature is one nobody opts into.
 *
 * It sits OUTSIDE the `<svg role="img">`, since assistive technology treats an
 * `img` as opaque and would never reach a table nested inside it.
 */
export function ChartTable({
  id,
  caption,
  columns,
  rows,
  rowHeader,
  note,
}: ChartTableProps): ReactElement {
  const span = columns.length + (rowHeader != null ? 1 : 0);
  return (
    <table id={id} className="stratum-visually-hidden" data-stratum="chart-table">
      <caption>{caption}</caption>
      <thead>
        <tr>
          {rowHeader != null && <th scope="col">{rowHeader}</th>}
          {columns.map((c, i) => (
            <th key={`${c}-${i}`} scope="col">
              {c}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            {r.header != null && <th scope="row">{r.header}</th>}
            {r.cells.map((c, i) => (
              <td key={i}>{c}</td>
            ))}
          </tr>
        ))}
      </tbody>
      {note != null && (
        <tfoot>
          <tr>
            <td colSpan={span}>{note}</td>
          </tr>
        </tfoot>
      )}
    </table>
  );
}

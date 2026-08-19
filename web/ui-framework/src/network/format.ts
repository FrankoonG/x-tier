/* ---------------------------------------------------------------------------
 * Formatting helpers for network quantities.
 *
 * These are pure functions so they can be used in table sorting, exports, chart
 * axes and tooltips — not only in the display components that wrap them.
 *
 * Two conventions run through all of them:
 *
 *  1. FIXED WIDTH. Every formatter produces a predictable number of significant
 *     digits so a live-updating column does not change width on each poll.
 *     Combined with tabular figures, a rate column stays perfectly still.
 *
 *  2. NULL MEANS UNOBSERVED. `null`/`undefined` never formats as `0`. A missing
 *     measurement and a measured zero are different facts and conflating them
 *     is how a panel ends up claiming a link is idle when it was never probed.
 * ------------------------------------------------------------------------- */

/** Rendered where a value has not been observed. */
export const UNOBSERVED = '—';

const BINARY_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'] as const;
const DECIMAL_UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB', 'EB'] as const;
const RATE_UNITS = ['bps', 'kbps', 'Mbps', 'Gbps', 'Tbps'] as const;

export interface ByteFormatOptions {
  /**
   * `'binary'` uses 1024 and KiB/MiB (correct for memory and file sizes);
   * `'decimal'` uses 1000 and kB/MB (correct for link rates and disk vendors).
   * Default `'binary'`.
   */
  base?: 'binary' | 'decimal';
  /** Significant digits after the decimal point. Default: 1 below 100, else 0. */
  precision?: number;
  /** Separator between number and unit. Default: a narrow no-break space. */
  space?: string;
}

function scale(value: number, step: number, units: readonly string[]) {
  let v = Math.abs(value);
  let i = 0;
  while (v >= step && i < units.length - 1) {
    v /= step;
    i += 1;
  }
  return { v: value < 0 ? -v : v, unit: units[i] ?? units[units.length - 1]! };
}

/** Digits chosen so the numeric part stays 1-4 glyphs wide at every scale. */
function autoPrecision(v: number): number {
  const a = Math.abs(v);
  if (a >= 100) return 0;
  if (a >= 10) return 1;
  return a >= 1 ? 2 : 1;
}

/**
 * Formats a byte count. `formatBytes(1536)` -> `'1.50 KiB'`.
 */
export function formatBytes(
  bytes: number | null | undefined,
  { base = 'binary', precision, space = ' ' }: ByteFormatOptions = {},
): string {
  if (bytes == null || !Number.isFinite(bytes)) return UNOBSERVED;
  if (bytes === 0) return `0${space}B`;

  const step = base === 'binary' ? 1024 : 1000;
  const units = base === 'binary' ? BINARY_UNITS : DECIMAL_UNITS;
  const { v, unit } = scale(bytes, step, units);
  const p = precision ?? (unit === 'B' ? 0 : autoPrecision(v));
  return `${v.toFixed(p)}${space}${unit}`;
}

/**
 * Formats a throughput given in BYTES per second, as bits per second — the
 * convention every network tool uses for link rate. `formatRate(125_000)` ->
 * `'1.00 Mbps'`.
 */
export function formatRate(
  bytesPerSecond: number | null | undefined,
  { precision, space = ' ' }: { precision?: number; space?: string } = {},
): string {
  if (bytesPerSecond == null || !Number.isFinite(bytesPerSecond)) return UNOBSERVED;
  if (bytesPerSecond === 0) return `0${space}bps`;

  const { v, unit } = scale(bytesPerSecond * 8, 1000, RATE_UNITS);
  const p = precision ?? (unit === 'bps' ? 0 : autoPrecision(v));
  return `${v.toFixed(p)}${space}${unit}`;
}

/**
 * Formats a duration in milliseconds. Sub-second values keep millisecond
 * resolution; longer ones switch to compact clock notation.
 *
 * `formatDuration(18.4)` -> `'18.4 ms'`, `formatDuration(3_671_000)` -> `'1h 1m'`.
 */
export function formatDuration(
  ms: number | null | undefined,
  { space = ' ' }: { space?: string } = {},
): string {
  if (ms == null || !Number.isFinite(ms)) return UNOBSERVED;

  const abs = Math.abs(ms);
  if (abs < 1) return `${ms.toFixed(2)}${space}ms`;
  if (abs < 1000) return `${ms.toFixed(abs < 100 ? 1 : 0)}${space}ms`;

  const s = ms / 1000;
  if (Math.abs(s) < 60) return `${s.toFixed(s < 10 ? 2 : 1)}${space}s`;

  const totalSeconds = Math.round(s);
  const d = Math.floor(totalSeconds / 86_400);
  const h = Math.floor((totalSeconds % 86_400) / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const sec = totalSeconds % 60;

  // Two units is the readable maximum; more turns a status cell into a puzzle.
  if (d > 0) return `${d}d${space}${h}h`;
  if (h > 0) return `${h}h${space}${m}m`;
  return `${m}m${space}${sec}s`;
}

/**
 * Compact relative time: `'12s ago'`, `'in 4m'`.
 *
 * Uses `Intl.RelativeTimeFormat` so it localises, and picks the largest unit
 * that keeps the number under its next threshold.
 */
export function formatRelativeTime(
  target: Date | number | string | null | undefined,
  now: Date | number = Date.now(),
  locale?: string,
): string {
  if (target == null) return UNOBSERVED;
  const t = target instanceof Date ? target.getTime() : new Date(target).getTime();
  if (!Number.isFinite(t)) return UNOBSERVED;

  const n = now instanceof Date ? now.getTime() : now;
  const deltaSeconds = Math.round((t - n) / 1000);
  const abs = Math.abs(deltaSeconds);

  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'narrow' });
  if (abs < 45) return rtf.format(deltaSeconds, 'second');
  if (abs < 2700) return rtf.format(Math.round(deltaSeconds / 60), 'minute');
  if (abs < 79_200) return rtf.format(Math.round(deltaSeconds / 3600), 'hour');
  if (abs < 2_592_000) return rtf.format(Math.round(deltaSeconds / 86_400), 'day');
  if (abs < 31_536_000) return rtf.format(Math.round(deltaSeconds / 2_592_000), 'month');
  return rtf.format(Math.round(deltaSeconds / 31_536_000), 'year');
}

/** Absolute timestamp, `Intl`-localised. */
export function formatTimestamp(
  value: Date | number | string | null | undefined,
  {
    locale,
    precision = 'second',
    timeZone,
  }: { locale?: string; precision?: 'minute' | 'second' | 'millisecond'; timeZone?: string } = {},
): string {
  if (value == null) return UNOBSERVED;
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return UNOBSERVED;

  const base: Intl.DateTimeFormatOptions = {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    ...(timeZone ? { timeZone } : {}),
  };
  if (precision !== 'minute') base.second = '2-digit';
  if (precision === 'millisecond') base.fractionalSecondDigits = 3;

  return new Intl.DateTimeFormat(locale, base).format(d);
}

/** Percentage with fixed width. `formatPercent(0.4231)` -> `'42.3%'`. */
export function formatPercent(
  ratio: number | null | undefined,
  { precision = 1, alreadyPercent = false }: { precision?: number; alreadyPercent?: boolean } = {},
): string {
  if (ratio == null || !Number.isFinite(ratio)) return UNOBSERVED;
  const pct = alreadyPercent ? ratio : ratio * 100;
  return `${pct.toFixed(precision)}%`;
}

/** Thousands-separated integer. */
export function formatCount(
  n: number | null | undefined,
  locale?: string,
): string {
  if (n == null || !Number.isFinite(n)) return UNOBSERVED;
  return new Intl.NumberFormat(locale).format(n);
}

/**
 * Shortens a long opaque identifier — public key, fingerprint, node id — to
 * `head…tail`, keeping enough on each end to be recognisable.
 *
 * The ends are what a human compares; the middle carries no scanning value.
 */
export function truncateId(id: string | null | undefined, head = 6, tail = 4): string {
  if (!id) return UNOBSERVED;
  if (id.length <= head + tail + 1) return id;
  return `${id.slice(0, head)}…${id.slice(-tail)}`;
}

/**
 * Classifies a latency in milliseconds into a status band.
 *
 * Thresholds are deliberately parameterised: what counts as slow differs
 * hugely between a LAN peer and an intercontinental relay, so a single global
 * constant would mislabel one of them.
 */
export function latencyStatus(
  ms: number | null | undefined,
  { good = 60, fair = 180 }: { good?: number; fair?: number } = {},
): 'ok' | 'degraded' | 'failed' | 'unknown' {
  if (ms == null || !Number.isFinite(ms)) return 'unknown';
  // A negative reading is the convention for "probe failed", not "very fast".
  if (ms < 0) return 'failed';
  if (ms <= good) return 'ok';
  if (ms <= fair) return 'degraded';
  return 'failed';
}

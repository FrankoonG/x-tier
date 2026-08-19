import { forwardRef, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import {
  UNOBSERVED,
  formatBytes,
  formatCount,
  formatDuration,
  formatPercent,
  formatRate,
  formatRelativeTime,
  formatTimestamp,
  latencyStatus,
  truncateId,
} from '../format';
import './Metric.css';

type Span = HTMLAttributes<HTMLSpanElement>;

export interface MetricBaseProps extends Omit<Span, 'children'> {
  size?: 'sm' | 'md';
  /** Dims the value. For secondary or historical readings. */
  muted?: boolean;
}

/* -------------------------------------------------------------------------- */

export interface ByteSizeProps extends MetricBaseProps {
  bytes: number | null | undefined;
  base?: 'binary' | 'decimal';
  precision?: number;
}

/** A byte count. `null` renders as "not observed", never as zero. */
export const ByteSize = forwardRef<HTMLSpanElement, ByteSizeProps>(function ByteSize(
  { bytes, base, precision, size = 'md', muted, className, ...rest },
  ref,
) {
  const text = formatBytes(bytes, { base, ...(precision !== undefined ? { precision } : {}) });
  return (
    <span
      // Before the spread: an unobserved value leaves this undefined, and
      // after the spread that would delete a consumer's own `title` rather
      // than fall back to it.
      title={bytes != null ? `${bytes} bytes` : undefined}
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="bytes"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={bytes == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {text}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface RateProps extends MetricBaseProps {
  /** Throughput in BYTES per second. Displayed as bits per second. */
  bytesPerSecond: number | null | undefined;
  /** Draws a direction marker. */
  direction?: 'in' | 'out';
  precision?: number;
}

/**
 * A throughput reading.
 *
 * Input is bytes/s because that is what transports report; output is bits/s
 * because that is what operators expect to read. Doing the conversion here,
 * once, prevents the unit confusion that otherwise shows up as an
 * eight-times-wrong number somewhere in every panel.
 */
export const Rate = forwardRef<HTMLSpanElement, RateProps>(function Rate(
  { bytesPerSecond, direction, precision, size = 'md', muted, className, ...rest },
  ref,
) {
  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="rate"
      data-direction={direction}
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={bytesPerSecond == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {direction && (
        <span className="stratum-metric__dir" aria-hidden="true">
          {direction === 'in' ? '↓' : '↑'}
        </span>
      )}
      {direction && (
        <span className="stratum-visually-hidden">
          {direction === 'in' ? 'inbound ' : 'outbound '}
        </span>
      )}
      {formatRate(bytesPerSecond, precision !== undefined ? { precision } : {})}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface DurationProps extends MetricBaseProps {
  ms: number | null | undefined;
}

/** An elapsed or expected duration. */
export const Duration = forwardRef<HTMLSpanElement, DurationProps>(function Duration(
  { ms, size = 'md', muted, className, ...rest },
  ref,
) {
  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="duration"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={ms == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {formatDuration(ms)}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface LatencyProps extends MetricBaseProps {
  ms: number | null | undefined;
  /** Upper bound of the "good" band, in ms. Default 60. */
  good?: number;
  /** Upper bound of the "fair" band, in ms. Default 180. */
  fair?: number;
  /** Renders a filled badge rather than bare coloured text. */
  variant?: 'text' | 'badge';
}

/**
 * A latency reading, banded into good / fair / poor.
 *
 * Thresholds are props, not constants: 60 ms is excellent for an
 * intercontinental hop and mediocre for a LAN peer, so a fixed global scale
 * would mislabel one of them. A negative value is treated as a failed probe —
 * the conventional encoding — not as an impossibly fast one.
 *
 * The band is also spelled out for assistive technology, so the colour is
 * never the only carrier.
 */
export const Latency = forwardRef<HTMLSpanElement, LatencyProps>(function Latency(
  { ms, good, fair, variant = 'text', size = 'md', muted, className, ...rest },
  ref,
) {
  const status = latencyStatus(ms, {
    ...(good !== undefined ? { good } : {}),
    ...(fair !== undefined ? { fair } : {}),
  });
  const band =
    status === 'ok' ? 'good' : status === 'degraded' ? 'fair' : status === 'failed' ? 'poor' : 'unknown';

  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="latency"
      data-status={status}
      data-variant={variant}
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={ms == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {ms != null && ms >= 0 ? formatDuration(ms) : ms != null ? 'failed' : UNOBSERVED}
      <span className="stratum-visually-hidden">, {band} latency</span>
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface TimestampProps extends MetricBaseProps {
  value: Date | number | string | null | undefined;
  /** `'relative'` shows "12s ago" with the absolute time in the tooltip. */
  display?: 'absolute' | 'relative';
  locale?: string;
  timeZone?: string;
  precision?: 'minute' | 'second' | 'millisecond';
}

/** A point in time, absolute or relative. */
export const Timestamp = forwardRef<HTMLSpanElement, TimestampProps>(function Timestamp(
  { value, display = 'absolute', locale, timeZone, precision, size = 'md', muted, className, ...rest },
  ref,
) {
  const absolute = formatTimestamp(value, {
    ...(locale !== undefined ? { locale } : {}),
    ...(timeZone !== undefined ? { timeZone } : {}),
    ...(precision !== undefined ? { precision } : {}),
  });
  const shown = display === 'relative' ? formatRelativeTime(value, Date.now(), locale) : absolute;
  const iso = value != null ? new Date(value as never).toISOString?.() : undefined;

  return (
    <span
      // Relative time is scannable but imprecise; the exact value stays one
      // hover away rather than being lost. Before the spread because the
      // absolute display leaves this undefined, and after the spread that
      // would delete a consumer's own `title` rather than fall back to it.
      title={display === 'relative' ? absolute : undefined}
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="timestamp"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={value == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {iso && !Number.isNaN(new Date(iso).getTime()) ? <time dateTime={iso}>{shown}</time> : shown}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface PercentProps extends MetricBaseProps {
  /** A ratio in 0..1 unless `alreadyPercent`. */
  value: number | null | undefined;
  precision?: number;
  alreadyPercent?: boolean;
}

export const Percent = forwardRef<HTMLSpanElement, PercentProps>(function Percent(
  { value, precision, alreadyPercent, size = 'md', muted, className, ...rest },
  ref,
) {
  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="percent"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={value == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {formatPercent(value, {
        ...(precision !== undefined ? { precision } : {}),
        ...(alreadyPercent !== undefined ? { alreadyPercent } : {}),
      })}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface CountProps extends MetricBaseProps {
  value: number | null | undefined;
  locale?: string;
}

export const Count = forwardRef<HTMLSpanElement, CountProps>(function Count(
  { value, locale, size = 'md', muted, className, ...rest },
  ref,
) {
  return (
    <span
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="count"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={value == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      {formatCount(value, locale)}
    </span>
  );
});

/* -------------------------------------------------------------------------- */

export interface IdentifierProps extends MetricBaseProps {
  value: string | null | undefined;
  /** Leading characters kept. Default 6. */
  head?: number;
  /** Trailing characters kept. Default 4. */
  tail?: number;
  /** Renders in full instead of truncating. */
  full?: boolean;
}

/**
 * An opaque identifier — key, fingerprint, node id.
 *
 * Truncated to `head…tail` because the ends are what a human actually compares
 * when checking two identifiers match; the middle carries no scanning value.
 * The complete value stays available via the tooltip and is exposed in full to
 * assistive technology, so nothing is actually hidden.
 */
export const Identifier = forwardRef<HTMLSpanElement, IdentifierProps>(function Identifier(
  { value, head, tail, full = false, size = 'md', muted, className, ...rest },
  ref,
) {
  const shown = full ? (value ?? UNOBSERVED) : truncateId(value, head, tail);
  return (
    <span
      // Before the spread: an unobserved identifier leaves this undefined, and
      // after the spread that would delete a consumer's own `title` rather
      // than fall back to it.
      title={value ?? undefined}
      {...rest}
      ref={ref}
      data-stratum="metric"
      data-kind="identifier"
      data-size={size}
      data-muted={muted || undefined}
      data-unobserved={value == null || undefined}
      className={clsx('stratum-metric', className)}
    >
      <span aria-hidden="true">{shown}</span>
      {value && <span className="stratum-visually-hidden">{value}</span>}
    </span>
  );
});

import { forwardRef, useMemo, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import { UNOBSERVED, formatCount } from '../../network/format';
import { ChartTable, arcPath, clamp, polar, type ChartTableRow } from '../chart';
import './Gauge.css';

/** Severity of a threshold band. `neutral` carries no judgement. */
export type GaugeBandStatus = 'ok' | 'warning' | 'danger' | 'info' | 'neutral';

export interface GaugeBand {
  /** Inclusive lower bound, in the gauge's own units. */
  from: number;
  /** Exclusive upper bound. */
  to: number;
  status?: GaugeBandStatus;
  label?: string;
}

export interface GaugeProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children' | 'color'> {
  /** `null` means NOT OBSERVED. The arc is omitted, never drawn at `min`. */
  value: number | null | undefined;
  min?: number;
  max?: number;
  /** Threshold bands drawn on an outer ring. Overlaps are drawn in order. */
  bands?: GaugeBand[];
  /** Total sweep in degrees, centred on twelve o'clock. 30-340. */
  sweep?: number;
  /** Rendered width in CSS pixels. Height follows from the sweep. */
  size?: number;
  /** Ring thickness in viewBox units, where the arc radius is 50. */
  thickness?: number;
  formatValue?: (value: number | null | undefined) => string;
  /** Formats the min/max end labels and band boundaries. */
  formatBound?: (value: number) => string;
  /** Draws the min and max values at the ends of the arc. */
  showBounds?: boolean;
  /** Draws a radial tick at each band boundary. */
  showTicks?: boolean;
  /** Caption under the value. */
  label?: string;
  /** Unit suffix rendered beside the value, e.g. `'ms'`. */
  unit?: ReactNode;
  /** Accessible name. Falls back to `label`. */
  accessibleLabel?: string;
  unobservedLabel?: string;
  tableCaption?: string;
  accessibleTable?: boolean;
}

const STATUS_RANK: Record<GaugeBandStatus, number> = {
  neutral: 0,
  info: 1,
  ok: 2,
  warning: 3,
  danger: 4,
};

/**
 * A radial meter with threshold bands.
 *
 * UNMEASURED IS NOT ZERO
 * ----------------------
 * Every gauge implementation that coerces a missing reading to its minimum
 * draws a needle resting confidently at the bottom of the dial. On a
 * utilisation meter that reads as "idle"; on a latency meter it reads as
 * "instant". Both are the most reassuring possible misreading of "we have no
 * data". Here a `null` removes the value arc entirely, dashes the track, and
 * prints the unobserved dash in the readout — so an unprobed meter is
 * instantly distinguishable from a meter reading zero, which still draws a
 * real (if tiny) arc.
 *
 * BANDS CARRY MORE THAN COLOUR
 * ----------------------------
 * The band ring is separate from the value ring, so a coloured band is never
 * hidden underneath the value it is supposed to qualify. Each boundary also
 * gets a radial tick, so the thresholds are locatable without relying on hue,
 * and the value's own band is named in the accessible text rather than being
 * left to the reader's colour perception.
 */
export const Gauge = forwardRef<HTMLDivElement, GaugeProps>(function Gauge(
  {
    value,
    min = 0,
    max = 100,
    bands,
    sweep = 240,
    size = 132,
    thickness = 11,
    formatValue = (v) => (v == null || !Number.isFinite(v) ? UNOBSERVED : formatCount(v)),
    formatBound = (v) => formatCount(v),
    showBounds = true,
    showTicks = true,
    label,
    unit,
    accessibleLabel,
    unobservedLabel = 'not observed',
    tableCaption = 'Meter reading',
    accessibleTable = true,
    className,
    ...rest
  },
  ref,
) {
  const geom = useMemo(() => {
    const R = 50;
    const s = clamp(sweep, 30, 340);
    const half = s / 2;
    const rad = (d: number) => (d * Math.PI) / 180;

    const maxX = half >= 90 ? R : R * Math.sin(rad(half));
    // Lowest point of the arc, in centred coordinates where -R is the top.
    const yMax = -R * Math.cos(rad(Math.min(half, 180)));

    const pad = thickness / 2 + 1;
    const vbWidth = 2 * maxX + pad * 2;
    const vbHeight = yMax + R + pad * 2;
    return {
      R,
      half,
      startAngle: -half,
      endAngle: half,
      cx: maxX + pad,
      cy: R + pad,
      vbWidth,
      vbHeight,
    };
  }, [sweep, thickness]);

  const model = useMemo(() => {
    const lo = Math.min(min, max);
    const hi = Math.max(min, max);
    const span = hi - lo;
    const observed = value != null && Number.isFinite(value);
    const v = observed ? clamp(value as number, lo, hi) : null;
    const clipped = observed && (value as number) !== v;

    const angleOf = (n: number): number =>
      span === 0
        ? geom.startAngle
        : geom.startAngle + ((clamp(n, lo, hi) - lo) / span) * (geom.endAngle - geom.startAngle);

    const list = (bands ?? []).filter((b) => Number.isFinite(b.from) && Number.isFinite(b.to));

    // The band a reading falls into. Where bands overlap, the most severe one
    // wins — under-reporting severity is the dangerous direction.
    let activeBand: GaugeBand | null = null;
    if (v != null) {
      for (const b of list) {
        const bLo = Math.min(b.from, b.to);
        const bHi = Math.max(b.from, b.to);
        if (v < bLo || v > bHi) continue;
        const rank = STATUS_RANK[b.status ?? 'neutral'];
        const best = activeBand ? STATUS_RANK[activeBand.status ?? 'neutral'] : -1;
        if (rank > best) activeBand = b;
      }
    }

    const boundaries = new Set<number>([lo, hi]);
    for (const b of list) {
      boundaries.add(clamp(Math.min(b.from, b.to), lo, hi));
      boundaries.add(clamp(Math.max(b.from, b.to), lo, hi));
    }

    return {
      lo,
      hi,
      span,
      observed,
      v,
      clipped,
      angleOf,
      bands: list,
      activeBand,
      ticks: Array.from(boundaries).sort((a, b) => a - b),
    };
  }, [value, min, max, bands, geom.startAngle, geom.endAngle]);

  const { cx, cy, R, startAngle, endAngle } = geom;
  const bandRadius = R + thickness * 0.62;
  // Angular gap between adjacent bands. The separation is negative space, not
  // a stroke drawn around each segment.
  const bandGap = 1.1;

  const summary = useMemo(() => {
    const name = accessibleLabel ?? label ?? 'Meter';
    if (!model.observed) return `${name}: ${unobservedLabel}.`;
    const band = model.activeBand
      ? `, ${model.activeBand.label ?? model.activeBand.status ?? 'band'}`
      : '';
    return `${name}: ${formatValue(model.v)} of ${formatBound(model.hi)}${band}.`;
  }, [accessibleLabel, label, model.observed, model.activeBand, model.v, model.hi, formatValue, formatBound, unobservedLabel]);

  const tableRows = useMemo<ChartTableRow[]>(() => {
    if (!accessibleTable) return [];
    const rows: ChartTableRow[] = [
      {
        key: 'value',
        header: label ?? 'Value',
        cells: [model.observed ? formatValue(model.v) : unobservedLabel],
      },
      { key: 'min', header: 'Minimum', cells: [formatBound(model.lo)] },
      { key: 'max', header: 'Maximum', cells: [formatBound(model.hi)] },
    ];
    model.bands.forEach((b, i) => {
      rows.push({
        key: `band${i}`,
        header: b.label ?? b.status ?? `Band ${i + 1}`,
        cells: [`${formatBound(b.from)} – ${formatBound(b.to)}`],
      });
    });
    return rows;
  }, [accessibleTable, label, model.observed, model.v, model.lo, model.hi, model.bands, formatValue, formatBound, unobservedLabel]);

  const valueAngle = model.v != null ? model.angleOf(model.v) : null;
  const height = Math.round((size * geom.vbHeight) / geom.vbWidth);

  const meterProps = model.observed
    ? ({
        role: 'meter',
        'aria-valuenow': model.v ?? undefined,
        'aria-valuemin': model.lo,
        'aria-valuemax': model.hi,
        'aria-valuetext': formatValue(model.v),
        'aria-label': summary,
      } as const)
    : ({ role: 'img', 'aria-label': summary } as const);

  return (
    <div
      {...rest}
      ref={ref}
      data-stratum="gauge"
      data-observed={model.observed || undefined}
      data-status={model.activeBand?.status ?? undefined}
      className={clsx('stratum-gauge', className)}
    >
      {/* The meter role lives on the dial, not the root: `meter` is a leaf
          role, so a data table nested inside it would be unreachable. */}
      <div className="stratum-gauge__dial" style={{ width: size, height }} {...meterProps}>
        <svg
          className="stratum-gauge__svg"
          viewBox={`0 0 ${geom.vbWidth} ${geom.vbHeight}`}
          width={size}
          height={height}
          aria-hidden="true"
          focusable="false"
        >
          {/* -- Track. Dashed when nothing was measured, so an unread meter
                 does not look like a meter reading its minimum. ------------ */}
          <path
            className="stratum-gauge__track"
            d={arcPath(cx, cy, R, startAngle, endAngle)}
            fill="none"
            stroke="currentColor"
            strokeWidth={thickness}
            // Butt caps and a thickness-relative dash: with round caps the
            // stroke's own end-caps overrun the gaps and the dashed track
            // renders solid, erasing the distinction this whole state exists
            // to make.
            strokeLinecap={model.observed ? 'round' : 'butt'}
            {...(model.observed
              ? {}
              : { strokeDasharray: `${(thickness * 0.55).toFixed(2)} ${(thickness * 0.7).toFixed(2)}` })}
          />

          {/* -- Threshold bands on their own outer ring, so the value arc
                 never covers the thresholds it is being judged against. ---- */}
          {model.bands.map((b, i) => {
            const a0 = model.angleOf(Math.min(b.from, b.to));
            const a1 = model.angleOf(Math.max(b.from, b.to));
            const inset = Math.min(bandGap, Math.max(0, (a1 - a0) / 4));
            return (
              <path
                key={`b${i}`}
                className="stratum-gauge__band"
                data-status={b.status ?? 'neutral'}
                d={arcPath(cx, cy, bandRadius, a0 + inset, a1 - inset)}
                fill="none"
                stroke="currentColor"
                strokeWidth={thickness * 0.28}
                strokeLinecap="butt"
              />
            );
          })}

          {/* -- Boundary ticks. The non-colour channel for the thresholds. -- */}
          {showTicks &&
            model.ticks.map((t) => {
              const a = model.angleOf(t);
              const p0 = polar(cx, cy, R - thickness / 2 - 1, a);
              const p1 = polar(cx, cy, R - thickness / 2 - 4, a);
              return (
                <line
                  key={`t${t}`}
                  className="stratum-gauge__tick"
                  x1={p0.x}
                  y1={p0.y}
                  x2={p1.x}
                  y2={p1.y}
                  stroke="currentColor"
                  vectorEffect="non-scaling-stroke"
                />
              );
            })}

          {/* -- Value arc. Absent entirely when unobserved. ----------------- */}
          {valueAngle != null && (
            <path
              className="stratum-gauge__value"
              data-status={model.activeBand?.status ?? 'accent'}
              d={arcPath(cx, cy, R, startAngle, valueAngle)}
              fill="none"
              stroke="currentColor"
              strokeWidth={thickness}
              strokeLinecap="round"
            />
          )}

          {/* -- Cap. A precise read of the value, and a shape that survives
                 forced colours where the arc fill does not. ---------------- */}
          {valueAngle != null && (
            <line
              className="stratum-gauge__cap"
              x1={polar(cx, cy, R - thickness / 2, valueAngle).x}
              y1={polar(cx, cy, R - thickness / 2, valueAngle).y}
              x2={polar(cx, cy, R + thickness / 2, valueAngle).x}
              y2={polar(cx, cy, R + thickness / 2, valueAngle).y}
              stroke="currentColor"
              strokeWidth={2}
              strokeLinecap="round"
              vectorEffect="non-scaling-stroke"
            />
          )}
        </svg>

        <div className="stratum-gauge__readout" aria-hidden="true">
          <span className="stratum-gauge__value-text" data-unobserved={!model.observed || undefined}>
            {model.observed ? formatValue(model.v) : UNOBSERVED}
            {unit != null && model.observed && (
              <span className="stratum-gauge__unit">{unit}</span>
            )}
          </span>
          {label != null && <span className="stratum-gauge__caption">{label}</span>}
        </div>
      </div>

      {showBounds && (
        <div className="stratum-gauge__bounds" aria-hidden="true">
          <span>{formatBound(model.lo)}</span>
          <span>{formatBound(model.hi)}</span>
        </div>
      )}

      {accessibleTable && (
        <ChartTable
          caption={label ? `${label} — ${tableCaption}` : tableCaption}
          rowHeader="Field"
          columns={['Value']}
          rows={tableRows}
          {...(model.clipped
            ? { note: 'The reading fell outside the meter range and is shown clamped.' }
            : {})}
        />
      )}
    </div>
  );
});

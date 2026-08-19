import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './StateMatrix.css';

/**
 * Status of one dimension.
 *
 * `unknown` is not a failure and not a fallback — it means "not observed".
 * Distinguishing it from a confirmed negative is the entire point of this
 * component; see the notes on {@link StateMatrix}.
 */
export type DimensionStatus = 'ok' | 'degraded' | 'failed' | 'unknown' | 'inactive' | 'info';

export interface StateDimension {
  key: string;
  /** Short axis name. Keep it to 1-2 words. */
  label: string;
  /**
   * Observed value. `null` or `undefined` means NOT OBSERVED and forces the
   * unobserved presentation regardless of `status`.
   */
  value?: ReactNode;
  status?: DimensionStatus;
  /** Longer explanation surfaced on hover and to assistive tech. */
  note?: string;
  /**
   * Marks a value the operator chose rather than one the system fell back to.
   * An explicitly configured relay is not a degradation and must not be
   * painted as one.
   */
  explicit?: boolean;
}

export interface StateMatrixProps extends Omit<HTMLAttributes<HTMLDListElement>, 'children'> {
  dimensions: StateDimension[];
  layout?: 'inline' | 'stack' | 'grid';
  size?: 'sm' | 'md';
  /** Text used where a dimension has not been observed. Default `'not observed'`. */
  unobservedLabel?: string;
  /** Accessible caption for the whole matrix. */
  label?: string;
}

/**
 * Displays several INDEPENDENT state axes without ever rolling them up.
 *
 * WHY THIS EXISTS
 * ---------------
 * The systems this framework targets have state that is genuinely
 * multi-dimensional: whether a peer's identity is verified, whether it is a
 * member of your group, whether you can currently reach it, whether it is
 * authorised to relay for you, and what the runtime is actually doing are five
 * separate facts. They are routinely collapsed into one "peer" object with one
 * status dot, and every one of those collapses eventually produces a bug where
 * the UI claims something the data never said — "offline" when the truth is
 * "authorised but not currently dialled", or "connected" when the truth is
 * "reachable but not permitted".
 *
 * So this component deliberately offers no aggregate. There is no overall
 * colour, no summary badge, no `status` prop for the whole matrix. If a caller
 * wants a headline they must decide the roll-up rule themselves and own it.
 *
 * OBSERVED VERSUS ABSENT
 * ----------------------
 * A capability that is not listed by a peer may be unavailable, unprobed,
 * irrelevant, or simply not something that peer reports. Those are not the
 * same as "off". A dimension with no value renders as *not observed* — muted,
 * dashed, and explicitly labelled — never as a red cross.
 *
 * EXPLICIT VERSUS FALLEN-BACK
 * ---------------------------
 * `explicit: true` marks a value the operator configured. A relay chosen on
 * purpose looks like a normal healthy value; only an unrequested downgrade is
 * drawn as degraded.
 */
export const StateMatrix = forwardRef<HTMLDListElement, StateMatrixProps>(function StateMatrix(
  {
    dimensions,
    layout = 'inline',
    size = 'md',
    unobservedLabel = 'not observed',
    label,
    className,
    ...rest
  },
  ref,
) {
  return (
    <dl
      // Before the spread: `label` is optional, so after it the `undefined`
      // would win the object literal and delete a consumer's own `aria-label`.
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="state-matrix"
      data-layout={layout}
      data-size={size}
      className={clsx('stratum-state-matrix', className)}
    >
      {dimensions.map((d) => {
        const observed = d.value !== null && d.value !== undefined && d.value !== '';
        const status: DimensionStatus = observed ? (d.status ?? 'info') : 'unknown';

        return (
          <div
            key={d.key}
            className="stratum-state-matrix__item"
            data-status={status}
            data-observed={observed || undefined}
            data-explicit={d.explicit || undefined}
            title={d.note}
          >
            <dt className="stratum-state-matrix__label">{d.label}</dt>
            <dd className="stratum-state-matrix__value">
              <span className="stratum-state-matrix__marker" aria-hidden="true" />
              <span className="stratum-state-matrix__text">
                {observed ? d.value : unobservedLabel}
              </span>
              {d.explicit && observed && (
                <span className="stratum-state-matrix__explicit" title="Explicitly configured">
                  <span className="stratum-visually-hidden">, explicitly configured</span>
                  <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
                    <path d="M6 1.5v9M1.5 6h9" transform="rotate(45 6 6)" />
                  </svg>
                </span>
              )}
              {d.note && <span className="stratum-visually-hidden">. {d.note}</span>}
            </dd>
          </div>
        );
      })}
    </dl>
  );
});

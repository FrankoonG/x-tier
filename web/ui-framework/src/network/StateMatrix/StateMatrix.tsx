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
   *
   * Pass a RAW value wherever one exists. This component decides
   * observed-vs-unobserved by inspecting what it is given, so a wrapper
   * element is always truthy — `<Count value={null} />` renders a dash while
   * the row it sits in claims an observation. Guard the element instead:
   * `x != null ? <Count value={x} /> : null`. The value text is already set in
   * the mono face with tabular figures, so wrapping a metric gains little; a
   * node is still right for something genuinely composite, like a `<Tag>`
   * naming a profile.
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
 * An element that will paint its own unobserved dash while the row it sits in
 * claims an observation.
 *
 * Detected from the element's OWN `value` prop rather than from its type: the
 * whole `Metric` family — and any consumer component following the same
 * convention — renders a dash when handed nothing. That is precise in both
 * directions, which matters because a warning that fires on correct code is
 * one people learn to scroll past:
 *
 *   `<Count value={null} />`            warns   — dash under an "observed" row
 *   `n != null ? <Count value={n}/> : null`  silent — the guard already did it
 *   `<Tag>relay</Tag>`                  silent  — no `value`, nothing to check
 */
function rendersUnobserved(value: unknown): boolean {
  if (typeof value !== 'object' || value === null) return false;
  const el = value as { $$typeof?: unknown; props?: Record<string, unknown> };
  if (el.$$typeof === undefined || el.props === null || typeof el.props !== 'object') return false;
  if (!('value' in el.props)) return false;
  const inner = el.props.value;
  return inner === null || inner === undefined || inner === '';
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

        /*
         * The one way to defeat this component's contract.
         *
         * "null means unobserved" can only be honoured on a value this
         * component can inspect. Hand it `<Count value={null} />` and the test
         * above sees a React element, marks the row OBSERVED, draws the solid
         * marker — and the `Count` inside independently prints the unobserved
         * dash. The row then asserts an observation next to a value that
         * denies one, which is precisely the collapse this component exists to
         * prevent.
         *
         * Pass the raw value: the CSS already sets the mono face and tabular
         * figures on `__text` (StateMatrix.css), so wrapping a metric buys
         * nothing and costs the contract.
         */
        if (import.meta.env?.DEV && rendersUnobserved(d.value)) {
          // eslint-disable-next-line no-console
          console.error(
            `[stratum] <StateMatrix> dimension "${d.key}" was given an element whose own `
              + '`value` is empty, so it will render "not observed" inside a row this '
              + 'component has already marked as observed — an element is always truthy. '
              + `Pass the raw value, or guard the element: {x != null ? <…value={x}/> : null}.`,
          );
        }
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
                  {/* A map pin — "an operator pinned this value here".
                    *
                    * Two earlier glyphs were rejected against the rendered
                    * pixels rather than the source. A plus rotated 45° is an
                    * ✕, so "relay ✕" read as a negation of the very thing the
                    * marker was affirming. A dot-plus-stem collapses into an
                    * arrow at 10px and reads as a link. The teardrop survives
                    * the size because its silhouette is asymmetric: nothing
                    * else in a status row is round on top and pointed at the
                    * bottom.
                    *
                    * `evenodd` punches the hole rather than filling it with a
                    * background colour, so the mark stays correct on any
                    * surface and under forced colours. */}
                  <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
                    <path
                      fill="currentColor"
                      fillRule="evenodd"
                      d="M6 11.2S2.3 7.4 2.3 4.7a3.7 3.7 0 1 1 7.4 0C9.7 7.4 6 11.2 6 11.2Zm0-5.1a1.4 1.4 0 1 0 0-2.8 1.4 1.4 0 0 0 0 2.8Z"
                    />
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

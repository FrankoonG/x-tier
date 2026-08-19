import { forwardRef, useMemo, type HTMLAttributes } from 'react';
import clsx from 'clsx';
import './DegradationLadder.css';

/**
 * What actually happened with one rung.
 *
 * `unattempted` and `rejected` are the pair this component exists to keep
 * apart. A strategy that was tried and failed is evidence; a strategy that was
 * never reached is not evidence of anything. The default for a rung with no
 * stated outcome is `unattempted` — the weaker claim — and it is never
 * upgraded to `rejected` by position, by being above the active rung, or by
 * anything else.
 */
export type LadderOutcome = 'active' | 'rejected' | 'unattempted' | 'unavailable';

export interface LadderRung {
  id: string;
  label: string;
  /** One line explaining what the strategy is. */
  description?: string;
  /** Defaults to `'unattempted'`. Never inferred from position. */
  outcome?: LadderOutcome;
  /** Why it was rejected, or why it is unavailable. */
  reason?: string;
}

export interface DegradationLadderProps extends Omit<HTMLAttributes<HTMLDivElement>, 'children'> {
  /** Ordered by preference. Index 0 is the most preferred strategy. */
  rungs: LadderRung[];
  /**
   * Id of the strategy currently in use.
   *
   * - a string  — that rung is active
   * - `null`    — explicitly nothing is active
   * - omitted   — derived from the first rung whose `outcome` is `'active'`
   *
   * `null` and `undefined` mean different things on purpose: "we know nothing
   * is running" is not "we have not been told what is running".
   */
  active?: string | null;
  /**
   * The active rung was CHOSEN, not fallen back to.
   *
   * An operator who pins a relay has not suffered a degradation, and painting
   * their deliberate configuration in warning colours trains them to ignore
   * the colour. This suppresses the degraded styling at any index.
   */
  explicit?: boolean;
  orientation?: 'vertical' | 'horizontal';
  size?: 'sm' | 'md';
  /** Accessible name for the list. */
  label?: string;
  /** Shows the 1-based preference rank against each rung. Default `true`. */
  showRank?: boolean;

  labelActive?: string;
  labelRejected?: string;
  labelUnattempted?: string;
  labelUnavailable?: string;
  /** Suffix on the rank of index 0. Default `'preferred'`. */
  labelPreferred?: string;
  /** Badge on an explicitly chosen rung. Default `'chosen explicitly'`. */
  labelExplicit?: string;
  /** Shown when no rung is active. Default `'no strategy in use'`. */
  labelNoneActive?: string;
}

/**
 * An ordered preference list of strategies with the current one marked.
 *
 * INDEX 0 IS NEVER A DEGRADATION
 * ------------------------------
 * Running on the most preferred strategy is the normal case, so the ladder
 * paints it as healthy and shows no warning of any kind. Only an *unrequested*
 * move down the list is a degradation, which means two independent facts have
 * to line up before anything turns amber: the active rung is below index 0
 * AND `explicit` is false.
 *
 * AN EXPLICIT CHOICE IS NOT A FAULT
 * ---------------------------------
 * `explicit` marks a rung the operator selected. It suppresses the degraded
 * styling entirely and adds a "chosen" marker instead, because a configuration
 * someone made on purpose must not be reported back to them as a problem — the
 * fastest way to make a colour signal worthless is to fire it at deliberate
 * settings.
 *
 * UNATTEMPTED IS NOT FAILED
 * -------------------------
 * Rungs carry their own outcome. A rung above the active one is only shown as
 * rejected if the caller says it was rejected; otherwise it is shown as never
 * attempted, in the dashed unobserved vocabulary used throughout the library.
 * A ladder that fell straight to rung 3 without probing rungs 0-2 looks
 * visibly different from one that tried and lost them, which is precisely the
 * distinction an operator needs to debug it.
 */
export const DegradationLadder = forwardRef<HTMLDivElement, DegradationLadderProps>(
  function DegradationLadder(
    {
      rungs,
      active,
      explicit = false,
      orientation = 'vertical',
      size = 'md',
      label,
      showRank = true,
      labelActive = 'in use',
      labelRejected = 'tried, rejected',
      labelUnattempted = 'not attempted',
      labelUnavailable = 'unavailable',
      labelPreferred = 'preferred',
      labelExplicit = 'chosen explicitly',
      labelNoneActive = 'no strategy in use',
      className,
      ...rest
    },
    ref,
  ) {
    const activeIndex = useMemo(() => {
      // `null` is a positive statement that nothing is running, so it must not
      // fall through to outcome-sniffing.
      if (active === null) return -1;
      if (active !== undefined) return rungs.findIndex((rung) => rung.id === active);
      return rungs.findIndex((rung) => rung.outcome === 'active');
    }, [rungs, active]);

    if (import.meta.env?.DEV && typeof active === 'string' && activeIndex < 0) {
      console.error(
        `[stratum] <DegradationLadder active="${active}"> does not match any rung id. ` +
          'Rendering as "no strategy in use". Pass `active={null}` if that is what you mean.',
      );
    }

    // Two independent conditions, deliberately. Neither alone is a degradation.
    const degraded = activeIndex > 0 && !explicit;

    const outcomeLabel: Record<LadderOutcome, string> = {
      active: labelActive,
      rejected: labelRejected,
      unattempted: labelUnattempted,
      unavailable: labelUnavailable,
    };

    return (
      <div
        {...rest}
        ref={ref}
        data-stratum="degradation-ladder"
        data-orientation={orientation}
        data-size={size}
        data-degraded={degraded || undefined}
        data-explicit={explicit || undefined}
        data-none-active={activeIndex < 0 || undefined}
        className={clsx('stratum-degradation-ladder', className)}
      >
        <ol className="stratum-degradation-ladder__list" aria-label={label}>
          {rungs.map((rung, index) => {
            const isActive = index === activeIndex;
            const outcome: LadderOutcome = isActive ? 'active' : (rung.outcome ?? 'unattempted');
            const isPreferred = index === 0;
            // Only the active rung can be degraded, and only when it was not
            // chosen on purpose.
            const rungDegraded = isActive && degraded;

            return (
              <li
                key={rung.id}
                className="stratum-degradation-ladder__rung"
                data-outcome={outcome}
                data-active={isActive || undefined}
                data-preferred={isPreferred || undefined}
                data-degraded={rungDegraded || undefined}
                data-explicit={(isActive && explicit) || undefined}
                aria-current={isActive ? 'true' : undefined}
              >
                <span className="stratum-degradation-ladder__spine" aria-hidden="true">
                  <span className="stratum-degradation-ladder__line stratum-degradation-ladder__line-in" />
                  <span className="stratum-degradation-ladder__marker" />
                  <span className="stratum-degradation-ladder__line stratum-degradation-ladder__line-out" />
                </span>

                <span className="stratum-degradation-ladder__body">
                  <span className="stratum-degradation-ladder__head">
                    {showRank && (
                      <span className="stratum-degradation-ladder__rank" aria-hidden="true">
                        {index + 1}
                      </span>
                    )}
                    <span className="stratum-degradation-ladder__label">{rung.label}</span>
                    {isActive && explicit && (
                      <span
                        className="stratum-degradation-ladder__explicit"
                        title={labelExplicit}
                      >
                        <svg
                          viewBox="0 0 12 12"
                          width="10"
                          height="10"
                          aria-hidden="true"
                          fill="none"
                          stroke="currentColor"
                          strokeWidth={1.6}
                          strokeLinecap="round"
                          focusable="false"
                        >
                          <path d="M6 1.5v9M1.5 6h9" transform="rotate(45 6 6)" />
                        </svg>
                        <span className="stratum-visually-hidden">, {labelExplicit}</span>
                      </span>
                    )}
                  </span>

                  <span className="stratum-degradation-ladder__state">
                    {outcomeLabel[outcome]}
                    {isPreferred && (
                      <span className="stratum-degradation-ladder__preferred">
                        {' · '}
                        {labelPreferred}
                      </span>
                    )}
                  </span>

                  {rung.description && (
                    <span className="stratum-degradation-ladder__desc">{rung.description}</span>
                  )}
                  {rung.reason && (
                    <span className="stratum-degradation-ladder__reason">{rung.reason}</span>
                  )}
                </span>
              </li>
            );
          })}
        </ol>

        {activeIndex < 0 && (
          <p className="stratum-degradation-ladder__none">{labelNoneActive}</p>
        )}
      </div>
    );
  },
);

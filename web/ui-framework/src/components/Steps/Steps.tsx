import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Steps.css';

export type StepStatus = 'complete' | 'current' | 'upcoming' | 'error';
export type StepsOrientation = 'horizontal' | 'vertical';
export type StepsSize = 'sm' | 'md' | 'lg';

export interface StepItem {
  /** Stable identity. Used as the React key. */
  id: string;
  label: ReactNode;
  description?: ReactNode;
  /** Overrides the status derived from `activeStep`. */
  status?: StepStatus;
  /** Replaces the marker glyph. */
  icon?: ReactNode;
  /** Blocks selection when the step list is interactive. */
  disabled?: boolean;
}

export interface StepsProps extends Omit<HTMLAttributes<HTMLElement>, 'onSelect'> {
  steps: StepItem[];
  /** Zero-based index of the current step. */
  activeStep?: number;
  orientation?: StepsOrientation;
  size?: StepsSize;
  /**
   * Makes each step a button. Omit for a pure progress readout — a wizard that
   * cannot be navigated out of order should not look as though it can be.
   */
  onStepSelect?: (index: number, step: StepItem) => void;
  /** Show the 1-based ordinal inside the marker of incomplete steps. */
  showNumbers?: boolean;

  /* -- Copy ---------------------------------------------------------------- */
  label?: string;
  labelComplete?: string;
  labelCurrent?: string;
  labelUpcoming?: string;
  labelError?: string;
  /** Announced before the label, e.g. "Step 2 of 5". */
  labelStep?: (index: number, total: number) => string;
}

/* `fill` is declared on every shape rather than once on the <svg>: the reset
 * layer sets `svg { fill: currentColor }`, which beats a presentational
 * `fill="none"` attribute on the root and would otherwise flood stroked icons. */
const CheckIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <path
      d="m3.5 8.4 3 3 6-6.8"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.9"
      strokeLinecap="round"
      strokeLinejoin="round"
    />
  </svg>
);

const AlertIcon = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
    <path d="M8 4.2v4.4" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" />
    <circle cx="8" cy="11.4" r="1.05" fill="currentColor" />
  </svg>
);

function resolveStatus(step: StepItem, index: number, activeStep: number): StepStatus {
  if (step.status) return step.status;
  if (index < activeStep) return 'complete';
  if (index === activeStep) return 'current';
  return 'upcoming';
}

/**
 * Linear progress through a multi-stage task.
 *
 * STATUS IS NEVER COLOUR ALONE
 * ----------------------------
 * Complete draws a tick, error draws an exclamation, current draws a filled
 * ring, and upcoming draws a hollow one. The colours are a second channel, not
 * the only one — which is what keeps the component readable for the ~8% of male
 * users with a red/green deficiency and under forced colours, where the fills
 * are repainted by the OS regardless of what we ask for.
 *
 * The connector between markers is `aria-hidden` and drawn with a real
 * background rather than a border, so its "travelled" and "remaining" halves
 * can differ without a second element.
 *
 * When `onStepSelect` is absent every step renders as a `<span>`. Rendering
 * disabled buttons instead would put four dead tab stops in front of a keyboard
 * user before they reach the form the wizard is actually asking them to fill in.
 */
export const Steps = forwardRef<HTMLElement, StepsProps>(function Steps(
  {
    steps,
    activeStep = 0,
    orientation = 'horizontal',
    size = 'md',
    onStepSelect,
    showNumbers = true,
    label = 'Progress',
    labelComplete = 'Completed',
    labelCurrent = 'Current step',
    labelUpcoming = 'Not started',
    labelError = 'Error',
    labelStep = (index: number, total: number) => `Step ${index + 1} of ${total}`,
    className,
    ...rest
  },
  ref,
) {
  const total = steps.length;
  const interactive = typeof onStepSelect === 'function';

  const statusLabel: Record<StepStatus, string> = {
    complete: labelComplete,
    current: labelCurrent,
    upcoming: labelUpcoming,
    error: labelError,
  };

  return (
    <nav
      // Before the spread: an attribute written after `...rest` wins in JSX,
      // so a consumer passing `aria-label` directly would silently get the
      // framework default instead.
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="steps"
      data-orientation={orientation}
      data-size={size}
      className={clsx('stratum-steps', className)}
    >
      <ol className="stratum-steps__list">
        {steps.map((step, index) => {
          const status = resolveStatus(step, index, activeStep);
          const isLast = index === total - 1;
          const isDisabled = step.disabled === true;

          const marker = (
            <span className="stratum-steps__marker" aria-hidden="true">
              {step.icon ??
                (status === 'complete' ? (
                  <CheckIcon />
                ) : status === 'error' ? (
                  <AlertIcon />
                ) : showNumbers ? (
                  <span className="stratum-steps__number stratum-numeric">{index + 1}</span>
                ) : (
                  <span className="stratum-steps__dot" />
                ))}
            </span>
          );

          const body = (
            <>
              {/* First in DOM order because the accessible name is built in DOM
               * order: "Step 2 of 4. Current step. Transport" is the useful
               * reading, and the ordinal and status are exactly the parts the
               * visual marker conveys and a screen reader cannot see. */}
              <span className="stratum-visually-hidden">
                {`${labelStep(index, total)}. ${statusLabel[status]}.`}
              </span>
              {marker}
              <span className="stratum-steps__text">
                <span className="stratum-steps__label">{step.label}</span>
                {step.description != null && step.description !== false && (
                  <span className="stratum-steps__description">{step.description}</span>
                )}
              </span>
            </>
          );

          return (
            <li
              key={step.id}
              className="stratum-steps__item"
              data-status={status}
              data-last={isLast || undefined}
            >
              {interactive ? (
                <button
                  type="button"
                  data-stratum="step"
                  data-status={status}
                  data-interactive="true"
                  data-disabled={isDisabled || undefined}
                  aria-current={status === 'current' ? 'step' : undefined}
                  aria-disabled={isDisabled || undefined}
                  className="stratum-steps__control"
                  onClick={(event) => {
                    if (isDisabled) {
                      event.preventDefault();
                      return;
                    }
                    onStepSelect?.(index, step);
                  }}
                >
                  {body}
                </button>
              ) : (
                <span
                  data-stratum="step"
                  data-status={status}
                  aria-current={status === 'current' ? 'step' : undefined}
                  className="stratum-steps__control"
                >
                  {body}
                </span>
              )}

              {!isLast && <span className="stratum-steps__connector" aria-hidden="true" />}
            </li>
          );
        })}
      </ol>
    </nav>
  );
});

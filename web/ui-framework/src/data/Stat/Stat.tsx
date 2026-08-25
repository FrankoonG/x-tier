import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Stat.css';

/**
 * Editorial weight of the reading.
 *
 * `neutral` is the only honest default: a count is neither good nor bad until
 * someone declares a threshold, and a component that guesses one is inventing
 * an opinion the data never expressed.
 */
export type StatTone = 'neutral' | 'ok' | 'warn' | 'danger' | 'info';

export interface StatProps extends Omit<HTMLAttributes<HTMLDListElement>, 'children'> {
  /** Axis name, rendered as the uppercase micro-label the framework uses for chrome. */
  label: ReactNode;
  /**
   * The reading. `null`/`undefined`/`''` means NOT OBSERVED and is rendered as
   * such — never as `0`. Pass a RAW value; see the note in the component
   * docblock about wrapping a metric here.
   */
  value: ReactNode;
  /** One short line under the value: what the number actually counts. */
  hint?: ReactNode;
  /** Leading glyph. Inherits `currentColor` and is sized in `em`. */
  icon?: ReactNode;
  tone?: StatTone;
  /** `md` is the 22px stat value; `sm` is one step down. */
  size?: 'sm' | 'md';
  /** Text used where the value has not been observed. Default `'not observed'`. */
  unobservedLabel?: string;
}

/**
 * Spoken form of the tone, so hue is never the only carrier (WCAG 1.4.1).
 * The marker shape is the second channel; this is the third, and the only one
 * that survives both forced colours and a screen reader.
 */
const TONE_TEXT: Record<Exclude<StatTone, 'neutral'>, string> = {
  ok: 'ok',
  warn: 'warning',
  danger: 'danger',
  info: 'info',
};

/**
 * An element that will paint its own unobserved dash while the stat around it
 * claims an observation.
 *
 * Same trap, same detection as `StateMatrix` — duplicated rather than shared
 * because exporting it from there would put a private helper on the public
 * surface. Detected from the element's OWN `value` prop rather than its type,
 * so the whole `Metric` family is covered and a guarded element is not:
 *
 *   `<Stat value={<Count value={null} />} />`   warns  — element is truthy
 *   `<Stat value={n != null ? <Count …/> : null} />`  silent — guard did it
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
 * A headline count: one axis name, one number, one line of gloss.
 *
 * WHY IT IS A COMPONENT AND NOT BODY TEXT
 * ---------------------------------------
 * `--stratum-text-2xl` exists for exactly this and nothing else. A count set in
 * body copy inside a large card reads as prose and gets skipped; the whole
 * value of a stat is that it is legible from across the room, which is a type
 * decision, not a layout one.
 *
 * UNMEASURED IS NOT ZERO
 * ----------------------
 * The failure this component exists to prevent: a panel printing a confident
 * `0` where the truth is "we never looked". Zero is a measurement — it says the
 * probe ran and found nothing. Absence says the probe did not run. Collapsing
 * them is how a panel ends up claiming a link is idle when it was never dialled.
 *
 * So `null` renders the framework's existing unobserved treatment — the words
 * *not observed*, italic, in the sans face, at the `unknown` text grade —
 * exactly as `StateMatrix` does. Not a dash: at 22px a lone em dash reads as a
 * rendering failure rather than a statement, which is why `Gauge` can use one
 * inside a dial with a track to explain it and a bare stat cannot.
 *
 * TONE CANNOT SURVIVE AN UNOBSERVED VALUE
 * ---------------------------------------
 * `tone` is forced back to `neutral` when nothing was observed. A caller that
 * hard-codes `tone="ok"` on a count that turns out to be missing would
 * otherwise paint reassurance over a hole in the data — the same lie as the
 * zero, in colour.
 *
 * PASS A RAW VALUE
 * ----------------
 * The number is already set in tabular figures here, so wrapping a `Metric`
 * gains nothing and costs the contract: an element is always truthy, so the
 * stat marks itself observed while the metric inside prints its own dash.
 * There is a DEV-only guard for it below.
 */
export const Stat = forwardRef<HTMLDListElement, StatProps>(function Stat(
  {
    label,
    value,
    hint,
    icon,
    tone = 'neutral',
    size = 'md',
    unobservedLabel = 'not observed',
    className,
    ...rest
  },
  ref,
) {
  // `''` counts as unobserved alongside nullish, matching StateMatrix: an empty
  // string is what a formatter returns when it had nothing to format.
  const observed = value !== null && value !== undefined && value !== '';

  if (import.meta.env?.DEV && rendersUnobserved(value)) {
    // eslint-disable-next-line no-console
    console.error(
      '[stratum] <Stat> was given an element whose own `value` is empty, so it will render '
        + '"not observed" inside a stat this component has already marked as observed — an '
        + 'element is always truthy. Pass the raw value, or guard the element: '
        + '{x != null ? <…value={x}/> : null}.',
    );
  }

  // Judgement requires a measurement. See the docblock.
  const effectiveTone: StatTone = observed ? tone : 'neutral';

  return (
    // A description list rather than StateMatrix's shared `<dl>` of `<div>`
    // items: a stat has to stand alone in a card, and a bare `<dt>`/`<dd>` pair
    // outside a list is invalid. Several of these inside a StatGroup is fine —
    // consecutive lists are legal and each pair keeps its own association.
    <dl
      {...rest}
      ref={ref}
      data-stratum="stat"
      data-tone={effectiveTone}
      data-size={size}
      data-observed={observed || undefined}
      className={clsx('stratum-stat', className)}
    >
      <dt className="stratum-stat__label">{label}</dt>
      <dd className="stratum-stat__value">
        {icon != null && (
          <span className="stratum-stat__icon" aria-hidden="true">
            {icon}
          </span>
        )}
        {effectiveTone !== 'neutral' && (
          <span className="stratum-stat__tone" aria-hidden="true" />
        )}
        <span className="stratum-stat__number" data-unobserved={!observed || undefined}>
          {observed ? value : unobservedLabel}
        </span>
        {effectiveTone !== 'neutral' && (
          <span className="stratum-visually-hidden">, {TONE_TEXT[effectiveTone]}</span>
        )}
      </dd>
      {/* A second `<dd>`: the hint describes the same axis, so it belongs to
          the same `<dt>` rather than to a term of its own. */}
      {hint != null && <dd className="stratum-stat__hint">{hint}</dd>}
    </dl>
  );
});

/* -------------------------------------------------------------------------- */

export interface StatGroupProps extends HTMLAttributes<HTMLDivElement> {
  /**
   * Draws a rule in each column gap. Off by default — see the CSS for why
   * space is the first separator and a rule is the exception.
   */
  separators?: boolean;
  /** Names the group for assistive tech. Without one, no group role is set. */
  label?: string;
}

/**
 * Lays several stats out in an auto-fit grid.
 *
 * No `columns` prop. A stat's minimum legible width is a property of its type
 * size, not of the page, so the track minimum lives in CSS as `--_min` (in
 * `rem`, so it tracks zoom) and the count falls out of the container. A caller
 * who genuinely needs a fixed count can override `--_min`.
 */
export const StatGroup = forwardRef<HTMLDivElement, StatGroupProps>(function StatGroup(
  { separators = false, label, className, children, ...rest },
  ref,
) {
  return (
    <div
      // Before the spread: both are optional, and after it the `undefined`
      // would win the object literal and delete a consumer's own value.
      // `group` only when there is a name for it — an unnamed group role is
      // announced as an unlabelled landmark and buys nothing.
      role={label ? 'group' : undefined}
      aria-label={label}
      {...rest}
      ref={ref}
      data-stratum="stat-group"
      data-separated={separators || undefined}
      className={clsx('stratum-stat-group', className)}
    >
      {children}
    </div>
  );
});

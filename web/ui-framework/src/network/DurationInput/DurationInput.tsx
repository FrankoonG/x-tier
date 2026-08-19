import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type InputHTMLAttributes,
} from 'react';
import clsx from 'clsx';
import { useFieldControl } from '../../components/Field/Field';
import { Select } from '../../components/Select/Select';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { UNOBSERVED, formatCount } from '../format';
import {
  ValidityMark,
  joinIds,
  resolveAriaInvalid,
  warnMissingNetFieldLabel,
  type NetControlSize,
  type ValidityState,
} from '../_shared/netField';
import { statusGlyph } from '../../components/_shared/statusIcons';
import './DurationInput.css';

export type DurationUnit = 'ms' | 's' | 'm' | 'h' | 'd';

/** Milliseconds per unit. Exported because callers convert outside the field too. */
export const DURATION_UNIT_MS: Record<DurationUnit, number> = {
  ms: 1,
  s: 1000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
};

export const DEFAULT_DURATION_UNITS: readonly DurationUnit[] = ['ms', 's', 'm', 'h', 'd'];

/** English default labels. Every one is overridable via `unitLabels`. */
export const DURATION_UNIT_LABELS: Record<DurationUnit, string> = {
  ms: 'ms',
  s: 'sec',
  m: 'min',
  h: 'hr',
  d: 'days',
};

/**
 * Chooses the largest unit in which `ms` is a whole number.
 *
 * 90 000 ms shows as `90 sec` rather than `1.5 min`, because a value an
 * operator can retype without a decimal point is a value they can retype
 * without a mistake. Falls back to `preferred` for `null` and for `0`, where
 * every unit divides exactly and the largest would be an odd choice.
 */
export function pickDurationUnit(
  ms: number | null,
  units: readonly DurationUnit[] = DEFAULT_DURATION_UNITS,
  preferred: DurationUnit = 'ms',
): DurationUnit {
  const allowed = units.length > 0 ? units : DEFAULT_DURATION_UNITS;
  const fallback = allowed.includes(preferred) ? preferred : (allowed[0] ?? 'ms');
  if (ms == null || !Number.isFinite(ms) || ms === 0) return fallback;

  const ordered = [...allowed].sort((a, b) => DURATION_UNIT_MS[b] - DURATION_UNIT_MS[a]);
  for (const unit of ordered) {
    if (Math.abs(ms) % DURATION_UNIT_MS[unit] === 0) return unit;
  }
  return ordered[ordered.length - 1] ?? fallback;
}

/** Renders a scaled number without float noise or trailing zeros. */
function numberToText(value: number): string {
  if (!Number.isFinite(value)) return '';
  // 1e-6 resolution is well past anything a duration field needs and removes
  // artefacts like 0.30000000000000004.
  return String(Math.round(value * 1e6) / 1e6);
}

export interface DurationValidation {
  /** `true` when the field is blank — the duration is UNOBSERVED, not zero. */
  empty: boolean;
  valid: boolean;
  /** Resolved milliseconds, or `null` when blank or unparseable. */
  ms: number | null;
  unit: DurationUnit;
  message: string | null;
}

type NativeInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'value' | 'defaultValue' | 'onChange' | 'type' | 'size' | 'min' | 'max' | 'step'
>;

export interface DurationInputProps extends NativeInputProps {
  /** Duration in milliseconds. `null` means not set. */
  value?: number | null;
  defaultValue?: number | null;
  /** Emits milliseconds, or `null` when the field is cleared. */
  onChange?: (ms: number | null) => void;
  onValidChange?: (validation: DurationValidation) => void;

  /** Units offered, in menu order. Default `['ms','s','m','h','d']`. */
  units?: readonly DurationUnit[];
  /** Unit used for a blank field and for zero. Default the first in `units`. */
  defaultUnit?: DurationUnit;
  /** Controlled unit. */
  unit?: DurationUnit;
  onUnitChange?: (unit: DurationUnit) => void;

  size?: NetControlSize;
  required?: boolean;
  /** Inclusive lower bound, in milliseconds. */
  min?: number;
  /** Inclusive upper bound, in milliseconds. */
  max?: number;
  /** Reject values that do not resolve to a whole number of ms. Default `false`. */
  integerOnly?: boolean;
  /** Permit negative durations — offsets and skews. Default `false`. */
  allowNegative?: boolean;

  /** Show the resolved millisecond value beneath the control. Default `true`. */
  showResolved?: boolean;
  showValidity?: boolean;
  showMessage?: boolean;
  /**
   * Suppress the error until first blur. Default `true` — while typing `1e3`
   * the field passes through `1e`, which is not a number, and flashing red
   * there teaches operators to ignore the message.
   */
  validateOnBlurOnly?: boolean;

  /** Accessible name for the unit selector. Default `'Unit'`. */
  unitLabel?: string;
  unitLabels?: Partial<Record<DurationUnit, string>>;
  /** Formats the resolved readout. Default `'= 1,500 ms'`. */
  formatResolved?: (ms: number | null) => string;
  /** Shown where the resolved value is unknown. Default `'not set'`. */
  unresolvedLabel?: string;

  messageRequired?: string;
  messageNotANumber?: string;
  messageNegative?: string;
  messageMin?: (minMs: number) => string;
  messageMax?: (maxMs: number) => string;
  messageInteger?: string;

  hint?: string;
  labelValid?: string;
  labelInvalid?: string;
  invalid?: boolean;
  fullWidth?: boolean;
  wrapperClassName?: string;
}

const DEFAULT_FORMAT_RESOLVED = (ms: number | null) =>
  ms == null ? UNOBSERVED : `= ${formatCount(ms)} ms`;

/**
 * A duration entered as a number plus a unit, emitted as milliseconds.
 *
 * WHY A UNIT SELECTOR RATHER THAN A PARSED STRING
 * -----------------------------------------------
 * The alternative is a free-text field accepting `30s`, `1m30s`, `PT1M30S`.
 * Every one of those grammars has an ambiguity — `m` is minutes here and
 * months in ISO 8601, `1.5h` is legal in Go and not in Rust — and an operator
 * who guesses the wrong one gets a plausible wrong value instead of an error.
 * Two explicit controls cannot be misread.
 *
 * NULL IS NOT ZERO
 * ----------------
 * A blank field emits `null`, never `0`. "No timeout configured" and "a timeout
 * of zero" are different instructions to a daemon — one inherits a default and
 * the other disables waiting entirely — and a field that silently turns the
 * first into the second is a production incident waiting to happen. The
 * resolved readout underneath shows the em dash for a blank field for exactly
 * the same reason.
 *
 * The displayed unit is chosen so the number stays whole: 90 000 ms appears as
 * `90 sec`, not `1.5 min`.
 */
export const DurationInput = forwardRef<HTMLInputElement, DurationInputProps>(
  function DurationInput(
    {
      value,
      defaultValue = null,
      onChange,
      onValidChange,

      units = DEFAULT_DURATION_UNITS,
      defaultUnit,
      unit: unitProp,
      onUnitChange,

      size: sizeProp,
      required: requiredProp,
      min,
      max,
      integerOnly = false,
      allowNegative = false,

      showResolved = true,
      showValidity = true,
      showMessage = true,
      validateOnBlurOnly = true,

      unitLabel = 'Unit',
      unitLabels,
      formatResolved = DEFAULT_FORMAT_RESOLVED,
      unresolvedLabel = 'not set',

      messageRequired = 'Enter a duration.',
      messageNotANumber = 'Enter a number.',
      messageNegative = 'A duration cannot be negative.',
      messageMin,
      messageMax,
      messageInteger = 'Must resolve to a whole number of milliseconds.',

      hint,
      labelValid = 'valid',
      labelInvalid = 'invalid',
      invalid: invalidProp = false,
      fullWidth = true,
      wrapperClassName,
      className,
      disabled,
      readOnly,
      placeholder,
      onBlur,
      onFocus,
      id: idProp,
      ...rest
    },
    ref,
  ) {
    /* Wiring published by an enclosing `<Field>`. Outside one this is an inert
     * object, so the standalone case needs no branch. The Field's `<label for>`
     * already points at `field.id`, so adopting it is not optional. */
    const field = useFieldControl();
    const reactId = useId();
    const id = idProp ?? field.id ?? `${reactId}-duration`;
    const size = sizeProp ?? field.size;
    const required = requiredProp ?? field.required;
    const messageId = `${id}-message`;
    const hintId = `${id}-hint`;
    const resolvedId = `${id}-resolved`;

    // The unit picker carries its own `aria-label`; the number beside it does
    // not, and "30" with no name tells an operator nothing.
    warnMissingNetFieldLabel('DurationInput', {
      fieldId: field.id,
      fieldLabelId: field.labelId,
      ariaLabel: rest['aria-label'],
      ariaLabelledBy: rest['aria-labelledby'],
      explicitId: idProp,
    });

    const allowedUnits = units.length > 0 ? units : DEFAULT_DURATION_UNITS;
    const preferredUnit = defaultUnit ?? allowedUnits[0] ?? 'ms';

    const [ms, setMs] = useControllableState<number | null>({
      value,
      defaultValue,
      onChange,
    });

    const [unitState, setUnitState] = useControllableState<DurationUnit>({
      value: unitProp,
      defaultValue: pickDurationUnit(defaultValue, allowedUnits, preferredUnit),
      onChange: onUnitChange,
    });

    /* The text is view state, not derived state. Deriving it from `ms` on every
     * render would erase a half-typed `1.` before the operator reaches the `5`. */
    const [text, setText] = useState<string>(() =>
      ms == null ? '' : numberToText(ms / DURATION_UNIT_MS[unitState]),
    );
    const [touched, setTouched] = useState(false);

    /* Tracks the last value this field itself produced, so an echo of our own
     * emission does not reformat the field under the cursor while a genuine
     * external change still does. */
    const selfValue = useRef<number | null>(ms);

    useEffect(() => {
      if (ms === selfValue.current) return;
      selfValue.current = ms;
      const nextUnit = pickDurationUnit(ms, allowedUnits, unitState);
      if (nextUnit !== unitState) setUnitState(nextUnit);
      setText(ms == null ? '' : numberToText(ms / DURATION_UNIT_MS[nextUnit]));
    }, [ms, allowedUnits, unitState, setUnitState]);

    const commit = useCallback(
      (nextText: string, nextUnit: DurationUnit) => {
        const trimmed = nextText.trim();
        if (trimmed.length === 0) {
          selfValue.current = null;
          setMs(null);
          return;
        }
        const parsed = Number(trimmed);
        if (!Number.isFinite(parsed)) return; // reported by `validation`, not emitted
        const resolved = parsed * DURATION_UNIT_MS[nextUnit];
        selfValue.current = resolved;
        setMs(resolved);
      },
      [setMs],
    );

    const validation = useMemo<DurationValidation>(() => {
      const trimmed = text.trim();
      if (trimmed.length === 0) {
        return {
          empty: true,
          valid: !required,
          ms: null,
          unit: unitState,
          message: required ? messageRequired : null,
        };
      }
      const parsed = Number(trimmed);
      if (!Number.isFinite(parsed)) {
        return { empty: false, valid: false, ms: null, unit: unitState, message: messageNotANumber };
      }
      const resolved = parsed * DURATION_UNIT_MS[unitState];
      if (!allowNegative && resolved < 0) {
        return { empty: false, valid: false, ms: resolved, unit: unitState, message: messageNegative };
      }
      if (integerOnly && !Number.isInteger(resolved)) {
        return { empty: false, valid: false, ms: resolved, unit: unitState, message: messageInteger };
      }
      if (min !== undefined && resolved < min) {
        return {
          empty: false,
          valid: false,
          ms: resolved,
          unit: unitState,
          message: messageMin?.(min) ?? `Must be at least ${formatCount(min)} ms.`,
        };
      }
      if (max !== undefined && resolved > max) {
        return {
          empty: false,
          valid: false,
          ms: resolved,
          unit: unitState,
          message: messageMax?.(max) ?? `Must be at most ${formatCount(max)} ms.`,
        };
      }
      return { empty: false, valid: true, ms: resolved, unit: unitState, message: null };
    }, [
      text,
      unitState,
      required,
      allowNegative,
      integerOnly,
      min,
      max,
      messageRequired,
      messageNotANumber,
      messageNegative,
      messageInteger,
      messageMin,
      messageMax,
    ]);

    const emitValid = useEventCallback(onValidChange);
    const lastSignature = useRef<string | null>(null);
    useEffect(() => {
      const signature = `${validation.valid}|${validation.empty}|${validation.ms ?? ''}|${validation.unit}|${validation.message ?? ''}`;
      if (lastSignature.current === signature) return;
      lastSignature.current = signature;
      emitValid(validation);
    }, [validation, emitValid]);

    const showProblem = !validation.valid && (touched || !validateOnBlurOnly);
    const state: ValidityState = invalidProp || field.invalid || showProblem
      ? 'invalid'
      : validation.empty
        ? 'empty'
        : 'valid';
    const isInvalid = state === 'invalid';
    const activeMessage = isInvalid ? validation.message : null;

    const describedBy = joinIds(
      field.describedBy,
      rest['aria-describedby'],
      activeMessage && showMessage ? messageId : undefined,
      hint && showMessage && !activeMessage ? hintId : undefined,
      showResolved ? resolvedId : undefined,
    );

    const handleBlur = useEventCallback(onBlur);
    const handleFocus = useEventCallback(onFocus);

    return (
      <div
        data-stratum="duration-input"
        data-size={size}
        data-validity={state}
        data-full-width={fullWidth || undefined}
        className={clsx('stratum-net-field', 'stratum-duration-input', wrapperClassName)}
      >
        <div
          className="stratum-net-control stratum-duration-input__control"
          data-size={size}
          data-validity={isInvalid ? 'invalid' : undefined}
          data-disabled={disabled || undefined}
          data-readonly={readOnly || undefined}
        >
          <input
            {...rest}
            ref={ref}
            id={id}
            /* `text`, not `number`. A native number input silently discards a
             * value it cannot parse — including on paste — so a mistyped
             * duration becomes an empty field with no explanation, and the
             * spinner arrows step by a unit that is meaningless here. */
            type="text"
            inputMode="decimal"
            className={clsx('stratum-net-input', className)}
            data-numeric=""
            value={text}
            disabled={disabled}
            readOnly={readOnly}
            placeholder={placeholder}
            autoComplete={rest.autoComplete ?? 'off'}
            spellCheck={false}
            aria-invalid={resolveAriaInvalid(isInvalid ? 'invalid' : 'valid', rest['aria-invalid'])}
            aria-describedby={describedBy}
            // Merged rather than assigned: this sits after the spread, so a
            // bare `required || undefined` would delete a consumer's own value.
            aria-required={required ? true : rest['aria-required']}
            onChange={(event) => {
              const next = event.currentTarget.value;
              setText(next);
              commit(next, unitState);
            }}
            onFocus={(event) => handleFocus(event)}
            onBlur={(event) => {
              setTouched(true);
              handleBlur(event);
            }}
          />

          {/* The framework's own Select, not a native one: an OS-drawn picker
            * inside a themed control shell reads as a different product.
            * `stratum-net-unit` stays on it because the shell draws the focus
            * ring with `:has(.stratum-net-unit:focus-visible)`, and
            * DurationInput.css flattens the trigger into a segment of the
            * shell rather than a second bordered box inside it. */}
          <Select
            className="stratum-net-unit stratum-duration-input__unit"
            size={size}
            aria-label={unitLabel}
            options={allowedUnits.map((u) => ({
              value: u,
              label: unitLabels?.[u] ?? DURATION_UNIT_LABELS[u],
            }))}
            value={unitState}
            disabled={disabled || readOnly}
            onChange={(value) => {
              if (value == null) return;
              const next = value as DurationUnit;
              setUnitState(next);
              /* Changing the unit re-interprets the SAME number rather than
               * converting it: `30` with `sec` becomes `30` with `min`, not
               * `0.5`. Converting would mean the operator picks a unit and the
               * field answers by changing the number they just typed. */
              commit(text, next);
            }}
          />

          {showValidity && (
            <ValidityMark
              state={state}
              label={state === 'invalid' ? labelInvalid : state === 'valid' ? labelValid : undefined}
            />
          )}
        </div>

        {showResolved && (
          <p id={resolvedId} className="stratum-net-meta">
            <span
              className="stratum-net-meta__value"
              data-unobserved={validation.ms == null || undefined}
            >
              {validation.ms == null ? unresolvedLabel : formatResolved(validation.ms)}
            </span>
          </p>
        )}

        {showMessage && activeMessage && (
          <p id={messageId} className="stratum-net-message" data-tone="error">
            <span className="stratum-net-message__icon" aria-hidden="true">
              {statusGlyph('danger')}
            </span>
            <span className="stratum-net-message__text">{activeMessage}</span>
          </p>
        )}

        {showMessage && hint && !activeMessage && (
          <p id={hintId} className="stratum-net-message" data-tone="hint">
            <span className="stratum-net-message__text">{hint}</span>
          </p>
        )}
      </div>
    );
  },
);

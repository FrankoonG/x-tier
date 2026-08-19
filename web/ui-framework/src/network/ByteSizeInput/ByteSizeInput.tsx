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
import { UNOBSERVED, formatBytes, formatCount } from '../format';
import {
  ValidityMark,
  joinIds,
  resolveAriaInvalid,
  warnMissingNetFieldLabel,
  type NetControlSize,
  type ValidityState,
} from '../_shared/netField';
import { statusGlyph } from '../../components/_shared/statusIcons';
import './ByteSizeInput.css';

/** Binary units (1024) and their decimal counterparts (1000). */
export type ByteUnit =
  | 'B'
  | 'KiB'
  | 'MiB'
  | 'GiB'
  | 'TiB'
  | 'PiB'
  | 'kB'
  | 'MB'
  | 'GB'
  | 'TB'
  | 'PB';

/**
 * Bytes per unit.
 *
 * Both families are here on purpose. KiB and kB differ by 2.4% and the gap
 * compounds: a "1 TB" quota entered as TiB is 10% larger than intended. Mixing
 * them silently is the single most common unit bug in storage and transfer UIs,
 * so the component makes the operator pick one explicitly and shows the
 * resulting byte count underneath.
 */
export const BYTE_UNIT_BYTES: Record<ByteUnit, number> = {
  B: 1,
  KiB: 1024,
  MiB: 1024 ** 2,
  GiB: 1024 ** 3,
  TiB: 1024 ** 4,
  PiB: 1024 ** 5,
  kB: 1000,
  MB: 1000 ** 2,
  GB: 1000 ** 3,
  TB: 1000 ** 4,
  PB: 1000 ** 5,
};

export const BINARY_BYTE_UNITS: readonly ByteUnit[] = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
export const DECIMAL_BYTE_UNITS: readonly ByteUnit[] = ['B', 'kB', 'MB', 'GB', 'TB'];

/**
 * Chooses the largest unit in which `bytes` is a whole number.
 *
 * 1 572 864 bytes shows as `1536 KiB` rather than `1.5 MiB`: a whole number is
 * one an operator can retype without losing a digit to a decimal point. Falls
 * back to `preferred` for `null` and for `0`, where every unit divides exactly
 * and the largest would be a strange choice.
 */
export function pickByteUnit(
  bytes: number | null,
  units: readonly ByteUnit[] = BINARY_BYTE_UNITS,
  preferred: ByteUnit = 'B',
): ByteUnit {
  const allowed = units.length > 0 ? units : BINARY_BYTE_UNITS;
  const fallback = allowed.includes(preferred) ? preferred : (allowed[0] ?? 'B');
  if (bytes == null || !Number.isFinite(bytes) || bytes === 0) return fallback;

  const ordered = [...allowed].sort((a, b) => BYTE_UNIT_BYTES[b] - BYTE_UNIT_BYTES[a]);
  for (const unit of ordered) {
    if (Math.abs(bytes) % BYTE_UNIT_BYTES[unit] === 0) return unit;
  }
  return ordered[ordered.length - 1] ?? fallback;
}

function numberToText(value: number): string {
  if (!Number.isFinite(value)) return '';
  return String(Math.round(value * 1e6) / 1e6);
}

export interface ByteSizeValidation {
  /** `true` when the field is blank — the size is UNOBSERVED, not zero. */
  empty: boolean;
  valid: boolean;
  /** Resolved bytes, or `null` when blank or unparseable. */
  bytes: number | null;
  unit: ByteUnit;
  message: string | null;
}

type NativeInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'value' | 'defaultValue' | 'onChange' | 'type' | 'size' | 'min' | 'max' | 'step'
>;

export interface ByteSizeInputProps extends NativeInputProps {
  /** Size in bytes. `null` means not set. */
  value?: number | null;
  defaultValue?: number | null;
  /** Emits bytes, or `null` when the field is cleared. */
  onChange?: (bytes: number | null) => void;
  onValidChange?: (validation: ByteSizeValidation) => void;

  /**
   * Which unit family to offer. `'binary'` gives B/KiB/MiB/GiB/TiB,
   * `'decimal'` gives B/kB/MB/GB/TB. Default `'binary'` — correct for buffers,
   * quotas and file sizes. Use `'decimal'` for link rates and vendor capacities.
   */
  base?: 'binary' | 'decimal';
  /** Explicit unit list, overriding `base`. */
  units?: readonly ByteUnit[];
  defaultUnit?: ByteUnit;
  unit?: ByteUnit;
  onUnitChange?: (unit: ByteUnit) => void;

  size?: NetControlSize;
  required?: boolean;
  /** Inclusive lower bound, in bytes. */
  min?: number;
  /** Inclusive upper bound, in bytes. */
  max?: number;
  /** Reject values that do not resolve to a whole number of bytes. Default `true`. */
  integerOnly?: boolean;
  allowNegative?: boolean;

  /** Show the resolved byte count beneath the control. Default `true`. */
  showResolved?: boolean;
  showValidity?: boolean;
  showMessage?: boolean;
  /** Suppress the error until first blur. Default `true`. */
  validateOnBlurOnly?: boolean;

  unitLabel?: string;
  unitLabels?: Partial<Record<ByteUnit, string>>;
  /** Formats the resolved readout. Default `'= 1,048,576 bytes (1.00 MiB)'`. */
  formatResolved?: (bytes: number | null, base: 'binary' | 'decimal') => string;
  unresolvedLabel?: string;

  messageRequired?: string;
  messageNotANumber?: string;
  messageNegative?: string;
  messageMin?: (minBytes: number) => string;
  messageMax?: (maxBytes: number) => string;
  messageInteger?: string;

  hint?: string;
  labelValid?: string;
  labelInvalid?: string;
  invalid?: boolean;
  fullWidth?: boolean;
  wrapperClassName?: string;
}

const DEFAULT_FORMAT_RESOLVED = (bytes: number | null, base: 'binary' | 'decimal') =>
  bytes == null ? UNOBSERVED : `= ${formatCount(bytes)} B (${formatBytes(bytes, { base })})`;

/**
 * A byte size entered as a number plus a unit, emitted as bytes.
 *
 * WHY THE BYTE COUNT IS ALWAYS SHOWN
 * ----------------------------------
 * KiB and kB are one keystroke apart and 2.4% different, and the error
 * compounds by unit: `1 TB` and `1 TiB` differ by about 10%, which is the
 * difference between a quota that holds and one that silently truncates. The
 * resolved readout underneath is the check — it makes the actual number leaving
 * the form visible before it is submitted rather than after it misbehaves.
 *
 * The default family is BINARY, because the values these fields carry are
 * buffers, windows, quotas and file sizes, all of which are allocated in powers
 * of two. Set `base="decimal"` for link rates, where the vendor convention is
 * powers of ten.
 *
 * NULL IS NOT ZERO
 * ----------------
 * A blank field emits `null`. "No limit configured" and "a limit of zero bytes"
 * are opposite instructions, and the field never converts the first into the
 * second — the readout shows the unresolved label rather than `0 B`.
 */
export const ByteSizeInput = forwardRef<HTMLInputElement, ByteSizeInputProps>(
  function ByteSizeInput(
    {
      value,
      defaultValue = null,
      onChange,
      onValidChange,

      base = 'binary',
      units,
      defaultUnit,
      unit: unitProp,
      onUnitChange,

      size: sizeProp,
      required: requiredProp,
      min,
      max,
      integerOnly = true,
      allowNegative = false,

      showResolved = true,
      showValidity = true,
      showMessage = true,
      validateOnBlurOnly = true,

      unitLabel = 'Unit',
      unitLabels,
      formatResolved = DEFAULT_FORMAT_RESOLVED,
      unresolvedLabel = 'not set',

      messageRequired = 'Enter a size.',
      messageNotANumber = 'Enter a number.',
      messageNegative = 'A size cannot be negative.',
      messageMin,
      messageMax,
      messageInteger = 'Must resolve to a whole number of bytes.',

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
    const id = idProp ?? field.id ?? `${reactId}-bytes`;
    const size = sizeProp ?? field.size;
    const required = requiredProp ?? field.required;
    const messageId = `${id}-message`;
    const hintId = `${id}-hint`;
    const resolvedId = `${id}-resolved`;

    // The unit picker carries its own `aria-label`; the number beside it does
    // not, and "512" with no name is not a size.
    warnMissingNetFieldLabel('ByteSizeInput', {
      fieldId: field.id,
      fieldLabelId: field.labelId,
      ariaLabel: rest['aria-label'],
      ariaLabelledBy: rest['aria-labelledby'],
      explicitId: idProp,
    });

    const allowedUnits = useMemo<readonly ByteUnit[]>(() => {
      if (units && units.length > 0) return units;
      return base === 'decimal' ? DECIMAL_BYTE_UNITS : BINARY_BYTE_UNITS;
    }, [units, base]);

    const preferredUnit = defaultUnit ?? allowedUnits[0] ?? 'B';

    const [bytes, setBytes] = useControllableState<number | null>({
      value,
      defaultValue,
      onChange,
    });

    const [unitState, setUnitState] = useControllableState<ByteUnit>({
      value: unitProp,
      defaultValue: pickByteUnit(defaultValue, allowedUnits, preferredUnit),
      onChange: onUnitChange,
    });

    const [text, setText] = useState<string>(() =>
      bytes == null ? '' : numberToText(bytes / BYTE_UNIT_BYTES[unitState]),
    );
    const [touched, setTouched] = useState(false);

    /* Echo guard: reformatting on our own emission would rewrite the field
     * under the cursor; a genuine external change still resyncs it. */
    const selfValue = useRef<number | null>(bytes);

    useEffect(() => {
      if (bytes === selfValue.current) return;
      selfValue.current = bytes;
      const nextUnit = pickByteUnit(bytes, allowedUnits, unitState);
      if (nextUnit !== unitState) setUnitState(nextUnit);
      setText(bytes == null ? '' : numberToText(bytes / BYTE_UNIT_BYTES[nextUnit]));
    }, [bytes, allowedUnits, unitState, setUnitState]);

    const commit = useCallback(
      (nextText: string, nextUnit: ByteUnit) => {
        const trimmed = nextText.trim();
        if (trimmed.length === 0) {
          selfValue.current = null;
          setBytes(null);
          return;
        }
        const parsed = Number(trimmed);
        if (!Number.isFinite(parsed)) return;
        const resolved = parsed * BYTE_UNIT_BYTES[nextUnit];
        selfValue.current = resolved;
        setBytes(resolved);
      },
      [setBytes],
    );

    const validation = useMemo<ByteSizeValidation>(() => {
      const trimmed = text.trim();
      if (trimmed.length === 0) {
        return {
          empty: true,
          valid: !required,
          bytes: null,
          unit: unitState,
          message: required ? messageRequired : null,
        };
      }
      const parsed = Number(trimmed);
      if (!Number.isFinite(parsed)) {
        return {
          empty: false,
          valid: false,
          bytes: null,
          unit: unitState,
          message: messageNotANumber,
        };
      }
      const resolved = parsed * BYTE_UNIT_BYTES[unitState];
      if (!allowNegative && resolved < 0) {
        return {
          empty: false,
          valid: false,
          bytes: resolved,
          unit: unitState,
          message: messageNegative,
        };
      }
      if (integerOnly && !Number.isInteger(resolved)) {
        return {
          empty: false,
          valid: false,
          bytes: resolved,
          unit: unitState,
          message: messageInteger,
        };
      }
      if (min !== undefined && resolved < min) {
        return {
          empty: false,
          valid: false,
          bytes: resolved,
          unit: unitState,
          message: messageMin?.(min) ?? `Must be at least ${formatBytes(min, { base })}.`,
        };
      }
      if (max !== undefined && resolved > max) {
        return {
          empty: false,
          valid: false,
          bytes: resolved,
          unit: unitState,
          message: messageMax?.(max) ?? `Must be at most ${formatBytes(max, { base })}.`,
        };
      }
      return { empty: false, valid: true, bytes: resolved, unit: unitState, message: null };
    }, [
      text,
      unitState,
      required,
      allowNegative,
      integerOnly,
      min,
      max,
      base,
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
      const signature = `${validation.valid}|${validation.empty}|${validation.bytes ?? ''}|${validation.unit}|${validation.message ?? ''}`;
      if (lastSignature.current === signature) return;
      lastSignature.current = signature;
      emitValid(validation);
    }, [validation, emitValid]);

    const showProblem = !validation.valid && (touched || !validateOnBlurOnly);
    const state: ValidityState =
      invalidProp || field.invalid || showProblem ? 'invalid' : validation.empty ? 'empty' : 'valid';
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
        data-stratum="byte-size-input"
        data-size={size}
        data-validity={state}
        data-base={base}
        data-full-width={fullWidth || undefined}
        className={clsx('stratum-net-field', 'stratum-byte-size-input', wrapperClassName)}
      >
        <div
          className="stratum-net-control stratum-byte-size-input__control"
          data-size={size}
          data-validity={isInvalid ? 'invalid' : undefined}
          data-disabled={disabled || undefined}
          data-readonly={readOnly || undefined}
        >
          <input
            {...rest}
            ref={ref}
            id={id}
            /* See DurationInput: a native number input discards what it cannot
             * parse, including on paste, leaving a blank field and no reason. */
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

          {/* The framework's own Select — see DurationInput for why, and
            * ByteSizeInput.css for how the trigger is flattened into a segment
            * of the shared control shell. `stratum-net-unit` stays on it
            * because the shell draws the focus ring with
            * `:has(.stratum-net-unit:focus-visible)`. */}
          <Select
            className="stratum-net-unit stratum-byte-size-input__unit"
            size={size}
            aria-label={unitLabel}
            options={allowedUnits.map((u) => ({
              value: u,
              label: unitLabels?.[u] ?? u,
            }))}
            value={unitState}
            disabled={disabled || readOnly}
            onChange={(value) => {
              if (value == null) return;
              const next = value as ByteUnit;
              setUnitState(next);
              /* Re-interprets the same number rather than converting it. `512`
               * with `KiB` becomes `512` with `MiB` — picking a unit must not
               * rewrite the number the operator typed. */
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
              data-unobserved={validation.bytes == null || undefined}
            >
              {validation.bytes == null ? unresolvedLabel : formatResolved(validation.bytes, base)}
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

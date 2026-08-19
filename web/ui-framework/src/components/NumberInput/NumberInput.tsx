import {
  forwardRef,
  useCallback,
  useEffect,
  useRef,
  useState,
  type FocusEvent as ReactFocusEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import { Input, setNativeInputValue, useMergedRefs, type InputProps } from '../Input/Input';
import './NumberInput.css';

/** Press-and-hold: delay before repeat starts, then the repeat interval. */
const HOLD_DELAY_MS = 400;
const HOLD_INTERVAL_MS = 60;

/**
 * Strict decimal. Deliberately rejects `1e5`, `0x10`, `Infinity` and `NaN`,
 * all of which `Number()` happily accepts — none of them is something an
 * operator meant to type into a port number or a timeout.
 */
const DECIMAL_RE = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/;

function parseDecimal(text: string): number | null {
  const trimmed = text.trim();
  if (trimmed === '' || !DECIMAL_RE.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : null;
}

function clamp(value: number, min: number | undefined, max: number | undefined): number {
  let out = value;
  if (min !== undefined && Number.isFinite(min)) out = Math.max(out, min);
  if (max !== undefined && Number.isFinite(max)) out = Math.min(out, max);
  return out;
}

/** Decimal places a number is written with, including exponent notation. */
function decimalsOf(value: number): number {
  if (!Number.isFinite(value)) return 0;
  const text = String(value);
  const exponent = text.indexOf('e-');
  const dot = text.indexOf('.');
  if (exponent >= 0) {
    const fraction = dot >= 0 ? exponent - dot - 1 : 0;
    return Number(text.slice(exponent + 2)) + fraction;
  }
  return dot >= 0 ? text.length - dot - 1 : 0;
}

/** Kills binary float dust: 0.1 + 0.2 must display as 0.3, not 0.30000000000000004. */
function round(value: number, decimals: number): number {
  if (decimals <= 0) return Math.round(value);
  return Number(value.toFixed(Math.min(decimals, 15)));
}

export interface NumberInputProps
  extends Omit<
    InputProps,
    | 'value'
    | 'defaultValue'
    | 'onValueChange'
    | 'min'
    | 'max'
    | 'step'
    | 'type'
    | 'inputMode'
    | 'clearable'
    | 'onClear'
    | 'clearLabel'
  > {
  value?: number | null;
  defaultValue?: number | null;
  /** Fires with the parsed value, or `null` when the field is empty. */
  onValueChange?: (value: number | null) => void;
  min?: number;
  max?: number;
  /** Increment for the steppers and Arrow keys. Defaults to 1. */
  step?: number;
  /** Increment for PageUp / PageDown. Defaults to `step * 10`. */
  largeStep?: number;
  /** Hides the stepper buttons. Keyboard stepping still works. */
  hideStepper?: boolean;
  /** Allows the field to be left empty, reporting `null`. Defaults to true. */
  allowEmpty?: boolean;
  incrementLabel?: string;
  decrementLabel?: string;
  /** Announced as `aria-valuetext` while the field is empty. */
  emptyValueLabel?: string;
  /** Extra adornment, rendered before the steppers. */
  suffix?: ReactNode;
}

const IconStepUp = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.8"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="m4.5 9.75 3.5-3.5 3.5 3.5" />
  </svg>
);

const IconStepDown = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.8"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="m4.5 6.25 3.5 3.5 3.5-3.5" />
  </svg>
);

/**
 * Numeric field with steppers and full spinbutton keyboard support.
 *
 * WHY NOT `input[type=number]`
 * ----------------------------
 * Three defects rule it out for operator tooling. The mouse wheel silently
 * changes the value of a focused field, which is a data-corruption bug waiting
 * for someone to scroll a form. Partial input is unreadable: while a user is
 * typing `1e` or `-`, `element.value` returns the empty string, so validation
 * and controlled state see nothing. And Firefox permits arbitrary text in it
 * anyway, so the "native validation" argument does not hold.
 *
 * A text input with `inputMode="decimal"` and `role="spinbutton"` keeps the
 * mobile numeric keypad and the correct assistive-technology semantics — value,
 * minimum and maximum are reported explicitly — while leaving parsing,
 * clamping and stepping under our control, where they can be made consistent
 * across engines.
 *
 * BEHAVIOUR
 * ---------
 * - Typing commits the parsed value immediately but never clamps: clamping mid
 *   keystroke makes `50` impossible to type when the minimum is `10`.
 * - Clamping happens on blur, along with reverting text that never parsed.
 * - Stepping snaps to the step grid measured from `min` (or zero), rounding in
 *   the direction of travel — so `2.5` with `step=1` goes up to `3`, matching
 *   the native control.
 * - The steppers are `tabIndex={-1}`. Tab moves between fields; the value is
 *   adjusted with the Arrow keys, which is what the spinbutton role advertises.
 *   Trapping two extra tab stops in every numeric field is a real cost in a
 *   form with thirty of them.
 */
export const NumberInput = forwardRef<HTMLInputElement, NumberInputProps>(function NumberInput(
  {
    value: valueProp,
    defaultValue = null,
    onValueChange,
    min,
    max,
    step: stepProp = 1,
    largeStep,
    hideStepper = false,
    allowEmpty = true,
    incrementLabel = 'Increase',
    decrementLabel = 'Decrease',
    emptyValueLabel = 'Empty',
    suffix,
    size = 'md',
    disabled = false,
    readOnly = false,
    className,
    onChange,
    onKeyDown,
    onBlur,
    autoComplete = 'off',
    ...rest
  },
  ref,
) {
  const innerRef = useRef<HTMLInputElement | null>(null);
  const mergedRef = useMergedRefs<HTMLInputElement>(innerRef, ref);

  const [value, setValue] = useControllableState<number | null>({
    value: valueProp,
    defaultValue: defaultValue ?? null,
    onChange: onValueChange,
  });

  const [text, setText] = useState(() => (value == null ? '' : String(value)));

  // The last number the field actually held. Typing "1.2.3" passes through a
  // moment where the text no longer parses and the committed value is null, so
  // blur needs somewhere better than zero to fall back to. Written from an
  // effect, never during render.
  const lastValidRef = useRef<number | null>(value);

  useEffect(() => {
    if (value !== null) lastValidRef.current = value;

    // Pull the text back in sync when the value changed from outside. Guarded
    // on the parsed text so a half-typed "5." or "-" survives its own commit.
    // `text` is intentionally not a dependency: this reconciles external
    // changes, and reacting to local typing would fight the user.
    if (parseDecimal(text) !== value) {
      setText(value == null ? '' : String(value));
    }

  }, [value]);

  // A non-positive step divides by zero in the grid maths and puts NaN in the
  // DOM, which is unrecoverable for the user and silent for everyone else.
  const step = Number.isFinite(stepProp) && stepProp > 0 ? stepProp : 1;
  const bigStep =
    largeStep !== undefined && Number.isFinite(largeStep) && largeStep > 0
      ? largeStep
      : step * 10;
  const inert = disabled || readOnly;

  if (import.meta.env?.DEV) {
    if (step !== stepProp) {
      console.error(
        `[stratum] <NumberInput step={${String(stepProp)}}> must be a finite positive number. Falling back to 1.`,
      );
    }
    if (
      min !== undefined &&
      max !== undefined &&
      Number.isFinite(min) &&
      Number.isFinite(max) &&
      min > max
    ) {
      console.error(
        `[stratum] <NumberInput min={${min}} max={${max}}> has min greater than max. Every value will clamp to max.`,
      );
    }
  }

  /** Writes through the DOM so a consumer's native `onChange` fires too. */
  const commit = useCallback(
    (next: number | null) => {
      const nextText = next == null ? '' : String(next);
      const element = innerRef.current;
      // Nothing to write. Without this, a held stepper that has driven the
      // value to `min`/`max` keeps dispatching a no-op native `input` event
      // every 60ms for as long as the pointer is down. Normalisation still
      // commits: "007" -> "7" and "5." -> "5" both differ from `element.value`.
      if (element && element.value === nextText) return;
      if (!element || !setNativeInputValue(element, nextText)) {
        setText(nextText);
        setValue(next);
      }
    },
    [setValue],
  );

  const applyStep = useEventCallback((direction: 1 | -1, amount: number) => {
    if (inert) return;

    let next: number;
    if (value == null) {
      // Matches the native control: an empty field jumps to the minimum when
      // one exists, otherwise to a single step away from zero.
      next = min !== undefined && Number.isFinite(min) ? min : direction * amount;
    } else {
      const origin = min !== undefined && Number.isFinite(min) ? min : 0;
      const raw = value + direction * amount;
      const units = (raw - origin) / step;
      // Round toward travel so an off-grid value always moves at least one
      // unit; the epsilon absorbs float error such as 0.3 / 0.1 = 2.999…
      const snapped = direction > 0 ? Math.floor(units + 1e-9) : Math.ceil(units - 1e-9);
      next = origin + snapped * step;
    }

    const decimals = Math.max(decimalsOf(step), decimalsOf(amount), decimalsOf(next));
    commit(clamp(round(next, decimals), min, max));
  });

  /* -- Press and hold ----------------------------------------------------- */
  const holdRef = useRef<{
    timeout?: ReturnType<typeof setTimeout>;
    interval?: ReturnType<typeof setInterval>;
    detach?: () => void;
  }>({});

  const stopHold = useCallback(() => {
    const hold = holdRef.current;
    if (hold.timeout !== undefined) clearTimeout(hold.timeout);
    if (hold.interval !== undefined) clearInterval(hold.interval);
    hold.detach?.();
    holdRef.current = {};
  }, []);

  // Also runs on unmount, so a hold interrupted by a route change cannot leave
  // an interval or a window listener behind.
  useEffect(() => stopHold, [stopHold]);

  const startHold = (event: ReactPointerEvent<HTMLButtonElement>, direction: 1 | -1) => {
    // Primary button only, and never while the field cannot change.
    if (event.button !== 0 || inert) return;

    // Suppressing the compatibility mouse event keeps focus in the field
    // instead of moving it to the stepper. That matters twice over: the user
    // can go on typing or arrowing after a click, and blur-clamping does not
    // fire in the middle of an interaction that is still running.
    event.preventDefault();
    innerRef.current?.focus();

    stopHold();
    applyStep(direction, step);

    holdRef.current.timeout = setTimeout(() => {
      holdRef.current.interval = setInterval(() => applyStep(direction, step), HOLD_INTERVAL_MS);
    }, HOLD_DELAY_MS);

    // Listening on the window catches a release outside the button, which is
    // the common case once the pointer drifts during a long hold.
    window.addEventListener('pointerup', stopHold);
    window.addEventListener('pointercancel', stopHold);
    holdRef.current.detach = () => {
      window.removeEventListener('pointerup', stopHold);
      window.removeEventListener('pointercancel', stopHold);
    };
  };

  /* -- Keyboard ----------------------------------------------------------- */
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLInputElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented || inert) return;
    if (event.altKey || event.ctrlKey || event.metaKey) return;

    switch (event.key) {
      case 'ArrowUp':
        event.preventDefault();
        applyStep(1, step);
        break;
      case 'ArrowDown':
        event.preventDefault();
        applyStep(-1, step);
        break;
      case 'PageUp':
        event.preventDefault();
        applyStep(1, bigStep);
        break;
      case 'PageDown':
        event.preventDefault();
        applyStep(-1, bigStep);
        break;
      case 'Home':
        if (min !== undefined && Number.isFinite(min)) {
          event.preventDefault();
          commit(min);
        }
        break;
      case 'End':
        if (max !== undefined && Number.isFinite(max)) {
          event.preventDefault();
          commit(max);
        }
        break;
      default:
        break;
    }
  };

  const handleBlur = (event: ReactFocusEvent<HTMLInputElement>) => {
    onBlur?.(event);
    if (inert) return;

    const parsed = parseDecimal(text);

    if (parsed == null) {
      // Empty, or text that never parsed ("-", "1.2.3"): fall back to the last
      // good value rather than leaving the field in a state nothing can read.
      if (text.trim() === '' && allowEmpty) {
        if (value !== null) commit(null);
        else setText('');
        return;
      }
      commit(lastValidRef.current ?? clamp(0, min, max));
      return;
    }

    const clamped = clamp(parsed, min, max);
    // Always re-commit: this also normalises "007" and "5." to "7" and "5".
    commit(round(clamped, Math.max(decimalsOf(clamped), decimalsOf(step))));
  };

  const atMax = value != null && max !== undefined && Number.isFinite(max) && value >= max;
  const atMin = value != null && min !== undefined && Number.isFinite(min) && value <= min;

  const stepper = hideStepper ? null : (
    <span className="stratum-number-input__stepper">
      <button
        type="button"
        className="stratum-number-input__step"
        data-action="increment"
        aria-label={incrementLabel}
        tabIndex={-1}
        disabled={inert || atMax}
        onPointerDown={(event) => startHold(event, 1)}
      >
        <IconStepUp />
      </button>
      <button
        type="button"
        className="stratum-number-input__step"
        data-action="decrement"
        aria-label={decrementLabel}
        tabIndex={-1}
        disabled={inert || atMin}
        onPointerDown={(event) => startHold(event, -1)}
      >
        <IconStepDown />
      </button>
    </span>
  );

  // An empty fragment would still render an adornment slot and its gap.
  const suffixNode =
    stepper == null && suffix == null ? undefined : (
      <>
        {suffix}
        {stepper}
      </>
    );

  return (
    <Input
      {...rest}
      ref={mergedRef}
      data-stratum="number-input"
      className={clsx('stratum-number-input', className)}
      inputClassName={clsx('stratum-number-input__control', rest.inputClassName)}
      type="text"
      inputMode="decimal"
      autoComplete={autoComplete}
      size={size}
      disabled={disabled}
      readOnly={readOnly}
      value={text}
      role="spinbutton"
      aria-valuenow={value ?? undefined}
      aria-valuemin={min !== undefined && Number.isFinite(min) ? min : undefined}
      aria-valuemax={max !== undefined && Number.isFinite(max) ? max : undefined}
      // Without a value there is nothing for aria-valuenow to report, so the
      // text has to say why: empty, or the literal characters that failed to
      // parse. Announcing "Empty" over a field containing "1.2.3" would be a
      // lie a screen reader user has no way to check.
      // Merged rather than assigned: these are written after `{...rest}`, and a
      // bare `undefined` there would delete a consumer's own value.
      aria-valuetext={
        rest['aria-valuetext'] ??
        (value == null ? (text.trim() === '' ? emptyValueLabel : text) : undefined)
      }
      // The spinbutton role replaces the input's implicit textbox role, and
      // with it the mapping of the native `readonly` attribute.
      aria-readonly={rest['aria-readonly'] ?? (readOnly || undefined)}
      onKeyDown={handleKeyDown}
      onBlur={handleBlur}
      onChange={(event) => {
        const next = event.target.value;
        setText(next);
        // Commit unclamped: clamping while typing makes values above the
        // minimum's first digit impossible to enter.
        setValue(parseDecimal(next));
        onChange?.(event);
      }}
      suffix={suffixNode}
    />
  );
});

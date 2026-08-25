import {
  forwardRef,
  useCallback,
  useRef,
  type InputHTMLAttributes,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type Ref,
  type RefCallback,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useFieldControl } from '../Field/Field';
import './Input.css';

export type InputSize = 'sm' | 'md' | 'lg';
export type InputVariant = 'default' | 'subtle';

export interface InputProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'prefix' | 'size' | 'value' | 'defaultValue'
  > {
  size?: InputSize;
  variant?: InputVariant;
  /**
   * Marks the field as failing validation. Sets `aria-invalid` unless the
   * consumer (or a `Field` wrapper) already passed one explicitly.
   */
  invalid?: boolean;
  /**
   * Leading adornment — an icon, a unit, a protocol scheme. Left in the
   * accessibility tree, since a unit is usually information the user needs;
   * mark purely decorative icons `aria-hidden` yourself.
   */
  prefix?: ReactNode;
  /** Trailing adornment. Rendered after the clear button. */
  suffix?: ReactNode;
  /** Shows a clear button once the field has a value. */
  clearable?: boolean;
  /** Accessible name for the clear button. */
  clearLabel?: string;
  /** Called after the value has been cleared. */
  onClear?: () => void;
  fullWidth?: boolean;
  value?: string | number;
  defaultValue?: string | number;
  /** Fires alongside the native `onChange`, with just the string value. */
  onValueChange?: (value: string) => void;
  /** Class applied to the inner `<input>` rather than the wrapper. */
  inputClassName?: string;
  /** Ref to the wrapper element. The forwarded ref goes to the `<input>`. */
  wrapperRef?: Ref<HTMLDivElement>;
  /** Component identity on the wrapper. Overridden by the built-on variants. */
  'data-stratum'?: string;
}

/* -------------------------------------------------------------------------- */
/* Shared internals — used by PasswordInput / NumberInput / SearchInput.       */
/* -------------------------------------------------------------------------- */

function assignRef<T>(ref: Ref<T> | undefined, value: T | null): void {
  if (typeof ref === 'function') {
    ref(value);
    return;
  }
  if (ref) {
    (ref as { current: T | null }).current = value;
  }
}

/**
 * Fans one node out to several refs.
 *
 * Recreated when the ref identities change rather than being permanently
 * stable: a stable callback would never hand the node to a ref that arrived
 * later, which is the failure mode people hit when a parent swaps refs.
 */
export function useMergedRefs<T>(...refs: (Ref<T> | undefined)[]): RefCallback<T> {
  return useCallback((node: T | null) => {
    for (const ref of refs) assignRef(ref, node);
    // The deps array length is fixed per call site, which is what the rule
    // of hooks actually requires.

  }, refs);
}

/**
 * Writes a value into a DOM input the way a user would, so React's own change
 * event fires.
 *
 * Setting `element.value` directly is invisible to React: it patches the
 * `value` property on the instance to track the last-seen value, so a direct
 * assignment updates the DOM but never produces an `onChange`. Going through
 * the prototype's setter updates the real value while leaving React's tracker
 * out of date, and the dispatched `input` event then reads as a genuine user
 * edit. This is what makes a clear button, a generated password or a stepper
 * click reach `react-hook-form`, Formik, or any plain `onChange` handler,
 * instead of silently updating only our internal state.
 *
 * @returns `true` when the synthetic edit was dispatched.
 */
export function setNativeInputValue(
  element: HTMLInputElement | HTMLTextAreaElement,
  value: string,
): boolean {
  const prototype = Object.getPrototypeOf(element) as object | null;
  const descriptor = prototype
    ? Object.getOwnPropertyDescriptor(prototype, 'value')
    : undefined;

  if (!descriptor?.set) return false;

  descriptor.set.call(element, value);
  element.dispatchEvent(new Event('input', { bubbles: true }));
  return true;
}

/** True when an `aria-invalid` value means "invalid". */
export function isAriaInvalid(value: unknown): boolean {
  return value === true || (typeof value === 'string' && value !== 'false');
}

const IconClear = () => (
  <svg
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.7"
    strokeLinecap="round"
    aria-hidden="true"
    focusable="false"
  >
    <path d="m4.75 4.75 6.5 6.5m0-6.5-6.5 6.5" />
  </svg>
);

/* -------------------------------------------------------------------------- */

/**
 * Single-line text field.
 *
 * PROP ROUTING
 * ------------
 * `className` and `style` land on the wrapper, because that is the box a
 * consumer needs to size and space. Everything else in `...rest` lands on the
 * `<input>`, because those are native input attributes — `id`, `name`,
 * `placeholder`, `autoComplete`, `aria-describedby`, `aria-errormessage` — and
 * putting them on a wrapper would break both forms and screen readers. Use
 * `inputClassName` / `wrapperRef` when you need the other end.
 *
 * ADORNMENTS AND FOCUS
 * --------------------
 * The border, background and focus ring live on the wrapper, not the input, so
 * adding a prefix or suffix cannot leave the ring wrapped around only part of
 * the control. The wrapper is not focusable; it forwards clicks on its padding
 * and non-interactive adornments to the input, and deliberately leaves clicks
 * on the input alone so text selection by dragging still works.
 *
 * The state is always React-controlled internally (via `useControllableState`)
 * so `clearable` can know whether there is a value in both controlled and
 * uncontrolled usage, and so `onValueChange` fires identically in both.
 */
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  {
    size: sizeProp,
    variant = 'default',
    invalid: invalidProp,
    prefix,
    suffix,
    clearable = false,
    clearLabel = 'Clear',
    onClear,
    fullWidth = false,
    value: valueProp,
    defaultValue,
    onValueChange,
    onChange,
    inputClassName,
    wrapperRef,
    className,
    style,
    disabled = false,
    readOnly = false,
    required: requiredProp,
    id: idProp,
    type = 'text',
    'data-stratum': dataStratum = 'input',
    'aria-invalid': ariaInvalidProp,
    'aria-required': ariaRequiredProp,
    'aria-describedby': ariaDescribedByProp,
    ...rest
  },
  ref,
) {
  const field = useFieldControl();
  const size = sizeProp ?? field.size;
  const invalid = invalidProp ?? field.invalid;
  const id = idProp ?? field.id;
  const describedBy = clsx(field.describedBy, ariaDescribedByProp) || undefined;
  const ariaRequired = ariaRequiredProp ?? (requiredProp || field.required || undefined);
  const innerRef = useRef<HTMLInputElement | null>(null);
  const inputRef = useMergedRefs<HTMLInputElement>(innerRef, ref);

  const [value, setValue] = useControllableState<string>({
    value: valueProp === undefined ? undefined : String(valueProp),
    defaultValue: defaultValue === undefined ? '' : String(defaultValue),
    onChange: onValueChange,
  });

  // An explicit `aria-invalid` wins over the `invalid` prop in BOTH channels.
  // Deriving `data-invalid` with an OR would let a wrapper that emits
  // `aria-invalid={String(hasError)}` paint the danger border while telling
  // assistive tech the field is valid — meaning carried by colour alone for
  // one of the two audiences.
  const isInvalid = ariaInvalidProp !== undefined ? isAriaInvalid(ariaInvalidProp) : invalid;
  const showClear = clearable && !disabled && !readOnly && value.length > 0;

  const handleClear = () => {
    const element = innerRef.current;
    // Focus first: the clear button unmounts the moment the value empties, and
    // a keyboard user whose focus lands on <body> has lost their place.
    element?.focus();
    if (!element || !setNativeInputValue(element, '')) {
      setValue('');
    }
    onClear?.();
  };

  /**
   * Clicking the wrapper's padding or a decorative adornment should focus the
   * field, exactly as clicking a label does.
   */
  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (disabled) return;
    const target = event.target as HTMLElement | null;
    const element = innerRef.current;
    if (!target || !element || target === element) return;
    // Real controls inside an adornment keep their own click behaviour.
    if (target.closest('button, a, input, select, textarea, label, [tabindex]')) return;
    event.preventDefault();
    element.focus();
  };

  return (
    <div
      ref={wrapperRef}
      data-stratum={dataStratum}
      data-size={size}
      data-variant={variant}
      data-invalid={isInvalid || undefined}
      data-disabled={disabled || undefined}
      data-readonly={readOnly || undefined}
      data-full-width={fullWidth || undefined}
      className={clsx('stratum-input', className)}
      style={style}
      onPointerDown={handlePointerDown}
    >
      {prefix != null && prefix !== false && (
        <span className="stratum-input__adornment" data-side="start">
          {prefix}
        </span>
      )}

      <input
        {...rest}
        ref={inputRef}
        id={id}
        type={type}
        className={clsx('stratum-input__control', inputClassName)}
        value={value}
        disabled={disabled}
        readOnly={readOnly}
        required={requiredProp}
        aria-invalid={ariaInvalidProp ?? (invalid || undefined)}
        aria-required={ariaRequired}
        aria-describedby={describedBy}
        onChange={(event) => {
          setValue(event.target.value);
          onChange?.(event);
        }}
      />

      {showClear && (
        <button
          type="button"
          className="stratum-input__action"
          data-action="clear"
          aria-label={clearLabel}
          onClick={handleClear}
        >
          <IconClear />
        </button>
      )}

      {suffix != null && suffix !== false && (
        <span className="stratum-input__adornment" data-side="end">
          {suffix}
        </span>
      )}
    </div>
  );
});

import {
  createContext,
  forwardRef,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type Ref,
} from 'react';
import clsx from 'clsx';
import { useControllableState } from '../../hooks/useControllableState';
import { useEventCallback } from '../../hooks/useEventCallback';
import './Radio.css';

export type RadioSize = 'sm' | 'md' | 'lg';
export type RadioGroupOrientation = 'horizontal' | 'vertical';

interface RadioGroupContextValue {
  name: string;
  value: string | null;
  select: (value: string) => void;
  disabled: boolean;
  required: boolean;
  invalid: boolean;
  size: RadioSize;
  /** The single value allowed in the tab order. */
  rovingValue: string | null;
  /** Re-reads DOM order after a radio mounts, unmounts or changes disabled. */
  syncOrder: () => void;
}

const RadioGroupContext = createContext<RadioGroupContextValue | null>(null);

const INPUT_SELECTOR = '.stratum-radio__input';

/** `useLayoutEffect` warns during SSR; there is no layout to read there. */
const useIsomorphicLayoutEffect = typeof document === 'undefined' ? useEffect : useLayoutEffect;

function assignRef<T>(ref: Ref<T> | undefined, node: T | null): void {
  if (typeof ref === 'function') ref(node);
  else if (ref) (ref as { current: T | null }).current = node;
}

/** Enabled radios of this group, in document order. */
function groupInputs(root: HTMLElement | null, name: string): HTMLInputElement[] {
  if (!root) return [];
  return Array.from(root.querySelectorAll<HTMLInputElement>(INPUT_SELECTOR)).filter(
    (input) => input.name === name && !input.disabled,
  );
}

function isRtl(element: HTMLElement): boolean {
  return getComputedStyle(element).direction === 'rtl';
}

/* ========================================================================== */
/* RadioGroup                                                                  */
/* ========================================================================== */

export interface RadioGroupProps
  extends Omit<HTMLAttributes<HTMLDivElement>, 'onChange' | 'defaultValue' | 'defaultChecked'> {
  /** Controlled selected value. */
  value?: string | null;
  /** Initial selected value for the uncontrolled case. */
  defaultValue?: string | null;
  /** Fires in both controlled and uncontrolled modes. */
  onValueChange?: (value: string) => void;
  /** Shared form control name. Generated when omitted. */
  name?: string;
  disabled?: boolean;
  required?: boolean;
  invalid?: boolean;
  size?: RadioSize;
  orientation?: RadioGroupOrientation;
  /** Group label. Becomes the group's accessible name via `aria-labelledby`. */
  label?: ReactNode;
  /** Hides the label visually while keeping it as the accessible name. */
  hideLabel?: boolean;
  /** Secondary line under the label, wired up as `aria-describedby`. */
  description?: ReactNode;
  children?: ReactNode;
}

/**
 * Exclusive choice group.
 *
 * KEYBOARD MODEL
 * --------------
 * A radio group is one tab stop. Only the checked radio is tabbable; when
 * nothing is checked the first *enabled* radio takes the tab stop, so a
 * keyboard user always has a way in. Arrow keys move focus and selection
 * together (selection follows focus, per the WAI-ARIA radio group pattern),
 * wrapping at both ends. `Home`/`End` jump to the first and last enabled radio.
 *
 * The browser's own arrow handling for same-name radios is suppressed with
 * `preventDefault` and replaced, because engines disagree on wrapping, none of
 * them implement `Home`/`End`, and Firefox will happily move selection into a
 * visually separate group that happens to share a name.
 *
 * The tab-stop and arrow order are read back from the DOM rather than from a
 * registration list, so conditionally rendered and re-ordered radios stay
 * correct without every child having to report its index.
 */
export const RadioGroup = forwardRef<HTMLDivElement, RadioGroupProps>(function RadioGroup(
  {
    value,
    defaultValue = null,
    onValueChange,
    name,
    disabled = false,
    required = false,
    invalid = false,
    size = 'md',
    orientation = 'vertical',
    label,
    hideLabel = false,
    description,
    className,
    children,
    onKeyDown,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const groupName = name ?? `${generatedId}-radio-group`;
  const labelId = `${generatedId}-label`;
  const descriptionId = `${generatedId}-description`;

  const [selected, setSelected] = useControllableState<string | null>({
    value,
    defaultValue,
    onChange: (next) => {
      if (next !== null) onValueChange?.(next);
    },
  });

  const rootRef = useRef<HTMLDivElement | null>(null);
  const setRefs = useCallback(
    (node: HTMLDivElement | null) => {
      rootRef.current = node;
      assignRef(ref, node);
    },
    [ref],
  );

  // Enabled values in DOM order. Only used to decide the fallback tab stop, so
  // it is cheap to recompute and never drives paint.
  const [order, setOrder] = useState<string[]>([]);
  const syncOrder = useEventCallback(() => {
    const next = groupInputs(rootRef.current, groupName).map((input) => input.value);
    setOrder((prev) =>
      prev.length === next.length && prev.every((v, i) => v === next[i]) ? prev : next,
    );
  });

  // Layout, not passive: the tab stop is derived from this DOM read, and a
  // passive effect runs after paint — so the first painted frame would be a
  // radio group that Tab skips entirely.
  useIsomorphicLayoutEffect(() => {
    syncOrder();
  }, [syncOrder]);

  const select = useCallback(
    (next: string) => {
      setSelected(next);
    },
    [setSelected],
  );

  // Before the first DOM read — and in the SSR/pre-hydration markup, where
  // there is none — `order` is empty. A selected value is still the right tab
  // stop then, so trust it rather than emitting a group nothing can Tab into.
  const rovingValue =
    selected !== null && (order.length === 0 || order.includes(selected))
      ? selected
      : (order[0] ?? null);

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(event);
    if (event.defaultPrevented || disabled) return;
    if (event.altKey || event.ctrlKey || event.metaKey) return;

    const root = rootRef.current;
    if (!root) return;

    const inputs = groupInputs(root, groupName);
    if (inputs.length === 0) return;

    const currentIndex = inputs.indexOf(event.target as HTMLInputElement);
    if (currentIndex === -1) return;

    const rtl = isRtl(root);
    const last = inputs.length - 1;
    let nextIndex: number;

    switch (event.key) {
      case 'ArrowDown':
        nextIndex = currentIndex === last ? 0 : currentIndex + 1;
        break;
      case 'ArrowUp':
        nextIndex = currentIndex === 0 ? last : currentIndex - 1;
        break;
      case 'ArrowRight':
        nextIndex = rtl
          ? currentIndex === 0
            ? last
            : currentIndex - 1
          : currentIndex === last
            ? 0
            : currentIndex + 1;
        break;
      case 'ArrowLeft':
        nextIndex = rtl
          ? currentIndex === last
            ? 0
            : currentIndex + 1
          : currentIndex === 0
            ? last
            : currentIndex - 1;
        break;
      case 'Home':
        nextIndex = 0;
        break;
      case 'End':
        nextIndex = last;
        break;
      default:
        return;
    }

    const next = inputs[nextIndex];
    if (!next) return;

    event.preventDefault();
    next.focus();
    select(next.value);
  };

  const context = useMemo<RadioGroupContextValue>(
    () => ({
      name: groupName,
      value: selected,
      select,
      disabled,
      required,
      invalid,
      size,
      rovingValue,
      syncOrder,
    }),
    [groupName, selected, select, disabled, required, invalid, size, rovingValue, syncOrder],
  );

  if (
    import.meta.env?.DEV &&
    label == null &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error(
      '[stratum] <RadioGroup> requires `label`, `aria-label` or `aria-labelledby`. `role="radiogroup"` without a name is announced as a bare "radio group".',
    );
  }

  return (
    <div
      {...rest}
      ref={setRefs}
      role="radiogroup"
      data-stratum="radio-group"
      data-orientation={orientation}
      data-size={size}
      data-invalid={invalid || undefined}
      data-disabled={disabled || undefined}
      className={clsx('stratum-radio-group', className)}
      aria-labelledby={clsx(rest['aria-labelledby'], label != null && labelId) || undefined}
      aria-describedby={
        clsx(rest['aria-describedby'], description != null && descriptionId) || undefined
      }
      // Merged, not assigned: these are written after `{...rest}`, where a bare
      // `undefined` would delete a consumer's own value.
      aria-required={rest['aria-required'] ?? (required || undefined)}
      aria-invalid={rest['aria-invalid'] ?? (invalid || undefined)}
      aria-disabled={rest['aria-disabled'] ?? (disabled || undefined)}
      aria-orientation={orientation}
      onKeyDown={handleKeyDown}
    >
      {label != null && (
        <span
          id={labelId}
          className={clsx('stratum-radio-group__label', hideLabel && 'stratum-visually-hidden')}
        >
          {label}
        </span>
      )}
      {description != null && (
        <span id={descriptionId} className="stratum-radio-group__description">
          {description}
        </span>
      )}
      <div className="stratum-radio-group__items">
        <RadioGroupContext.Provider value={context}>{children}</RadioGroupContext.Provider>
      </div>
    </div>
  );
});

/* ========================================================================== */
/* Radio                                                                       */
/* ========================================================================== */

export interface RadioProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    'size' | 'type' | 'checked' | 'defaultChecked' | 'value' | 'name'
  > {
  /** Value submitted and compared against the group's selection. */
  value: string;
  /** Overrides the group size for this option. */
  size?: RadioSize;
  /** Overrides the group invalid state for this option. */
  invalid?: boolean;
  /** Secondary line under the label, wired up as `aria-describedby`. */
  description?: ReactNode;
  /** Visible label. */
  children?: ReactNode;
}

/**
 * One option inside a `<RadioGroup>`.
 *
 * Throws outside a group rather than degrading: without the group there is no
 * shared `name`, no roving tab stop and no arrow navigation, so a lone radio is
 * a silently broken control — and silently broken keyboard behaviour is the
 * failure mode that survives review the longest.
 */
export const Radio = forwardRef<HTMLInputElement, RadioProps>(function Radio(
  {
    value,
    size,
    invalid,
    disabled = false,
    description,
    className,
    style,
    children,
    id,
    onChange,
    ...rest
  },
  ref,
) {
  const group = useContext(RadioGroupContext);
  if (!group) {
    throw new Error('[stratum] <Radio> must be rendered inside a <RadioGroup>.');
  }

  const generatedId = useId();
  const inputId = id ?? `${generatedId}-input`;
  const descriptionId = `${generatedId}-description`;

  const isDisabled = disabled || group.disabled;
  const isInvalid = invalid ?? group.invalid;
  const resolvedSize = size ?? group.size;
  const checked = group.value === value;

  const { syncOrder } = group;
  // DOM order is the source of truth for the tab stop, so every mount, unmount
  // and disabled flip asks the group to re-read it. Layout, not passive: the
  // group's tab stop must be settled before the browser paints.
  useIsomorphicLayoutEffect(() => {
    syncOrder();
    return syncOrder;
  }, [syncOrder, isDisabled, value]);

  if (
    import.meta.env?.DEV &&
    children == null &&
    !rest['aria-label'] &&
    !rest['aria-labelledby']
  ) {
    console.error('[stratum] <Radio> without children requires `aria-label` or `aria-labelledby`.');
  }

  return (
    <span
      data-stratum="radio"
      data-size={resolvedSize}
      data-state={checked ? 'checked' : 'unchecked'}
      data-invalid={isInvalid || undefined}
      data-disabled={isDisabled || undefined}
      data-block={description != null || undefined}
      className={clsx('stratum-radio', className)}
      style={style}
    >
      <span className="stratum-radio__control">
        <input
          {...rest}
          ref={ref}
          id={inputId}
          type="radio"
          className="stratum-radio__input"
          name={group.name}
          value={value}
          checked={checked}
          disabled={isDisabled}
          // Merged, not assigned: written after `{...rest}`, where a bare
          // `undefined` would delete a consumer's own value.
          required={rest.required ?? (group.required || undefined)}
          aria-invalid={rest['aria-invalid'] ?? (isInvalid || undefined)}
          aria-describedby={
            clsx(rest['aria-describedby'], description != null && descriptionId) || undefined
          }
          // Exactly one radio per group is reachable with Tab; arrows move
          // within the group from there.
          tabIndex={group.rovingValue === value ? 0 : -1}
          onChange={(event: ChangeEvent<HTMLInputElement>) => {
            group.select(value);
            onChange?.(event);
          }}
        />
        <span className="stratum-radio__dot" aria-hidden="true" />
      </span>

      {(children != null || description != null) && (
        <span className="stratum-radio__text">
          {children != null && (
            <label className="stratum-radio__label" htmlFor={inputId}>
              {children}
            </label>
          )}
          {description != null && (
            <span className="stratum-radio__description" id={descriptionId}>
              {description}
            </span>
          )}
        </span>
      )}
    </span>
  );
});
